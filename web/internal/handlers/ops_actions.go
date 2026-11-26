package handlers

import (
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/kiff/cloud/web/internal/pages"
)

// Control-plane mutation handlers for the operator console. Plain
// server-rendered POST forms (no JS): on success they redirect back to
// the tenant detail so the operator sees the new state and the fresh
// audit row. The real enforcement + audit happen in apps/api; these
// just forward the operator's bearer.

func (h *OpsHandler) suspend(w http.ResponseWriter, r *http.Request) {
	h.tenantAction(w, r, "suspend")
}

func (h *OpsHandler) unsuspend(w http.ResponseWriter, r *http.Request) {
	h.tenantAction(w, r, "unsuspend")
}

func (h *OpsHandler) tenantAction(w http.ResponseWriter, r *http.Request, action string) {
	id := r.PathValue("id")
	if h.api == nil {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = pages.OpsUnavailable("Tenant", h.chrome(r)).Render(r.Context(), w)
		return
	}
	bearer, ok := h.bearer(r)
	if !ok {
		http.Redirect(w, r, "/sign-in?next=/ops/tenants", http.StatusFound)
		return
	}
	reason := r.FormValue("reason")
	var err error
	switch action {
	case "suspend":
		err = h.api.OpsSuspendBearer(r.Context(), bearer, id, reason)
	case "unsuspend":
		err = h.api.OpsUnsuspendBearer(r.Context(), bearer, id, reason)
	}
	if err != nil {
		h.logger.WarnContext(r.Context(), "ops console: tenant action failed",
			slog.String("action", action),
			slog.String("tenant", id),
			slog.String("err", err.Error()),
		)
		// Re-render the detail with whatever the API reports; the
		// detail call will surface the current state.
	}
	http.Redirect(w, r, "/ops/tenants/"+id, http.StatusSeeOther)
}

func (h *OpsHandler) auditLog(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	chrome := h.chrome(r)
	if h.api == nil {
		_ = pages.OpsUnavailable("Audit", chrome).Render(r.Context(), w)
		return
	}
	bearer, ok := h.bearer(r)
	if !ok {
		http.Redirect(w, r, "/sign-in?next=/ops/audit", http.StatusFound)
		return
	}
	entries, err := h.api.OpsAuditBearer(r.Context(), bearer, "")
	if err != nil {
		h.renderError(w, r, "Audit", chrome, err)
		return
	}
	_ = pages.OpsAudit(entries, chrome).Render(r.Context(), w)
}

func (h *OpsHandler) changePlan(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if bearer, ok := h.guardMutation(w, r); ok {
		_ = h.api.OpsChangePlanBearer(r.Context(), bearer, id, r.FormValue("plan"), r.FormValue("reason"))
	}
	http.Redirect(w, r, "/ops/tenants/"+id, http.StatusSeeOther)
}

// setLabel sets the operators' label on the tenant (display only). A
// failure is logged; the tenant page shows the label it has either way.
func (h *OpsHandler) setLabel(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if bearer, ok := h.guardMutation(w, r); ok {
		if err := h.api.OpsSetLabelBearer(r.Context(), bearer, id, r.FormValue("label"), r.FormValue("reason")); err != nil {
			h.logger.WarnContext(r.Context(), "ops console: label failed", slog.String("tenant", id), slog.String("err", err.Error()))
		}
	}
	http.Redirect(w, r, "/ops/tenants/"+id, http.StatusSeeOther)
}

// grantBonus gives the tenant bonus credits, which never expire. A failure
// is logged; the tenant page shows what the account holds either way.
func (h *OpsHandler) grantBonus(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if bearer, ok := h.guardMutation(w, r); ok {
		credits, _ := strconv.Atoi(strings.TrimSpace(r.FormValue("credits")))
		if err := h.api.OpsGrantBonusBearer(r.Context(), bearer, id, credits, r.FormValue("reason")); err != nil {
			h.logger.WarnContext(r.Context(), "ops console: bonus credits failed", slog.String("tenant", id), slog.String("err", err.Error()))
		}
	}
	http.Redirect(w, r, "/ops/tenants/"+id, http.StatusSeeOther)
}

func (h *OpsHandler) syncAlerts(w http.ResponseWriter, r *http.Request) {
	if bearer, ok := h.guardMutation(w, r); ok {
		if _, err := h.api.OpsSyncAlertsBearer(r.Context(), bearer); err != nil {
			h.logger.WarnContext(r.Context(), "ops console: alert sync failed", slog.String("err", err.Error()))
		}
	}
	http.Redirect(w, r, "/ops/alerts", http.StatusSeeOther)
}

func (h *OpsHandler) ackAlert(w http.ResponseWriter, r *http.Request) { h.alertAction(w, r, "ack") }
func (h *OpsHandler) resolveAlert(w http.ResponseWriter, r *http.Request) {
	h.alertAction(w, r, "resolve")
}

func (h *OpsHandler) alertAction(w http.ResponseWriter, r *http.Request, action string) {
	if bearer, ok := h.guardMutation(w, r); ok {
		id := r.PathValue("id")
		if err := h.api.OpsAlertActionBearer(r.Context(), bearer, id, action, r.FormValue("reason")); err != nil {
			h.logger.WarnContext(r.Context(), "ops console: alert action failed",
				slog.String("action", action), slog.String("err", err.Error()))
		}
	}
	http.Redirect(w, r, "/ops/alerts", http.StatusSeeOther)
}

func (h *OpsHandler) alerts(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	chrome := h.chrome(r)
	if h.api == nil {
		_ = pages.OpsUnavailable("Alerts", chrome).Render(r.Context(), w)
		return
	}
	bearer, ok := h.bearer(r)
	if !ok {
		http.Redirect(w, r, "/sign-in?next=/ops/alerts", http.StatusFound)
		return
	}
	list, err := h.api.OpsAlertsBearer(r.Context(), bearer, false)
	if err != nil {
		h.renderError(w, r, "Alerts", chrome, err)
		return
	}
	_ = pages.OpsAlerts(list, chrome).Render(r.Context(), w)
}

// resetStoreBilling puts a review or test account back to no plan after a
// store purchase (App Review's account after a TestFlight Solo), then shows
// the account read back from the API, or why nothing changed.
func (h *OpsHandler) resetStoreBilling(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	bearer, ok := h.guardMutation(w, r)
	if !ok {
		if h.api == nil {
			http.Redirect(w, r, "/ops/tenants/"+id, http.StatusSeeOther)
		}
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	v, err := h.api.OpsResetStoreBillingBearer(r.Context(), bearer, id, strings.TrimSpace(r.FormValue("confirm")), r.FormValue("reason"))
	if err != nil {
		h.logger.WarnContext(r.Context(), "ops console: store billing reset failed", slog.String("tenant", id), slog.String("err", err.Error()))
		_ = pages.OpsStoreResetResult(id, nil, err.Error(), h.chrome(r)).Render(r.Context(), w)
		return
	}
	_ = pages.OpsStoreResetResult(id, v, "", h.chrome(r)).Render(r.Context(), w)
}

// guardMutation resolves the bearer for a POST mutation; on a missing
// api client or session it writes the appropriate response and returns
// ok=false so the caller redirects.
func (h *OpsHandler) guardMutation(w http.ResponseWriter, r *http.Request) (string, bool) {
	if h.api == nil {
		return "", false
	}
	bearer, ok := h.bearer(r)
	if !ok {
		http.Redirect(w, r, "/sign-in?next=/ops", http.StatusFound)
		return "", false
	}
	return bearer, true
}
