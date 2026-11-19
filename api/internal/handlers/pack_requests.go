package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/kiff/cloud/api/internal/alert"
	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/membership"
	"github.com/kiff/cloud/api/internal/meter"
	"github.com/kiff/cloud/api/internal/notify"
	"github.com/kiff/cloud/api/internal/packs"
	"github.com/kiff/cloud/api/internal/personlang"
	"github.com/kiff/cloud/api/internal/tenant"
	"github.com/kiff/cloud/api/internal/words"
)

// PackRequestsHandler lets a member who can't manage billing ask the
// account's admins for a pack (Solo), or to raise the spend limit
// (Company):
//
//	GET  /v1/me/pack-requests                  every open request for someone who manages billing; the caller's own otherwise
//	POST /v1/me/pack-requests                  {"pack": "…", "reason": "…"} or {"kind": "raise", "reason": "…"}: ask; the admins are told
//	POST /v1/me/pack-requests/{id}/withdraw    the member who asked takes it back
//	POST /v1/me/pack-requests/{id}/decline     {"note": "…"}: a billing admin declines; from a phone, signed
//
// The first answer counts. Approving a request for a pack is buying it
// with the request's id: through Stripe on kiff.dev (the checkout carries
// it), or through Apple or Google in the app (sent with the transaction).
// Approving one for a raise is raising the limit, which answers every open
// one. A request expires at the end of the period it was made in.
type PackRequestsHandler struct {
	tenants  tenant.Store
	requests packs.Requests
	catalog  packs.Catalog
	alerts   alert.Notifier // optional: the app's backend, through the account's alert targets
	members  MembersLister  // optional, with emails and mail: the admins' email
	emails   EmailLookup
	mail     notify.MembershipNotifier
	appURL   string
	logger   *slog.Logger
	now      func() time.Time
}

// NewPackRequestsHandler serves requests from requests, for the packs in
// catalog.
func NewPackRequestsHandler(tenants tenant.Store, requests packs.Requests, catalog packs.Catalog, logger *slog.Logger) *PackRequestsHandler {
	if tenants == nil || requests == nil {
		return nil
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &PackRequestsHandler{tenants: tenants, requests: requests, catalog: catalog, logger: logger, now: time.Now}
}

// WithAlerts tells the account's alert targets of each request and answer.
func (h *PackRequestsHandler) WithAlerts(n alert.Notifier) *PackRequestsHandler {
	if h != nil && n != nil {
		h.alerts = n
	}
	return h
}

// WithEmail emails each request to the account's admins: members lists
// them, emails finds their addresses, mail sends.
func (h *PackRequestsHandler) WithEmail(members MembersLister, emails EmailLookup, mail notify.MembershipNotifier, appURL string) *PackRequestsHandler {
	if h != nil {
		h.members, h.emails, h.mail, h.appURL = members, emails, mail, strings.TrimRight(appURL, "/")
	}
	return h
}

// WithClock sets the clock, for tests.
func (h *PackRequestsHandler) WithClock(now func() time.Time) *PackRequestsHandler {
	h.now = now
	return h
}

// Register mounts the routes.
func (h *PackRequestsHandler) Register(mux *http.ServeMux) {
	if h == nil {
		return
	}
	mux.HandleFunc("GET /v1/me/pack-requests", h.list)
	mux.HandleFunc("POST /v1/me/pack-requests", h.ask)
	mux.HandleFunc("POST /v1/me/pack-requests/{id}/withdraw", h.withdraw)
	mux.HandleFunc("POST /v1/me/pack-requests/{id}/decline", h.decline)
}

// person is the caller, a person on an account (never an agent's key).
func (h *PackRequestsHandler) person(w http.ResponseWriter, r *http.Request) (auth.Identity, bool) {
	id, err := auth.IdentityFrom(r.Context())
	if err != nil || strings.TrimSpace(id.Tenant) == "" || strings.TrimSpace(id.Subject) == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "sign in to an account"})
		return id, false
	}
	if id.AgentID != "" {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "requests are people's"})
		return id, false
	}
	return id, true
}

