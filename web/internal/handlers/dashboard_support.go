package handlers

import (
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/kiff/cloud/web/internal/apiclient"
	"github.com/kiff/cloud/web/internal/pages"
	"github.com/kiff/cloud/web/internal/session"
)

// Support section: the tenant's private support tickets. A signed-in
// user lists their tickets, opens one to read the thread, files a
// new ticket, and replies. All server-rendered (templ + form POST +
// redirect). The operator side lives under /ops.

// supportTickets renders GET /dashboard/support — the ticket list.
func (h *DashboardHandler) supportTickets(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	s, ok := SessionFrom(r.Context())
	if !ok {
		http.Redirect(w, r, "/sign-in", http.StatusFound)
		return
	}
	flash, _ := h.sessions.ReadFlash(w, r)
	view := pages.DashboardSupportView{
		Email:       s.Email,
		Subject:     s.Subject,
		DisplayName: s.DisplayName,
		Flash:       convertFlash(flash),
	}
	if h.api == nil {
		view.NotConfiguredMsg = "apps/api is not configured. Set KIFF_API_BASE_URL on the web binary to use support."
		_ = pages.DashboardSupport(view).Render(r.Context(), w)
		return
	}

	bearer, me, ok := h.supportResolveMe(w, r, s)
	if !ok {
		return
	}
	view.Me = me

	tickets, err := h.api.ListSupportTicketsBearer(r.Context(), bearer)
	if err != nil {
		if errors.Is(err, apiclient.ErrUnauthorized) {
			h.bounceToSignIn(w, r)
			return
		}
		view.ErrMsg = "could not load your tickets: " + err.Error()
		_ = pages.DashboardSupport(view).Render(r.Context(), w)
		return
	}
	view.Tickets = tickets
	_ = pages.DashboardSupport(view).Render(r.Context(), w)
}

// newSupportTicket renders GET /dashboard/support/new — the file form.
func (h *DashboardHandler) newSupportTicket(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	s, ok := SessionFrom(r.Context())
	if !ok {
		http.Redirect(w, r, "/sign-in", http.StatusFound)
		return
	}
	// A link can open the form already filled in, e.g. "Ask for guided
	// setup" from a card; the person still reads and sends it.
	q := r.URL.Query()
	view := pages.DashboardNewSupportView{
		Email:       s.Email,
		Subject:     s.Subject,
		DisplayName: s.DisplayName,
		Title:       truncate(strings.TrimSpace(q.Get("title")), 200),
		Category:    strings.TrimSpace(q.Get("category")),
		Body:        truncate(q.Get("body"), 4000),
	}
	if h.api == nil {
		view.NotConfiguredMsg = "apps/api is not configured. Set KIFF_API_BASE_URL on the web binary to file tickets."
		_ = pages.DashboardNewSupportTicket(view).Render(r.Context(), w)
		return
	}
	_, me, ok := h.supportResolveMe(w, r, s)
	if !ok {
		return
	}
	view.Me = me
	_ = pages.DashboardNewSupportTicket(view).Render(r.Context(), w)
}

// createSupportTicket handles POST /dashboard/support — file a ticket.
// On success it flashes and redirects to the new ticket's page; a
// validation error re-renders the form with the draft preserved.
func (h *DashboardHandler) createSupportTicket(w http.ResponseWriter, r *http.Request) {
	s, ok := SessionFrom(r.Context())
	if !ok {
		http.Redirect(w, r, "/sign-in", http.StatusFound)
		return
	}
	if h.api == nil {
		_ = h.sessions.IssueFlash(w, session.Flash{Kind: session.FlashError, Message: "API not configured"})
		http.Redirect(w, r, "/dashboard/support/new", http.StatusSeeOther)
		return
	}

	title := strings.TrimSpace(r.PostFormValue("title"))
	category := strings.TrimSpace(r.PostFormValue("category"))
	body := strings.TrimSpace(r.PostFormValue("body"))

	bearer, err := h.bearer(r.Context(), s)
	if err != nil {
		h.bounceToSignIn(w, r)
		return
	}

	rerender := func(errMsg string) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		view := pages.DashboardNewSupportView{
			Email:       s.Email,
			Subject:     s.Subject,
			DisplayName: s.DisplayName,
			Title:       title,
			Category:    category,
			Body:        body,
			ErrMsg:      errMsg,
		}
		if me, merr := h.api.GetMeBearer(r.Context(), bearer); merr == nil {
			view.Me = me
		}
		_ = pages.DashboardNewSupportTicket(view).Render(r.Context(), w)
	}

	if title == "" || body == "" {
		rerender("Add a subject and some details so we can help.")
		return
	}

	created, err := h.api.CreateSupportTicketBearer(r.Context(), bearer, apiclient.CreateSupportTicketRequest{
		Title: title, Body: body, Category: category,
	})
	if err != nil {
		if errors.Is(err, apiclient.ErrUnauthorized) {
			h.bounceToSignIn(w, r)
			return
		}
		h.logger.WarnContext(r.Context(), "dashboard: create support ticket failed", slog.String("err", err.Error()))
		rerender("Could not file the ticket: " + err.Error())
		return
	}

	_ = h.sessions.IssueFlash(w, session.Flash{
		Kind:    session.FlashSupportCreated,
		Message: "Ticket filed. We'll email you when there's a reply.",
	})
	http.Redirect(w, r, "/dashboard/support/"+url.PathEscape(created.ID), http.StatusSeeOther)
}

