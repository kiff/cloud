package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/kiff/cloud/api/internal/appstore"
	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/billing"
	"github.com/kiff/cloud/api/internal/membership"
	"github.com/kiff/cloud/api/internal/packs"
	"github.com/kiff/cloud/api/internal/tenant"
)

// CheckoutOpener opens a Stripe Checkout session; *billing.Stripe is one.
type CheckoutOpener interface {
	CreateCheckout(ctx context.Context, c billing.Checkout) (string, error)
	CreatePackCheckout(ctx context.Context, c billing.PackCheckout) (string, error)
}

// MembersLister lists an account's live members; the membership store is one.
type MembersLister interface {
	List(ctx context.Context, tenantID string) ([]membership.Member, error)
}

// BillingCheckoutHandler opens the v1 checkout on the web:
//
//	POST /v1/me/billing/checkout {"plan": "team", "seats": n}                  → {"url": …}
//	POST /v1/me/billing/checkout {"pack": "rayo", "request_id": "pr_…"}       → {"url": …}
//
// No plan has a free trial: a plan is billed from its first day, with a
// card taken at checkout. An account moving from the App Store's Solo to
// Team keeps the Solo days it paid for as Team days (TrialEnd with
// AppleCredit). Team's
// seats start at the account's admins and editors, at least 2. An account
// that already has a subscription changes it in the billing portal
// instead, and one Apple bills is never sold a plan here. A pack is a
// one-time payment, whoever bills the plan; with a member's request, it
// answers it. Stripe's checkout is on the web: no phone reaches this
// route.
type BillingCheckoutHandler struct {
	tenants    tenant.Store
	stripe     CheckoutOpener
	members    MembersLister
	catalog    packs.Catalog
	requests   packs.Requests
	appBaseURL string
	logger     *slog.Logger
	now        func() time.Time
}

// WithPacks sells the packs in catalog, answering requests.
func (h *BillingCheckoutHandler) WithPacks(catalog packs.Catalog, requests packs.Requests) *BillingCheckoutHandler {
	if h != nil {
		h.catalog, h.requests = catalog, requests
	}
	return h
}

