package handlers

import (
	"net/http"
	"sort"
	"time"

	"github.com/kiff/cloud/api/internal/receipt"
	"github.com/kiff/cloud/api/internal/tenant"
	"github.com/kiff/cloud/api/internal/usage"
)

// opsActivityWindowDays is the look-back the overview sums governed
// activity over.
const opsActivityWindowDays = 7

// OpsOverview is the body of GET /v1/ops/overview — the command
// center: platform stats, recent governed activity, the anchor
// backlog health, and the risk-ranked attention list.
type OpsOverview struct {
	Tenants   OpsTenantStats   `json:"tenants"`
	Activity  OpsActivityStats `json:"activity"`
	Backlog   OpsBacklog       `json:"backlog"`
	Attention []OpsTenantRisk  `json:"attention"`
}

// OpsTenantStats aggregates tenant bookkeeping.
type OpsTenantStats struct {
	Total    int            `json:"total"`
	ByStatus map[string]int `json:"by_status"`
	ByPlan   map[string]int `json:"by_plan"`
	New7d    int            `json:"new_7d"`
	New30d   int            `json:"new_30d"`
}

// OpsActivityStats aggregates governed activity over a recent window.
type OpsActivityStats struct {
	// WindowDays is the look-back the counters were summed over.
	WindowDays int `json:"window_days"`
	// Counters maps cloud counter name → total across all tenants
	// over the window (e.g. kiff.cloud.proposals.blocked).
	Counters map[string]uint64 `json:"counters"`
	// ReceiptsRecent is the count of receipts minted in the window.
	ReceiptsRecent int `json:"receipts_recent"`
}

// overview serves GET /v1/ops/overview: tenant counts by status and
// plan, new-tenant velocity, and governed-activity totals across all
// tenants over the window. All reads are cross-tenant native
// (tenant.List + usage.Snapshot); the receipt count uses the bounded
// cross-tenant feed read.
func (h *OpsHandler) overview(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	now := time.Now().UTC()

	tenants, err := h.tenants.List(ctx, tenant.ListFilter{})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	stats := OpsTenantStats{
		ByStatus: map[string]int{},
		ByPlan:   map[string]int{},
		Total:    len(tenants),
	}
	for _, t := range tenants {
		stats.ByStatus[string(t.Status)]++
		stats.ByPlan[string(tenant.PlanFor(t).Canonical)]++
		age := now.Sub(t.CreatedAt)
		if age <= 7*24*time.Hour {
			stats.New7d++
		}
		if age <= 30*24*time.Hour {
			stats.New30d++
		}
	}

	// Governed-activity totals across all tenants over the window.
	from := usage.HourBucket(now.Add(-opsActivityWindowDays * 24 * time.Hour))
	counters, err := h.usage.Snapshot(ctx, usage.Filter{HourFrom: from})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	totals := map[string]uint64{}
	for _, c := range counters {
		totals[c.CounterName] += c.Count
	}

	// Recent receipts across all tenants (count within the window).
	recentReceipts, err := h.receipts.ListRecentAcrossTenants(ctx, receipt.OpsActivityLimit)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	receiptsRecent := 0
	for _, rc := range recentReceipts {
		if !rc.IssuedAt.Before(from) {
			receiptsRecent++
		}
	}

	// Anchor backlog health (cross-tenant): pending count + oldest age.
	pending, oldest, err := h.receipts.AnchorBacklog(ctx)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	backlog := OpsBacklog{Pending: pending, OldestAgeSeconds: backlogAgeSeconds(oldest, now)}

	// Attention list: per-tenant risk over month-to-date usage. One
	// cross-tenant snapshot, grouped in Go (no N+1, no new table).
	monthFrom := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	monthRows, err := h.usage.Snapshot(ctx, usage.Filter{HourFrom: monthFrom})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	byTenant := groupUsageByTenant(monthRows)
	attention := make([]OpsTenantRisk, 0)
	for _, t := range tenants {
		if risk := scoreTenant(t, byTenant[t.ID]); risk != nil {
			attention = append(attention, *risk)
		}
	}
	// Highest score first; stable by slug for ties.
	sort.Slice(attention, func(i, j int) bool {
		if attention[i].Score != attention[j].Score {
			return attention[i].Score > attention[j].Score
		}
		return attention[i].Slug < attention[j].Slug
	})

	writeJSON(w, http.StatusOK, OpsOverview{
		Tenants: stats,
		Activity: OpsActivityStats{
			WindowDays:     opsActivityWindowDays,
			Counters:       totals,
			ReceiptsRecent: receiptsRecent,
		},
		Backlog:   backlog,
		Attention: attention,
	})
}
