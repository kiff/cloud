package evidence

import (
	"context"
	"log/slog"

	"github.com/kiff/cloud/api/internal/domain"
	"github.com/kiff/cloud/api/internal/posture"
	"github.com/kiff/cloud/api/internal/proposals"
)

// collectPosture reads the tenant's posture-change history (G5) and
// derives the current mode plus the became-enforce / last-changed
// moments. Unavailable when no posture reader is wired (preserves the
// proof-pack shape, which doesn't render posture). Empty when the
// reader is wired but no change is recorded.
func (c *Composer) collectPosture(ctx context.Context, tenantID string) (PostureSection, error) {
	if c.posture == nil {
		return PostureSection{Status: StatusUnavailable}, nil
	}
	transitions, err := c.posture.Transitions(ctx, tenantID)
	if err != nil {
		// Secondary section: posture is decoration on the decisions
		// payload, not the payload. A read error degrades the section
		// to Unavailable (logged) rather than failing the whole object
		// — the prior /decisions contract: never fail the decisions
		// read on a posture hiccup.
		c.logger.WarnContext(ctx, "evidence: posture history read failed",
			slog.String("tenant", tenantID), slog.String("err", err.Error()))
		return PostureSection{Status: StatusUnavailable}, nil
	}
	hist := posture.History{Transitions: transitions}
	sec := PostureSection{Status: StatusEmpty}
	if last, ok := hist.LastChange(); ok {
		sec.Status = StatusComplete
		sec.LastChangedAt = last.ChangedAt
		// Current effective mode is the `to` of the most recent change.
		// Normalized because older transitions stored the
		// legacy "shadow" spelling, and evidence must read in one
		// vocabulary regardless of when the flip happened.
		sec.Current = domain.NormalizeControlMode(last.To)
	}
	if be, ok := hist.LastEnforce(); ok {
		sec.BecameEnforceAt = be.ChangedAt
		sec.BecameEnforceBy = be.ChangedBy
	}
	return sec, nil
}

// collectAlerts enriches the decision rows with their alert deliveries
// (G4), keyed by proposal_id — the natural join the delivery store
// already records. Unavailable when no delivery store is wired. Empty
// when no row has a recorded delivery.
func (c *Composer) collectAlerts(ctx context.Context, tenantID string, rows []proposals.DecisionRow) (AlertsSection, error) {
	if c.deliveries == nil {
		return AlertsSection{Status: StatusUnavailable}, nil
	}
	sec := AlertsSection{Status: StatusEmpty, Deliveries: []AlertDelivery{}}
	seen := map[string]bool{}
	for _, row := range rows {
		pid := row.ProposalID
		if pid == "" || seen[pid] {
			continue
		}
		seen[pid] = true
		ds, err := c.deliveries.ListDeliveries(ctx, tenantID, pid)
		if err != nil {
			// Secondary section: degrade rather than fail the object.
			c.logger.WarnContext(ctx, "evidence: alert deliveries read failed",
				slog.String("tenant", tenantID), slog.String("proposal_id", pid), slog.String("err", err.Error()))
			return AlertsSection{Status: StatusUnavailable}, nil
		}
		for _, d := range ds {
			sec.Deliveries = append(sec.Deliveries, AlertDelivery{
				ProposalID:  d.ProposalID,
				TargetID:    d.TargetID,
				TargetType:  d.TargetType,
				Status:      d.Status,
				Detail:      d.Detail,
				AttemptedAt: d.AttemptedAt,
			})
		}
	}
	if len(sec.Deliveries) > 0 {
		sec.Status = StatusComplete
	}
	return sec, nil
}