// supportTicket renders GET /dashboard/support/{id} — one ticket and
// its thread.
func (h *DashboardHandler) supportTicket(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	s, ok := SessionFrom(r.Context())
	if !ok {
		http.Redirect(w, r, "/sign-in", http.StatusFound)
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	flash, _ := h.sessions.ReadFlash(w, r)
	view := pages.DashboardSupportDetailView{
		Email:       s.Email,
		Subject:     s.Subject,
		DisplayName: s.DisplayName,
		Flash:       convertFlash(flash),
	}
	if h.api == nil {
		view.NotConfiguredMsg = "apps/api is not configured. Set KIFF_API_BASE_URL on the web binary to use support."
		_ = pages.DashboardSupportDetail(view).Render(r.Context(), w)
		return
	}

	bearer, me, ok := h.supportResolveMe(w, r, s)
	if !ok {
		return
	}
	view.Me = me

	detail, err := h.api.GetSupportTicketBearer(r.Context(), bearer, id)
	switch {
	case err == nil:
		view.Ticket = detail
	case errors.Is(err, apiclient.ErrUnauthorized):
		h.bounceToSignIn(w, r)
		return
	case errors.Is(err, apiclient.ErrTicketNotFound):
		_ = h.sessions.IssueFlash(w, session.Flash{Kind: session.FlashError, Message: "That ticket no longer exists."})
		http.Redirect(w, r, "/dashboard/support", http.StatusSeeOther)
		return
	default:
		view.ErrMsg = "could not load this ticket: " + err.Error()
	}
	_ = pages.DashboardSupportDetail(view).Render(r.Context(), w)
}

// replySupportTicket handles POST /dashboard/support/{id}/reply. On
// success it flashes and redirects back to the ticket.
func (h *DashboardHandler) replySupportTicket(w http.ResponseWriter, r *http.Request) {
	s, ok := SessionFrom(r.Context())
	if !ok {
		http.Redirect(w, r, "/sign-in", http.StatusFound)
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	detailPath := "/dashboard/support/" + url.PathEscape(id)

	if h.api == nil {
		_ = h.sessions.IssueFlash(w, session.Flash{Kind: session.FlashError, Message: "API not configured"})
		http.Redirect(w, r, detailPath, http.StatusSeeOther)
		return
	}

	body := strings.TrimSpace(r.PostFormValue("body"))
	bearer, err := h.bearer(r.Context(), s)
	if err != nil {
		h.bounceToSignIn(w, r)
		return
	}

	if body == "" {
		_ = h.sessions.IssueFlash(w, session.Flash{Kind: session.FlashError, Message: "Type a reply before sending."})
		http.Redirect(w, r, detailPath, http.StatusSeeOther)
		return
	}

	if err := h.api.ReplySupportTicketBearer(r.Context(), bearer, id, body); err != nil {
		switch {
		case errors.Is(err, apiclient.ErrUnauthorized):
			h.bounceToSignIn(w, r)
			return
		case errors.Is(err, apiclient.ErrTicketNotFound):
			_ = h.sessions.IssueFlash(w, session.Flash{Kind: session.FlashError, Message: "That ticket no longer exists."})
			http.Redirect(w, r, "/dashboard/support", http.StatusSeeOther)
			return
		default:
			h.logger.WarnContext(r.Context(), "dashboard: support reply failed", slog.String("err", err.Error()))
			_ = h.sessions.IssueFlash(w, session.Flash{Kind: session.FlashError, Message: "Could not send your reply: " + err.Error()})
			http.Redirect(w, r, detailPath, http.StatusSeeOther)
			return
		}
	}

	_ = h.sessions.IssueFlash(w, session.Flash{
		Kind:    session.FlashSupportReplied,
		Message: "Reply sent.",
	})
	http.Redirect(w, r, detailPath, http.StatusSeeOther)
}

// supportResolveMe refreshes the upstream bearer and loads /v1/me,
// handling the auth-failure bounce in one place. Returns ok=false when
// it has already redirected the response.
func (h *DashboardHandler) supportResolveMe(w http.ResponseWriter, r *http.Request, s session.Session) (string, *apiclient.Me, bool) {
	bearer, err := h.bearer(r.Context(), s)
	if err != nil {
		h.bounceToSignIn(w, r)
		return "", nil, false
	}
	me, err := h.api.GetMeBearer(r.Context(), bearer)
	if err != nil {
		if errors.Is(err, apiclient.ErrUnauthorized) {
			h.bounceToSignIn(w, r)
			return "", nil, false
		}
		// A hard /me failure bounces to sign-in: every support page
		// needs Me to render the dashboard chrome, so there's no
		// useful degraded render without it.
		h.logger.WarnContext(r.Context(), "dashboard support: load me failed", slog.String("err", err.Error()))
		h.bounceToSignIn(w, r)
		return "", nil, false
	}
	return bearer, me, true
}

// bounceToSignIn clears the session and redirects to /sign-in — the
// shared response to an expired/rejected upstream credential.
func (h *DashboardHandler) bounceToSignIn(w http.ResponseWriter, r *http.Request) {
	h.sessions.Clear(w)
	http.Redirect(w, r, "/sign-in", http.StatusFound)
}

// truncate caps a prefilled field at n bytes on a rune boundary.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
