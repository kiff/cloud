// Package settle says how each held call ended, for its settled receipt:
// it reads the tenant's Card exceptions, who answered them
// (membership hold answers) and what the tool reported (gateway
// records), and lists the holds that are settled.
package settle

import (
	"context"
	"time"

	"github.com/kiff/cloud/api/internal/mandate"
	"github.com/kiff/cloud/api/internal/membership"
	"github.com/kiff/cloud/api/internal/receipt"
	"github.com/kiff/cloud/api/internal/tooloutcome"
)

// Grace is receipt.SettlementGrace: how long a used approval waits for
// the tool's report before it settles without one.
const Grace = receipt.SettlementGrace

// Exceptions lists a tenant's Card exceptions (mandate.Store).
type Exceptions interface {
	Exceptions(ctx context.Context, tenantID string, status mandate.ExceptionStatus) ([]mandate.Exception, error)
}

// Answers reads who answered holds (membership.Reader).
type Answers interface {
	HoldAnswers(ctx context.Context, tenantID string, holdIDs []string) (map[string]membership.HoldAnswer, error)
}

// Source implements receipt.SettlementSource. Answers and Tools are
// optional: without them a settled receipt says less, never something
// untrue.
type Source struct {
	Exceptions Exceptions
	Answers    Answers
	Tools      tooloutcome.Reader
	Now        func() time.Time
}

// Settled lists the tenant's settled holds.
func (s Source) Settled(ctx context.Context, tenantID string) ([]receipt.Settlement, error) {
	now := time.Now()
	if s.Now != nil {
		now = s.Now()
	}
	es, err := s.Exceptions.Exceptions(ctx, tenantID, "")
	if err != nil {
		return nil, err
	}
	var holdIDs, proposals []string
	for _, e := range es {
		if e.ProposalID == "" {
			continue
		}
		holdIDs = append(holdIDs, e.ID)
		proposals = append(proposals, e.ProposalID)
	}
	answers := map[string]membership.HoldAnswer{}
	if s.Answers != nil && len(holdIDs) > 0 {
		// Best effort: without attribution the receipt names no role.
		if a, err := s.Answers.HoldAnswers(ctx, tenantID, holdIDs); err == nil {
			answers = a
		}
	}
	tools := map[string]tooloutcome.Outcome{}
	if s.Tools != nil && len(proposals) > 0 {
		t, err := s.Tools.Outcomes(ctx, tenantID, proposals)
		if err != nil {
			// A failed read must not settle a call as having no report.
			return nil, err
		}
		tools = t
	}
	var out []receipt.Settlement
	for _, e := range es {
		if e.ProposalID == "" {
			continue
		}
		set, ok := settlement(e, now, answers, tools)
		if ok {
			out = append(out, set)
		}
	}
	return out, nil
}

// settlement is how e ended, and whether it has.
func settlement(e mandate.Exception, now time.Time, answers map[string]membership.HoldAnswer, tools map[string]tooloutcome.Outcome) (receipt.Settlement, bool) {
	s := receipt.Settlement{ProposalID: e.ProposalID, HoldID: e.ID, AnsweredBy: e.ResolvedBy}
	if e.ResolvedAt != nil {
		s.At = *e.ResolvedAt
	}
	if a := e.ExternalAnswer; a != nil && a.Decision != "defer" {
		s.ExternalApprover = a.Principal
		if a.Name != "" {
			s.ExternalApprover = a.Name
		}
	}
	tool, hasTool := tools[e.ProposalID]
	switch e.StatusAt(now) {
	case mandate.ExceptionRejected:
		s.Outcome, s.Answer = "rejected", "reject"
		if r := e.Refusal; r != nil {
			s.NextStep, s.Note = r.NextStep, r.Note
		}
	case mandate.ExceptionExpired:
		s.Outcome, s.AnsweredBy = "expired", ""
		if e.ExpiresAt != nil && (e.ResolvedAt == nil || e.ExpiresAt.Before(*e.ResolvedAt)) {
			s.At = *e.ExpiresAt
		}
	case mandate.ExceptionInvalidated:
		s.Outcome, s.Reason = "withdrawn", e.Reason
	case mandate.ExceptionConsumed:
		s.Outcome, s.Answer = "approved_once", "approve-once"
		since := s.At
		if e.ConsumedAt != nil {
			since = *e.ConsumedAt
		}
		switch {
		case hasTool && tool.Status != tooloutcome.Sending:
			s.Tool = &receipt.SettlementTool{Status: string(tool.Status), Message: tool.Message, At: tool.At}
		case hasTool, now.Sub(since) < Grace:
			// Sent and not answered yet, or the report may still come.
			return s, false
		}
	case mandate.ExceptionChanged:
		// Change Card stores the change and decides nothing: the call is
		// re-decided when the agent asks again. The pipeline settles it
		// only once the trail has that retry, and waits the grace from
		// the retry, not the change.
		s.Outcome, s.Answer, s.AwaitRetry = "card_changed", "change-card", true
		// Nobody answered: on a retry it fit the Card as it then stood,
		// and the authorizer let it through.
		if e.ResolvedBy == "" && e.Reason == "fits the card as it now stands" {
			s.Outcome, s.Answer, s.Reason = "fit_on_retry", "", e.Reason
		}
		switch {
		case hasTool && tool.Status == tooloutcome.Sending:
			return s, false
		case hasTool:
			s.Tool = &receipt.SettlementTool{Status: string(tool.Status), Message: tool.Message, At: tool.At}
		}
	default:
		// Held, approved and not yet used, or approved with no room: not
		// settled.
		return s, false
	}
	if s.At.IsZero() {
		return s, false
	}
	if a, ok := answers[e.ID]; ok && a.Answer == s.Answer && s.Answer != "" {
		s.Role = string(a.Role)
		sod := a.SeparationOfDuties
		s.SeparationOfDuties = &sod
		if d := a.Device; d != nil {
			s.Device = &receipt.SettlementDevice{Name: d.Name, KeyID: d.KeyID, PublicKey: d.PublicKey, Message: d.Message, Signature: d.Signature}
		}
	}
	return s, true
}
