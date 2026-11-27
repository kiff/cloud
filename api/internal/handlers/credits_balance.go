package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/kiff/cloud/api/internal/alert"
	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/meter"
	"github.com/kiff/cloud/api/internal/packs"
	"github.com/kiff/cloud/api/internal/tenant"
)

// CreditPacksRead is what the balance reads of the packs of credits:
// those drawn in a period, and those left. *packs.Postgres is one.
type CreditPacksRead interface {
	Credits(ctx context.Context, tenantID string, from, to time.Time) (drawn, left int64, err error)
}

// WithCreditPacks reads Solo's packs of credits from c.
func (h *CreditsHandler) WithCreditPacks(c CreditPacksRead) *CreditsHandler { h.packs = c; return h }

// WithBought reads which packs an account has bought from b, so a pack
// sold once per account is offered only until then.
func (h *CreditsHandler) WithBought(b packs.BoughtRead) *CreditsHandler { h.bought = b; return h }

// onceOffered reports whether the table has a pack sold once per account
// that t hasn't bought. With no read of what it bought, none is offered:
// a pack that can't be told bought is never offered twice.
func (h *CreditsHandler) onceOffered(ctx context.Context, t tenant.Tenant) (bool, error) {
	if h.catalog == nil || h.bought == nil || len(h.catalog.OncePacks()) == 0 {
		return false, nil
	}
	got, err := h.bought.BoughtPacks(ctx, t.ID)
	if err != nil {
		return false, err
	}
	for _, p := range h.catalog.OncePacks() {
		if !got[p.ID] {
			return true, nil
		}
	}
	return false, nil
}

// WithCatalog offers what catalog sells: Solo, and the packs of credits.
func (h *CreditsHandler) WithCatalog(c packs.Catalog) *CreditsHandler { h.catalog = &c; return h }

// WithRequests answers the open requests for a raise when someone raises
// the spend limit, and tells the people who asked through alerts.
func (h *CreditsHandler) WithRequests(r packs.Requests, alerts alert.Notifier) *CreditsHandler {
	h.requests, h.alerts = r, alerts
	return h
}

// buyView is what the app may offer the caller in the store: Solo on an
// account with no plan, packs of credits while Solo runs.
type buyView struct {
	Solo  bool `json:"solo"`
	Packs bool `json:"packs"`
	// Once: a pack sold once per account (the First month pack) is still
	// on offer to this account, beside Solo before a plan and while Solo
	// runs. It is bought once Solo runs, like any pack.
	Once bool `json:"once"`
}

// balanceView is the account's balance now, for the reader: its figures,
// the current period, and what the reader may do about it.
type balanceView struct {
	meter.Balance
	// Period is the current period, whichever the read is of.
	Period usagePeriod `json:"period"`
	// CanRaise: the reader may raise the spend limit (Company, billing).
	// RaiseToCents is what one raise would set, 0 at the ceiling.
	CanRaise     bool    `json:"can_raise"`
	RaiseToCents int64   `json:"raise_to_cents"`
	CanBuy       buyView `json:"can_buy"`
	// BilledBy is who bills the plan: stripe, apple, google, contract, or
	// "" for none.
	BilledBy string `json:"billed_by"`
}

// maxLimitCents is as high as a raise from the phone takes the limit.
const maxLimitCents = meter.SpendLimitStepCents * (1 + meter.MaxSpendLimitSteps)

// limitOf is t's spend limit, the default when none is stored.
func limitOf(t tenant.Tenant) int64 {
	if c := tenant.SpendLimitCents(t); c > 0 {
		return c
	}
	return meter.SpendLimitStepCents
}

// balanceOf is t's balance now, and the current period. cur, when set, is
// the current period's totals, already read.
func (h *CreditsHandler) balanceOf(ctx context.Context, t tenant.Tenant, cur *meter.RunTotals) (meter.Balance, time.Time, time.Time, error) {
	now := h.now().UTC()
	kind, seats := meter.KindOf(t)
	from, to := tenant.Period(t, now)
	in := meter.BalanceIn{Kind: kind, Seats: seats, LimitCents: limitOf(t)}
	if kind != meter.BalanceNone {
		if cur == nil {
			tot, err := h.ledger.RunTotals(ctx, t.ID, from, to)
			if err != nil {
				return meter.Balance{}, from, to, err
			}
			cur = &tot
		}
		in.Used, in.Over = cur.Credits, cur.Over
	}
	if kind == meter.BalanceSolo && h.packs != nil {
		var err error
		if in.PacksDrawn, in.PacksLeft, err = h.packs.Credits(ctx, t.ID, from, to); err != nil {
			return meter.Balance{}, from, to, err
		}
	}
	return meter.ComputeBalance(in), from, to, nil
}

// view is b as id reads it.
func (h *CreditsHandler) view(id auth.Identity, t tenant.Tenant, b meter.Balance, from, to time.Time, once bool) balanceView {
	v := balanceView{Balance: b, Period: usagePeriod{Start: from.UTC(), End: to.UTC()}, BilledBy: string(tenant.BillingSourceOf(t))}
	billing := mayAddTasks(id)
	if billing && b.Kind == meter.BalanceCompany {
		v.CanRaise = true
		if next := limitOf(t) + meter.SpendLimitStepCents; next <= maxLimitCents {
			v.RaiseToCents = next
		}
	}
	if billing && h.catalog != nil {
		v.CanBuy.Solo = b.Kind == meter.BalanceNone && (h.catalog.Solo.AppleProduct != "" || h.catalog.Solo.GoogleProduct != "")
		v.CanBuy.Packs = b.Kind == meter.BalanceSolo && len(h.catalog.CreditPacks()) > 0
		v.CanBuy.Once = once && (v.CanBuy.Solo || b.Kind == meter.BalanceSolo)
	}
	return v
}

