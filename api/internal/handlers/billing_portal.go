package handlers

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/billing"
	"github.com/kiff/cloud/api/internal/tenant"
)

// BillingPortalHandler serves POST /v1/me/billing-portal: it mints a
// Stripe Customer Portal session for the caller's tenant and returns
// the one-time URL the web tier redirects to. The portal covers card
// updates, invoices, and cancellation — all hosted by Stripe, so KIFF
// never touches card data.
//
// This lives api-side because minting the session needs the Stripe
// secret key, which must never reach the web tier. The
// route is authenticated (mounted under the /v1 auth middleware), and
// the customer id is read from the caller's OWN tenant metadata — a
// caller can only ever open their own portal.
//
// When no portal client is configured (KIFF_STRIPE_SECRET_KEY unset)
// the route is left unregistered, so the endpoint 404s rather than
// pretending billing is wired (mirrors the webhook handler).
type BillingPortalHandler struct {
	tenants    tenant.Store
	portal     *billing.PortalClient
	appBaseURL string
	logger     *slog.Logger
}

// NewBillingPortalHandler wires the tenant store, the portal client,
// and the dashboard origin used to build the return URL. Returns nil
// when portal is nil so the caller leaves the route unmounted.
func NewBillingPortalHandler(tenants tenant.Store, portal *billing.PortalClient, appBaseURL string, logger *slog.Logger) *BillingPortalHandler {
	if tenants == nil || portal == nil {
		return nil
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &BillingPortalHandler{
		tenants:    tenants,
		portal:     portal,
		appBaseURL: strings.TrimRight(appBaseURL, "/"),
		logger:     logger,
	}
}

// Register mounts the route. No-op on a nil handler so an unconfigured
// environment simply has no portal endpoint.
func (h *BillingPortalHandler) Register(mux *http.ServeMux) {
	if h == nil {
		return
	}
	mux.HandleFunc("POST /v1/me/billing-portal", h.create)
}

type billingPortalResponse struct {
	URL string `json:"url"`
}

func (h *BillingPortalHandler) create(w http.ResponseWriter, r *http.Request) {
	id, err := auth.IdentityFrom(r.Context())
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return
	}
	if id.Tenant == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "identity has no tenant"})
		return
	}
	t, err := h.tenants.Get(r.Context(), id.Tenant)
	if err != nil {
		if errors.Is(err, tenant.ErrNotFound) {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "tenant not found"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	customerID := metaString(t, billing.MetaStripeCustomerID)
	if customerID == "" {
		// No Stripe customer linked — the tenant has never had a
		// subscription. The web gates the action on has_billing, so
		// this is defence in depth; 404 keeps it unambiguous.
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no billing account for this tenant"})
		return
	}

	returnURL := h.appBaseURL + "/dashboard/settings"
	url, err := h.portal.CreateSession(r.Context(), customerID, returnURL)
	if err != nil {
		h.logger.ErrorContext(r.Context(), "billing: portal session failed",
			slog.String("tenant", t.ID), slog.String("err", err.Error()))
		// 502: the dependency (Stripe) failed; the client can retry.
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "could not open billing portal"})
		return
	}
	writeJSON(w, http.StatusOK, billingPortalResponse{URL: url})
}