func (h *PackRequestsHandler) list(w http.ResponseWriter, r *http.Request) {
	id, ok := h.person(w, r)
	if !ok {
		return
	}
	now := h.now().UTC()
	var (
		list []packs.Request
		err  error
	)
	if mayAddTasks(id) {
		list, err = h.requests.List(r.Context(), id.Tenant, "", true, now.AddDate(-1, 0, 0), now)
	} else {
		list, err = h.requests.List(r.Context(), id.Tenant, id.Subject, false, now.AddDate(0, -3, 0), now)
	}
	if err != nil {
		h.logger.ErrorContext(r.Context(), "pack requests: list failed", slog.String("tenant", id.Tenant), slog.String("err", err.Error()))
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "requests unavailable"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": list})
}

func (h *PackRequestsHandler) ask(w http.ResponseWriter, r *http.Request) {
	id, ok := h.person(w, r)
	if !ok {
		return
	}
	var body struct {
		Kind   string `json:"kind"`
		Pack   string `json:"pack"`
		Reason string `json:"reason"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": `send {"pack": "<pack>", "reason": "<why, optional>"} or {"kind": "raise", "reason": "<why, optional>"}`})
		return
	}
	kind := strings.TrimSpace(body.Kind)
	if kind == "" {
		kind = packs.AskPack
	}
	if kind != packs.AskPack && kind != packs.AskRaise {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "kind is pack or raise"})
		return
	}
	if mayAddTasks(id) {
		if kind == packs.AskRaise {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "you_can_raise", "message": "You can raise the spend limit yourself."})
			return
		}
		writeJSON(w, http.StatusConflict, map[string]string{"error": "you_can_add_tasks", "message": "You can add a pack yourself."})
		return
	}
	var pack packs.Pack
	if kind == packs.AskPack {
		var ok bool
		// Only a pack on sale: a pack of credits. Packs of tasks are no
		// longer sold, so a request for one could never be answered.
		if pack, ok = h.catalog.Pack(strings.TrimSpace(body.Pack)); !ok || pack.Credits <= 0 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "no such pack"})
			return
		}
	}
	if utf8.RuneCountInString(strings.TrimSpace(body.Reason)) > packs.MaxReason {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "the reason is at most 500 characters"})
		return
	}
	t, err := h.tenants.Get(r.Context(), id.Tenant)
	if err != nil {
		h.logger.ErrorContext(r.Context(), "pack requests: read account failed", slog.String("tenant", id.Tenant), slog.String("err", err.Error()))
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not ask"})
		return
	}
	// A pack is Solo's; a raise is Company's, whose credits beyond the
	// included ones go on the invoice up to the spend limit.
	switch plan, _ := meter.KindOf(t); {
	case kind == packs.AskRaise && plan != meter.BalanceCompany:
		writeJSON(w, http.StatusConflict, map[string]string{"error": "no_spend_limit", "message": "Only a Company plan has a spend limit."})
		return
	case kind == packs.AskPack && plan == meter.BalanceCompany:
		writeJSON(w, http.StatusConflict, map[string]string{"error": "ask_for_a_raise", "message": "On Company, ask an admin to raise the spend limit."})
		return
	case kind == packs.AskPack && plan != meter.BalanceSolo:
		writeJSON(w, http.StatusConflict, map[string]string{"error": "no_packs", "message": "This account's plan has no packs."})
		return
	}
	now := h.now().UTC()
	_, end := tenant.Period(t, now)
	req, err := h.requests.Ask(r.Context(), packs.Request{TenantID: t.ID, AskedBy: id.Subject, Kind: kind, Pack: pack.ID, Reason: body.Reason, CreatedAt: now, ExpiresAt: end})
	switch {
	case errors.Is(err, packs.ErrAlreadyAsked):
		writeJSON(w, http.StatusConflict, map[string]string{"error": "already_asked", "message": "You already asked. Withdraw it to ask again."})
		return
	case err != nil:
		h.logger.ErrorContext(r.Context(), "pack requests: ask failed", slog.String("tenant", t.ID), slog.String("err", err.Error()))
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not ask"})
		return
	}
	h.tell(t, req, alert.PackRequested, now)
	h.emailAdmins(t, req, pack, id.Subject)
	writeJSON(w, http.StatusCreated, req)
}

func (h *PackRequestsHandler) withdraw(w http.ResponseWriter, r *http.Request) {
	id, ok := h.person(w, r)
	if !ok {
		return
	}
	now := h.now().UTC()
	req, err := h.requests.Withdraw(r.Context(), id.Tenant, r.PathValue("id"), id.Subject, now)
	if h.answered(w, r, req, err) {
		h.tell(tenant.Tenant{ID: id.Tenant}, req, alert.PackRequestAnswered, now)
		writeJSON(w, http.StatusOK, req)
	}
}

func (h *PackRequestsHandler) decline(w http.ResponseWriter, r *http.Request) {
	id, ok := h.person(w, r)
	if !ok {
		return
	}
	if !mayAddTasks(id) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "answering a request is the account's billing admin's"})
		return
	}
	var body struct {
		Note string `json:"note"`
	}
	if r.ContentLength != 0 {
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&body); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": `send {"note": "<optional>"}`})
			return
		}
	}
	if utf8.RuneCountInString(strings.TrimSpace(body.Note)) > packs.MaxReason {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "the note is at most 500 characters"})
		return
	}
	now := h.now().UTC()
	req, err := h.requests.Decline(r.Context(), id.Tenant, r.PathValue("id"), id.Subject, body.Note, now)
	if h.answered(w, r, req, err) {
		h.tell(tenant.Tenant{ID: id.Tenant}, req, alert.PackRequestAnswered, now)
		writeJSON(w, http.StatusOK, req)
	}
}

// answered writes the refusal for err and reports whether there was none.
func (h *PackRequestsHandler) answered(w http.ResponseWriter, r *http.Request, req packs.Request, err error) bool {
	switch {
	case err == nil:
		return true
	case errors.Is(err, packs.ErrRequestNotFound):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "request not found"})
	case errors.Is(err, packs.ErrAnswered):
		writeJSON(w, http.StatusConflict, map[string]any{"error": "request_answered", "request": req})
	default:
		h.logger.ErrorContext(r.Context(), "pack requests: answer failed", slog.String("err", err.Error()))
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not answer"})
	}
	return false
}

// tell sends the event to the account's alert targets that list it: the
// app's backend, which tells the admins (asked) or the member (answered).
func (h *PackRequestsHandler) tell(t tenant.Tenant, req packs.Request, outcome string, at time.Time) {
	if h.alerts == nil {
		return
	}
	req.TenantID = t.ID
	packs.Tell(h.alerts, req, outcome, at)
}

// emailAdmins emails the account's admins, in the background: a member's
// request, with the pack and a link to answer it. Best effort.
func (h *PackRequestsHandler) emailAdmins(t tenant.Tenant, req packs.Request, pack packs.Pack, asker string) {
	if h.members == nil || h.emails == nil || h.mail == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		ms, err := h.members.List(ctx, t.ID)
		if err != nil {
			h.logger.WarnContext(ctx, "pack requests: list admins failed", slog.String("tenant", t.ID), slog.String("err", err.Error()))
			return
		}
		who := ""
		if e, err := h.emails.EmailForSubject(ctx, asker); err == nil {
			who = strings.TrimSpace(e)
		}
		// Each admin in their own language: the one their app last showed them.
		compose := func(w *words.Words) (string, string) {
			asker := orWord(who, w.AMember)
			subject, body := w.PackAskSubject, w.PackAskBody(asker, pack.Name)
			if req.Kind == packs.AskRaise {
				subject, body = w.RaiseAskSubject, w.RaiseAskBody(asker)
			}
			if req.Reason != "" {
				body += " " + w.AskReason(req.Reason)
			}
			if req.Kind == packs.AskRaise {
				body += " " + w.RaiseAskHow
			} else {
				body += " " + w.PackAskHow(h.appURL+"/dashboard/settings")
			}
			return subject, body
		}
		for _, m := range ms {
			if !m.Active() || m.Subject == asker || !membership.Can(m.Role, membership.CapBilling) {
				continue
			}
			to, err := h.emails.EmailForSubject(ctx, m.Subject)
			if err != nil || strings.TrimSpace(to) == "" {
				continue
			}
			lang := personlang.Of(t, m.Subject)
			subject, body := compose(words.Of(lang))
			if err := h.mail.AccountNotice(ctx, to, notify.AccountNotice{Subject: subject, Body: body, Lang: lang}); err != nil {
				h.logger.WarnContext(ctx, "pack requests: email failed", slog.String("tenant", t.ID), slog.String("err", err.Error()))
			}
		}
	}()
}