// NewBillingCheckoutHandler returns nil when stripe isn't configured,
// leaving the route unmounted.
func NewBillingCheckoutHandler(tenants tenant.Store, stripe CheckoutOpener, members MembersLister, appBaseURL string, logger *slog.Logger) *BillingCheckoutHandler {
	if tenants == nil || stripe == nil {
		return nil
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &BillingCheckoutHandler{tenants: tenants, stripe: stripe, members: members,
		appBaseURL: strings.TrimRight(appBaseURL, "/"), logger: logger, now: time.Now}
}

// WithClock sets the clock, for tests.
func (h *BillingCheckoutHandler) WithClock(now func() time.Time) *BillingCheckoutHandler {
	h.now = now
	return h
}

// Register mounts the route.
func (h *BillingCheckoutHandler) Register(mux *http.ServeMux) {
	if h == nil {
		return
	}
	mux.HandleFunc("POST /v1/me/billing/checkout", h.checkout)
}

func (h *BillingCheckoutHandler) checkout(w http.ResponseWriter, r *http.Request) {
	id, err := auth.IdentityFrom(r.Context())
	if err != nil || id.Tenant == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "sign in to an account"})
		return
	}
	if id.AgentID != "" || (id.MemberRole != "" && !membership.Can(id.MemberRole, membership.CapBilling)) || (id.MemberRole == "" && !id.ManagesAccount()) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "choosing the plan is the account's billing admin's"})
		return
	}
	var body struct {
		Plan      string `json:"plan"`
		Yearly    bool   `json:"yearly"`
		Seats     int    `json:"seats"`
		Pack      string `json:"pack"`
		RequestID string `json:"request_id"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&body); err == nil && body.Pack != "" && body.Plan == "" {
		h.packCheckout(w, r, id, body.Pack, strings.TrimSpace(body.RequestID))
		return
	} else if err == nil && body.Plan == "solo" {
		// Solo is sold only in the app stores (pricing phase 3).
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "solo_in_the_app",
			"message": "Solo is sold in the tafs app, through the App Store or Google Play."})
		return
	} else if err != nil || body.Plan != "team" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": `send {"plan": "team", "seats": 1} or {"pack": "<pack>"}`})
		return
	}
	if body.Yearly {
		// v1 sells monthly only.
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "plans are monthly"})
		return
	}
	t, err := h.tenants.Get(r.Context(), id.Tenant)
	if err != nil {
		h.logger.ErrorContext(r.Context(), "billing: checkout: read account failed", slog.String("tenant", id.Tenant), slog.String("err", err.Error()))
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "checkout unavailable"})
		return
	}
	l := tenant.PlanFor(t)
	if l.Canonical == tenant.PlanInternal || l.Canonical == tenant.PlanCompany {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "this account's plan is arranged with KIFF"})
		return
	}
	if tenant.BillingSourceOf(t) == tenant.BillingGoogle {
		// One billing source per account: Google Play's Solo is managed
		// in Google Play (no move to Team here yet).
		writeJSON(w, http.StatusConflict, map[string]string{"error": "billed_by_google",
			"message": "Google Play bills this account's plan. Cancel it in Google Play first to choose a plan here."})
		return
	}
	apple := tenant.BillingSourceOf(t) == tenant.BillingApple
	if apple && (body.Plan != "team" || l.Canonical != tenant.PlanSolo) {
		// One billing source per account: Solo stays Apple's, and only a paid
		// Solo moves to Team here. A store free trial (none is offered on Solo
		// today) would become paid Solo beside Team.
		writeJSON(w, http.StatusConflict, map[string]string{"error": "billed_by_apple",
			"message": "Apple bills this account's plan. Cancel it with Apple first to choose a plan here."})
		return
	}
	if !apple {
		switch status, _ := t.Metadata[billing.MetaBillingStatus].(string); status {
		case billing.StatusActive, billing.StatusPastDue, "trialing":
			writeJSON(w, http.StatusConflict, map[string]string{"error": "already_subscribed", "message": "Change the plan in the billing portal."})
			return
		}
	}
	c := billing.Checkout{TenantID: t.ID, Plan: body.Plan, Seats: body.Seats,
		SuccessURL: h.appBaseURL + "/dashboard/settings?billing=done", CancelURL: h.appBaseURL + "/dashboard/settings"}
	c.CustomerID, _ = t.Metadata[billing.MetaStripeCustomerID].(string)
	now := h.now().UTC()
	if apple {
		// Solo → Team (the shared-tafs design, the owner's option a): Team starts now,
		// and the Solo days Apple was paid for are Team days, unbilled. The
		// person cancels Solo with Apple: KIFF can't, and says how.
		if until, ok := appstore.PaidSoloUntil(t); ok && until.After(now.Add(time.Hour)) {
			c.TrialEnd, c.AppleCredit = until, true
		}
	}
	// No free trial: a plan is billed from its first day.
	if body.Plan == "team" {
		c.Seats = max(c.Seats, h.peopleWhoRunThings(r.Context(), t.ID), tenant.TeamMinSeats)
	}
	url, err := h.stripe.CreateCheckout(r.Context(), c)
	if err != nil {
		h.logger.ErrorContext(r.Context(), "billing: checkout failed", slog.String("tenant", t.ID), slog.String("err", err.Error()))
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "could not open checkout"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"url": url})
}

// peopleWhoRunThings counts the account's admins and editors; 0 when it
// can't tell.
func (h *BillingCheckoutHandler) peopleWhoRunThings(ctx context.Context, tenantID string) int {
	if h.members == nil {
		return 0
	}
	ms, err := h.members.List(ctx, tenantID)
	if err != nil {
		return 0
	}
	n := 0
	for _, m := range ms {
		if paysForSeat(m.Role) {
			n++
		}
	}
	return n
}

// packCheckout opens a pack's one-time payment. An account on a plan with
// no task limit (company, internal) has nothing to add. With a request,
// the request must be open on the account. Only a pack with a Stripe price
// is sold here: a pack of credits is Solo's, bought in the app, and packs
// of tasks are no longer sold.
func (h *BillingCheckoutHandler) packCheckout(w http.ResponseWriter, r *http.Request, id auth.Identity, packID, requestID string) {
	pack, ok := h.catalog.Pack(packID)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "no such pack"})
		return
	}
	switch {
	case pack.Credits > 0:
		writeJSON(w, http.StatusConflict, map[string]string{"error": "credit_packs_in_the_app"})
		return
	case pack.StripePrice == "":
		writeJSON(w, http.StatusConflict, map[string]string{"error": "pack_not_sold"})
		return
	}
	t, err := h.tenants.Get(r.Context(), id.Tenant)
	if err != nil {
		h.logger.ErrorContext(r.Context(), "billing: pack checkout: read account failed", slog.String("tenant", id.Tenant), slog.String("err", err.Error()))
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "checkout unavailable"})
		return
	}
	if tenant.PlanFor(t).IncludedTasks == 0 {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "this account's tasks have no limit"})
		return
	}
	if requestID != "" {
		if h.requests == nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "request not found"})
			return
		}
		req, err := h.requests.Get(r.Context(), t.ID, requestID, h.now())
		switch {
		case errors.Is(err, packs.ErrRequestNotFound):
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "request not found"})
			return
		case err != nil:
			h.logger.ErrorContext(r.Context(), "billing: pack checkout: read request failed", slog.String("tenant", t.ID), slog.String("err", err.Error()))
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "checkout unavailable"})
			return
		case req.Status != packs.RequestOpen:
			writeJSON(w, http.StatusConflict, map[string]any{"error": "request_answered", "request": req})
			return
		}
	}
	c := billing.PackCheckout{TenantID: t.ID, Pack: pack.ID, PriceKey: pack.StripePrice, RequestID: requestID, AnsweredBy: id.Subject,
		SuccessURL: h.appBaseURL + "/dashboard/settings?pack=done", CancelURL: h.appBaseURL + "/dashboard/settings"}
	c.CustomerID, _ = t.Metadata[billing.MetaStripeCustomerID].(string)
	url, err := h.stripe.CreatePackCheckout(r.Context(), c)
	if err != nil {
		h.logger.ErrorContext(r.Context(), "billing: pack checkout failed", slog.String("tenant", t.ID), slog.String("err", err.Error()))
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "could not open checkout"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"url": url})
}