// allowance answers whether a new run of the key's agent may start: its
// runtime asks before each one. A taf's waits with the balance; Setup's
// (Coach's) doesn't while a plan runs and it's within its fair use, and on
// an account with no plan it may start within the no-plan allowance, which
// KIFF pays for. It also says the plan ("ai", meter.AIOf), which the runtime
// sizes the account's share of the AI provider's day by.
func (h *CreditsHandler) allowance(w http.ResponseWriter, r *http.Request) {
	id, err := auth.IdentityFrom(r.Context())
	if err != nil || strings.TrimSpace(id.Tenant) == "" || !id.APIKey || id.AgentID == "" {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "an agent's key asks for its own runs"})
		return
	}
	t, ok := h.account(w, r)
	if !ok {
		return
	}
	setup := id.HasRole(auth.RoleSetupReader)
	if !setup {
		tafs, err := h.ledger.Tafs(r.Context(), t.ID)
		if err != nil {
			h.logger.ErrorContext(r.Context(), "credits: tafs failed", slog.String("tenant", t.ID), slog.String("err", err.Error()))
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "credits unavailable"})
			return
		}
		if !slices.Contains(tafs, id.AgentID) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "only a taf's or Coach's runs use credits"})
			return
		}
	}
	b, from, to, err := h.balanceOf(r.Context(), t, nil)
	var setupSpent, noPlanSpent int64
	switch {
	case err != nil:
	case setup && b.Kind == meter.BalanceNone:
		// Counted from when the allowance last started: an account's first day,
		// or an operator's Reset store billing (tenant.NoPlanAllowanceFrom).
		since, _ := tenant.NoPlanAllowanceFrom(t)
		noPlanSpent, err = h.ledger.NoPlanSetupSpent(r.Context(), t.ID, since)
	case setup:
		setupSpent, err = h.ledger.SetupSpent(r.Context(), t.ID, from, to)
	}
	if err != nil {
		h.logger.ErrorContext(r.Context(), "credits: allowance failed", slog.String("tenant", t.ID), slog.String("err", err.Error()))
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "credits unavailable"})
		return
	}
	allowed, why := meter.Allow(b, setup, setupSpent, noPlanSpent)
	// The plan, for the agents runtime's share of the AI provider's day
	// (meter.AIOf): a bound against a runaway, never the gate.
	out := map[string]any{"allowed": allowed, "ai": meter.AIOf(b)}
	if why != "" {
		out["why"] = why
	}
	writeJSON(w, http.StatusOK, out)
}

// raise raises the spend limit one step, from the limit the caller saw:
// someone who manages billing, signed on the phone (PhoneSignedRules). A
// raise from a limit that changed meanwhile is refused, so two at once add
// one step. It answers every open request for a raise.
func (h *CreditsHandler) raise(w http.ResponseWriter, r *http.Request) {
	id, err := auth.IdentityFrom(r.Context())
	if err != nil || strings.TrimSpace(id.Tenant) == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "sign in to an account"})
		return
	}
	if !mayAddTasks(id) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "raising the spend limit is the account's billing admin's"})
		return
	}
	var body struct {
		Raise     bool  `json:"raise"`
		FromCents int64 `json:"from_cents"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&body); err != nil || !body.Raise || body.FromCents <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": `send {"raise": true, "from_cents": <the limit it raises from>}`})
		return
	}
	t, ok := h.account(w, r)
	if !ok {
		return
	}
	if kind, _ := meter.KindOf(t); kind != meter.BalanceCompany {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "no_spend_limit", "message": "Only a Company plan has a spend limit."})
		return
	}
	cur := limitOf(t)
	if body.FromCents != cur {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "limit_changed", "message": "The spend limit changed meanwhile. Read it again.", "limit_cents": cur})
		return
	}
	next := cur + meter.SpendLimitStepCents
	if next > maxLimitCents {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "limit_ceiling", "message": "The spend limit can't go higher from the app. Contact KIFF to raise it further.", "limit_cents": cur})
		return
	}
	switch err := h.tenants.RaiseSpendLimit(r.Context(), t.ID, cur, next, meter.SpendLimitStepCents); {
	case errors.Is(err, tenant.ErrLimitChanged):
		writeJSON(w, http.StatusConflict, map[string]any{"error": "limit_changed", "message": "The spend limit changed meanwhile. Read it again."})
		return
	case err != nil:
		h.logger.ErrorContext(r.Context(), "credits: raise failed", slog.String("tenant", t.ID), slog.String("err", err.Error()))
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not raise it"})
		return
	}
	h.logger.InfoContext(r.Context(), "credits: spend limit raised", slog.String("tenant", t.ID), slog.String("by", id.Subject),
		slog.Int64("from_cents", cur), slog.Int64("to_cents", next))
	if h.requests != nil {
		now := h.now().UTC()
		answered, err := h.requests.ApproveRaises(r.Context(), t.ID, id.Subject, now)
		if err != nil {
			// The raise stands; the requests stay open until the period ends.
			h.logger.WarnContext(r.Context(), "credits: answering raise requests failed", slog.String("tenant", t.ID), slog.String("err", err.Error()))
		}
		for _, req := range answered {
			req.TenantID = t.ID
			packs.Answered(h.alerts, req, now)
		}
	}
	writeJSON(w, http.StatusOK, map[string]int64{"limit_cents": next})
}
