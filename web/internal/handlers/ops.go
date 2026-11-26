package handlers

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/kiff/cloud/web/internal/apiclient"
	"github.com/kiff/cloud/web/internal/components"
	"github.com/kiff/cloud/web/internal/pages"
	"github.com/kiff/cloud/web/internal/session"
)

// OpsHandler serves the internal operator console at /ops: a
// read-only, cross-tenant observability surface. Every route is
// mounted behind RequireSession + RequireOperator, so by the time a
// handler runs the caller is a verified operator.
//
// The handler holds no data of its own — it forwards the operator's
// bearer to the apps/api /v1/ops/* endpoints (which re-check the
// allow-list) and renders the result. It reuses the dashboard's light
// app shell + content primitives so the console matches the rest of
// kiff.dev rather than being a separate-looking tool.
//
// The console is never linked from the marketing site or the tenant
// dashboard nav; it is reached by typing the path while signed in as
// an operator.
type OpsHandler struct {
	api      *apiclient.Client
	sessions *session.Manager
	bearers  BearerSource
	logger   *slog.Logger
}

// NewOpsHandler wires the session-aware apiclient + bearer source.
// api may be nil; the console then renders an "API not configured"
// notice so the gate can still be smoke-tested without apps/api.
func NewOpsHandler(api *apiclient.Client, sessions *session.Manager, bearers BearerSource, logger *slog.Logger) *OpsHandler {
	if logger == nil {
		logger = slog.Default()
	}
	if bearers == nil {
		bearers = StaticBearerSource()
	}
	return &OpsHandler{api: api, sessions: sessions, bearers: bearers, logger: logger}
}

// MountOps attaches the gated operator-console handler to the outer
// mux under /ops. It is the single source of truth for how the console
// is mounted on the public router — cmd/web calls it, and the routing
// guard test drives it, so the two can't drift.
//
// Both GET and POST under /ops/ are forwarded to the gated handler.
// The console's mutation forms (suspend/unsuspend, plan change, alert
// ack/resolve, support reply/status) are POSTs; mounting only the GET
// patterns — the shape shipped from the ops MVP until — makes Go's
// ServeMux return 405 for every operator POST (the path matches GET
// /ops/ but no pattern matches the POST method), so the mutation forms
// never reach the handler. All three patterns forward to the same
// gated handler, so RequireSession + RequireOperator apply uniformly;
// there is no unauthenticated or ungated path here.
func MountOps(mux *http.ServeMux, gated http.Handler) {
	mux.Handle("GET /ops", gated)
	mux.Handle("GET /ops/", gated)
	mux.Handle("POST /ops/", gated)
}

// Register attaches the operator routes to mux. Callers are expected
// to wrap mux with RequireSession + RequireOperator before mounting.
func (h *OpsHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /ops", h.overview)
	mux.HandleFunc("GET /ops/", h.overview)
	mux.HandleFunc("GET /ops/tenants", h.tenants)
	mux.HandleFunc("GET /ops/tenants/{id}", h.tenantDetail)
	mux.HandleFunc("GET /ops/activity", h.activity)
	mux.HandleFunc("GET /ops/consumption", h.consumption)
	mux.HandleFunc("GET /ops/audit", h.auditLog)
	mux.HandleFunc("GET /ops/alerts", h.alerts)
	mux.HandleFunc("GET /ops/support", h.support)
	mux.HandleFunc("GET /ops/support/{id}", h.supportDetail)
	// Control-plane mutations. Server-rendered POST forms (no JS),
	// redirecting back on success.
	mux.HandleFunc("POST /ops/tenants/{id}/suspend", h.suspend)
	mux.HandleFunc("POST /ops/tenants/{id}/unsuspend", h.unsuspend)
	mux.HandleFunc("POST /ops/tenants/{id}/plan", h.changePlan)
	mux.HandleFunc("POST /ops/tenants/{id}/label", h.setLabel)
	mux.HandleFunc("POST /ops/tenants/{id}/bonus", h.grantBonus)
	mux.HandleFunc("POST /ops/tenants/{id}/store-billing/reset", h.resetStoreBilling)
	mux.HandleFunc("POST /ops/alerts/sync", h.syncAlerts)
	mux.HandleFunc("POST /ops/alerts/{id}/ack", h.ackAlert)
	mux.HandleFunc("POST /ops/alerts/{id}/resolve", h.resolveAlert)
	mux.HandleFunc("POST /ops/support/{id}/reply", h.supportReply)
	mux.HandleFunc("POST /ops/support/{id}/status", h.supportStatus)
	h.registerProofs(mux)
}

// chrome builds the shared app-shell chrome from the verified
// operator session (display name + email + avatar). The workspace
// label is fixed to "operator" so the topbar never reads as the
// operator's own tenant.
func (h *OpsHandler) chrome(r *http.Request) components.DashboardChrome {
	s, _ := SessionFrom(r.Context())
	return components.NewDashboardChrome(s.DisplayName, s.Email, "operator")
}

// bearer resolves the upstream bearer for the verified session.
func (h *OpsHandler) bearer(r *http.Request) (string, bool) {
	s, ok := SessionFrom(r.Context())
	if !ok {
		return "", false
	}
	b, err := h.bearers.BearerFor(r.Context(), s)
	if err != nil {
		return "", false
	}
	return b, true
}

