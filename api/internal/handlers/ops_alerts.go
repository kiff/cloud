package handlers

import (
	"net/http"
	"time"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/operatoralerts"
	"github.com/kiff/cloud/api/internal/operatoraudit"
	"github.com/kiff/cloud/api/internal/tenant"
	"github.com/kiff/cloud/api/internal/usage"
)

// Operator alert queue. The queue is populated by
// syncAlerts, which recomputes the command-center risk signals and
// upserts one alert per (tenant, signal kind) — deduped, so re-syncing
// bumps count rather than spamming. Operators then ack/resolve.

// listAlerts serves GET /v1/ops/alerts. ?all=1 includes resolved.
func (h *OpsHandler) listAlerts(w http.ResponseWriter, r *http.Request) {
	openOnly := r.URL.Query().Get("all") == ""
	alerts, err := h.alertQueue.List(r.Context(), openOnly, 200)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"alerts": alerts})
}

// syncAlerts serves POST /v1/ops/alerts/sync: recompute risk signals
// and upsert them into the queue. Returns the resulting open alerts.
// Idempotent — re-running with the same conditions bumps counts.
func (h *OpsHandler) syncAlerts(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	now := h.now().UTC()

	tenants, err := h.tenants.List(ctx, tenant.ListFilter{})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	monthFrom := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	rows, err := h.usage.Snapshot(ctx, usage.Filter{HourFrom: monthFrom})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	byTenant := groupUsageByTenant(rows)

	upserted := 0
	for _, t := range tenants {
		risk := scoreTenant(t, byTenant[t.ID])
		if risk == nil {
			continue
		}
		for _, s := range risk.Signals {
			if _, err := h.alertQueue.Upsert(ctx, operatoralerts.Upsert{
				TenantID: t.ID, TenantSlug: t.Slug, Kind: s.Key, Severity: s.Level, Detail: s.Detail,
			}, now); err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
				return
			}
			upserted++
		}
	}
	alerts, err := h.alertQueue.List(ctx, true, 200)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"synced": upserted, "alerts": alerts})
}

func (h *OpsHandler) ackAlert(w http.ResponseWriter, r *http.Request) {
	h.alertStatus(w, r, operatoralerts.StatusAcked, operatoraudit.ActionAlertAck)
}

func (h *OpsHandler) resolveAlert(w http.ResponseWriter, r *http.Request) {
	h.alertStatus(w, r, operatoralerts.StatusResolved, operatoraudit.ActionAlertResolve)
}

func (h *OpsHandler) alertStatus(w http.ResponseWriter, r *http.Request, status, auditAction string) {
	ctx := r.Context()
	id := r.PathValue("id")
	op := auth.MustIdentity(ctx).Subject
	note := readReason(r) // the "reason" field doubles as the triage note

	// Atomic: the alert transition and its audit row commit together.
	// The mutator fills the audit entry's tenant + kind from the
	// transitioned alert. The audit error is never ignored.
	a, err := h.mutator.AlertStatus(ctx, id, status, op, note, operatoraudit.Entry{
		OperatorSubject: op, Action: auditAction, Reason: note,
	})
	if err != nil {
		if err == operatoralerts.ErrNotFound {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, a)
}
