package handlers

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"time"

	"github.com/kiff/cloud/api/internal/alert"
	"github.com/kiff/cloud/api/internal/meter"
	"github.com/kiff/cloud/api/internal/posture"
	"github.com/kiff/cloud/api/internal/proposals"
	"github.com/kiff/cloud/api/internal/receipt"
	"github.com/kiff/cloud/api/internal/tenant"
	"github.com/kiff/cloud/api/internal/usage"
)

// opsDetailLimit caps the recent receipts / proposals rows shown in
// the per-tenant detail view.
const opsDetailLimit = 20

// OpsTenantRow is one row in GET /v1/ops/tenants and the header of
// the per-tenant detail view.
type OpsTenantRow struct {
	ID             string     `json:"id"`
	Slug           string     `json:"slug"`
	DisplayName    string     `json:"display_name,omitempty"`
	Status         string     `json:"status"`
	Plan           string     `json:"plan"`
	CreatedAt      time.Time  `json:"created_at"`
	LastActivityAt *time.Time `json:"last_activity_at,omitempty"`
	// Label is the operators' own label (internal, eval, e2e, demo), ""
	// for none; BilledBy who bills the plan (stripe, apple, google,
	// contract), "" for none.
	Label    string `json:"label,omitempty"`
	BilledBy string `json:"billed_by,omitempty"`
	// KeyPrefix starts every API key of the account's.
	KeyPrefix string `json:"key_prefix"`
	// Runs and Credits are the account's runs in the last 30 days and the
	// credits they used, LastRunAt when the latest ended; all from the run
	// ledger, when it's wired.
	Runs      int        `json:"runs"`
	Credits   int64      `json:"credits"`
	LastRunAt *time.Time `json:"last_run_at,omitempty"`
}

// tenantsList serves GET /v1/ops/tenants: every tenant with its
// status, plan, created-at, and last-activity (max usage hour
// bucket), newest first.
func (h *OpsHandler) tenantsList(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	tenants, err := h.tenants.List(ctx, tenant.ListFilter{})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	// Per-tenant last-activity = max usage hour bucket. One snapshot,
	// indexed by tenant, avoids an N+1 over the usage store.
	counters, err := h.usage.Snapshot(ctx, usage.Filter{})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	lastActivity := map[string]time.Time{}
	for _, c := range counters {
		if c.HourBucket.After(lastActivity[c.TenantID]) {
			lastActivity[c.TenantID] = c.HourBucket
		}
	}

	runs := map[string][]meter.RunDay{}
	if h.runs != nil {
		now := h.now().UTC()
		days, err := h.runs.RunDays(ctx, "", now.Add(-opsRunWindow), now)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		for _, d := range days {
			runs[d.Tenant] = append(runs[d.Tenant], d)
		}
	}

	rows := make([]OpsTenantRow, 0, len(tenants))
	for _, t := range tenants {
		row := opsRow(t)
		row.addRuns(runs[t.ID])
		if la, ok := lastActivity[t.ID]; ok {
			la := la
			row.LastActivityAt = &la
		}
		rows = append(rows, row)
	}
	// Newest tenants first.
	sort.Slice(rows, func(i, j int) bool { return rows[i].CreatedAt.After(rows[j].CreatedAt) })

	writeJSON(w, http.StatusOK, map[string]any{"tenants": rows})
}

// OpsTenantDetail is the body of GET /v1/ops/tenants/{id}.
type OpsTenantDetail struct {
	Tenant    OpsTenantRow            `json:"tenant"`
	Usage     []usage.Counter         `json:"usage"`
	Receipts  []receipt.Receipt       `json:"receipts"`
	Proposals []proposals.DecisionRow `json:"proposals"`
	Posture   []posture.Transition    `json:"posture"`
	Alerts    OpsAlertHealth          `json:"alerts"`
	// Keys are the account's active API keys, by prefix; Agents its runs
	// in the last 30 days per agent. Each nil when its reader isn't wired.
	Keys   []OpsKey       `json:"keys,omitempty"`
	Agents []OpsAgentRuns `json:"agents,omitempty"`
}

// OpsAlertHealth is a small delivery-status rollup over the recent
// proposals shown in the detail view.
type OpsAlertHealth struct {
	Delivered int `json:"delivered"`
	Failed    int `json:"failed"`
	Blocked   int `json:"blocked"`
}

// tenantDetail serves GET /v1/ops/tenants/{id}: one tenant's usage,
// recent receipts/proposals, posture transitions, and alert-delivery
// health. All reads here are tenant-scoped through existing readers;
// optional readers (decisions/posture/alerts) degrade to empty when
// not wired.
func (h *OpsHandler) tenantDetail(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := r.PathValue("id")
	t, err := h.tenants.Get(ctx, id)
	if err != nil {
		if errors.Is(err, tenant.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	detail := OpsTenantDetail{
		Tenant:    opsRow(t),
		Usage:     []usage.Counter{},
		Receipts:  []receipt.Receipt{},
		Proposals: []proposals.DecisionRow{},
		Posture:   []posture.Transition{},
	}

	if counters, err := h.usage.Snapshot(ctx, usage.Filter{TenantID: t.ID}); err == nil {
		detail.Usage = counters
	}
	if h.keys != nil {
		keys, err := h.opsKeys(ctx, t.ID)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		detail.Keys = keys
	}
	if h.runs != nil {
		now := h.now().UTC()
		days, err := h.runs.RunDays(ctx, t.ID, now.Add(-opsRunWindow), now)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		detail.Tenant.addRuns(days)
		detail.Agents = byAgent(days)
	}
	if receipts, err := h.receipts.List(ctx, t.ID, opsDetailLimit); err == nil {
		detail.Receipts = receipts
	}

	var recentProposals []proposals.DecisionRow
	if h.decisions != nil {
		if rows, err := h.decisions.ListDecisions(ctx, t.ID, opsDetailLimit); err == nil {
			recentProposals = rows
			detail.Proposals = rows
		}
	}
	if h.posture != nil {
		if trs, err := h.posture.Transitions(ctx, t.ID); err == nil {
			detail.Posture = trs
		}
	}
	// Alert health: roll up delivery status over the recent proposals.
	// Best-effort; the DeliveryStore is keyed by (tenant, proposal),
	// so we aggregate over the proposals already fetched rather than
	// add a cross-tenant alert read.
	if h.alerts != nil {
		detail.Alerts = h.alertHealth(ctx, t.ID, recentProposals)
	}

	writeJSON(w, http.StatusOK, detail)
}

func (h *OpsHandler) alertHealth(ctx context.Context, tenantID string, rows []proposals.DecisionRow) OpsAlertHealth {
	var health OpsAlertHealth
	for _, row := range rows {
		if row.ProposalID == "" {
			continue
		}
		deliveries, err := h.alerts.ListDeliveries(ctx, tenantID, row.ProposalID)
		if err != nil {
			continue
		}
		for _, d := range deliveries {
			switch d.Status {
			case alert.DeliveryDelivered:
				health.Delivered++
			case alert.DeliveryFailed:
				health.Failed++
			case alert.DeliveryBlocked:
				health.Blocked++
			}
		}
	}
	return health
}