func (h *OpsHandler) overview(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	chrome := h.chrome(r)
	if h.api == nil {
		_ = pages.OpsUnavailable("Overview", chrome).Render(r.Context(), w)
		return
	}
	bearer, ok := h.bearer(r)
	if !ok {
		http.Redirect(w, r, "/sign-in?next=/ops", http.StatusFound)
		return
	}
	overview, err := h.api.OpsOverviewBearer(r.Context(), bearer)
	if err != nil {
		h.renderError(w, r, "Overview", chrome, err)
		return
	}
	_ = pages.OpsOverview(*overview, chrome).Render(r.Context(), w)
}

func (h *OpsHandler) tenants(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	chrome := h.chrome(r)
	if h.api == nil {
		_ = pages.OpsUnavailable("Tenants", chrome).Render(r.Context(), w)
		return
	}
	bearer, ok := h.bearer(r)
	if !ok {
		http.Redirect(w, r, "/sign-in?next=/ops/tenants", http.StatusFound)
		return
	}
	rows, err := h.api.OpsTenantsBearer(r.Context(), bearer)
	if err != nil {
		h.renderError(w, r, "Tenants", chrome, err)
		return
	}
	// Server-side search by slug / id / display name. Read-only,
	// operates on the already-fetched cross-tenant list.
	q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	if q != "" {
		filtered := rows[:0]
		for _, t := range rows {
			if strings.Contains(strings.ToLower(t.Slug), q) ||
				strings.Contains(strings.ToLower(t.ID), q) ||
				strings.Contains(strings.ToLower(t.DisplayName), q) ||
				strings.ToLower(t.Label) == q {
				filtered = append(filtered, t)
			}
		}
		rows = filtered
	}
	_ = pages.OpsTenants(rows, q, chrome).Render(r.Context(), w)
}

func (h *OpsHandler) tenantDetail(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	chrome := h.chrome(r)
	id := r.PathValue("id")
	if h.api == nil {
		_ = pages.OpsUnavailable("Tenant", chrome).Render(r.Context(), w)
		return
	}
	bearer, ok := h.bearer(r)
	if !ok {
		http.Redirect(w, r, "/sign-in?next=/ops/tenants", http.StatusFound)
		return
	}
	detail, err := h.api.OpsTenantDetailBearer(r.Context(), bearer, id)
	if err != nil {
		// A 404 from the API (unknown tenant) surfaces as
		// ErrReceiptNotFound via the shared do() helper.
		if errors.Is(err, apiclient.ErrReceiptNotFound) {
			http.NotFound(w, r)
			return
		}
		h.renderError(w, r, "Tenant", chrome, err)
		return
	}
	// Risk signals for this tenant, reused from the command-center
	// overview (no new backend): the attention list already carries
	// per-tenant signals. Best-effort — a failed lookup just omits the
	// risk card.
	var signals []apiclient.OpsRiskSignal
	if overview, oerr := h.api.OpsOverviewBearer(r.Context(), bearer); oerr == nil {
		for _, a := range overview.Attention {
			if a.ID == id {
				signals = a.Signals
				break
			}
		}
	}
	_ = pages.OpsTenantDetail(*detail, signals, chrome).Render(r.Context(), w)
}

// consumption renders the run ledger's last 30 days by day, account and
// agent.
func (h *OpsHandler) consumption(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	chrome := h.chrome(r)
	if h.api == nil {
		_ = pages.OpsUnavailable("Consumption", chrome).Render(r.Context(), w)
		return
	}
	bearer, ok := h.bearer(r)
	if !ok {
		http.Redirect(w, r, "/sign-in?next=/ops/consumption", http.StatusFound)
		return
	}
	c, err := h.api.OpsConsumptionBearer(r.Context(), bearer)
	if err != nil {
		h.renderError(w, r, "Consumption", chrome, err)
		return
	}
	_ = pages.OpsConsumption(*c, chrome).Render(r.Context(), w)
}

func (h *OpsHandler) activity(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	chrome := h.chrome(r)
	if h.api == nil {
		_ = pages.OpsUnavailable("Activity", chrome).Render(r.Context(), w)
		return
	}
	bearer, ok := h.bearer(r)
	if !ok {
		http.Redirect(w, r, "/sign-in?next=/ops/activity", http.StatusFound)
		return
	}
	rows, err := h.api.OpsActivityBearer(r.Context(), bearer, 0)
	if err != nil {
		h.renderError(w, r, "Activity", chrome, err)
		return
	}
	_ = pages.OpsActivity(rows, chrome).Render(r.Context(), w)
}

// renderError logs the upstream failure and renders the unavailable
// page rather than leaking the error to the operator's browser.
func (h *OpsHandler) renderError(w http.ResponseWriter, r *http.Request, surface string, chrome components.DashboardChrome, err error) {
	h.logger.WarnContext(r.Context(), "ops console: upstream read failed",
		slog.String("surface", surface),
		slog.String("err", err.Error()),
	)
	_ = pages.OpsUnavailable(surface, chrome).Render(r.Context(), w)
}
