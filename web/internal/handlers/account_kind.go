package handlers

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/kiff/cloud/web/internal/apiclient"
	"github.com/kiff/cloud/web/internal/pages"
	"github.com/kiff/cloud/web/internal/session"
)

// "For me" or "for a company". An account whose owner has not chosen is
// asked once, at /welcome, before the dashboard; the choice can be
// changed in Settings. It changes what the account's people see, not
// what any Card, exception or statement is.

// asksAccountKind reports whether a signed-in GET of path should first
// ask what the account is for: every page of the app except the
// question itself and the device approval page (a CLI is waiting on it).
func asksAccountKind(path string) bool {
	return path != "/welcome" && !strings.HasPrefix(path, "/auth/")
}

// safeNext keeps a return path on this site: a local absolute path, and
// not one that a browser reads as another host. Browsers treat "\\" as
// "/" and drop tabs and newlines, so "/\\evil" and "/<tab>/evil" become
// "//evil"; any of those, or any control character, is refused.
func safeNext(next string) string {
	bad := !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") ||
		strings.ContainsAny(next, "\\ ") || next == "/welcome" || strings.HasPrefix(next, "/welcome?")
	for _, r := range next {
		if r < 0x20 || r == 0x7f {
			bad = true
		}
	}
	if bad {
		return "/cards"
	}
	return next
}

func (h *DashboardHandler) welcome(w http.ResponseWriter, r *http.Request) {
	s, ok := SessionFrom(r.Context())
	if !ok {
		http.Redirect(w, r, "/sign-in", http.StatusFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	flash, _ := h.sessions.ReadFlash(w, r)
	_ = pages.Welcome(pages.WelcomeView{Email: s.Email, DisplayName: s.DisplayName, Flash: convertFlash(flash),
		Next: safeNext(r.URL.Query().Get("next"))}).Render(r.Context(), w)
}

// setAccountKind handles POST /welcome (the first choice) and POST
// /dashboard/account-kind (a change from Settings).
func (h *DashboardHandler) setAccountKind(w http.ResponseWriter, r *http.Request) {
	s, ok := SessionFrom(r.Context())
	if !ok {
		http.Redirect(w, r, "/sign-in", http.StatusFound)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form", http.StatusBadRequest)
		return
	}
	fromSettings := r.URL.Path != "/welcome"
	back := "/welcome?next=" + safeNext(r.PostFormValue("next"))
	done := safeNext(r.PostFormValue("next"))
	if fromSettings {
		back, done = "/dashboard/settings?tab=view", "/dashboard/settings?tab=view"
	}
	kind := r.PostFormValue("kind")
	fail := func(msg string) {
		_ = h.sessions.IssueFlash(w, session.Flash{Kind: session.FlashError, Message: msg})
		http.Redirect(w, r, back, http.StatusSeeOther)
	}
	if kind != "personal" && kind != "company" {
		fail("Choose whether this account is for you or for a company.")
		return
	}
	if h.api == nil {
		fail("apps/api is not configured.")
		return
	}
	bearer, err := h.bearer(r.Context(), s)
	if err != nil {
		h.sessions.Clear(w)
		http.Redirect(w, r, "/sign-in", http.StatusFound)
		return
	}
	switch err := h.api.SetAccountKindBearer(r.Context(), bearer, kind); {
	case errors.Is(err, apiclient.ErrForbidden):
		fail("Only the account's owner or an admin can choose what the account is for.")
		return
	case err != nil:
		h.logger.WarnContext(r.Context(), "account kind: save failed", slog.String("err", err.Error()))
		fail("Your choice could not be saved. Try again in a moment.")
		return
	}
	if fromSettings {
		_ = h.sessions.IssueFlash(w, session.Flash{Kind: session.FlashNotificationSaved, Message: pages.AccountKindSaved(kind)})
	}
	http.Redirect(w, r, done, http.StatusSeeOther)
}

// setChatApprovals handles POST /dashboard/chat-approvals: whether a held
// call is also offered in the agent's chat.
func (h *DashboardHandler) setChatApprovals(w http.ResponseWriter, r *http.Request) {
	const back = "/dashboard/settings?tab=account"
	s, ok := SessionFrom(r.Context())
	if !ok {
		http.Redirect(w, r, "/sign-in", http.StatusFound)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form", http.StatusBadRequest)
		return
	}
	fail := func(msg string) {
		_ = h.sessions.IssueFlash(w, session.Flash{Kind: session.FlashError, Message: msg})
		http.Redirect(w, r, back, http.StatusSeeOther)
	}
	v := r.PostFormValue("enabled")
	if v != "true" && v != "false" {
		fail("Choose whether held calls are offered in the agent's chat.")
		return
	}
	if h.api == nil {
		fail("apps/api is not configured.")
		return
	}
	bearer, err := h.bearer(r.Context(), s)
	if err != nil {
		h.sessions.Clear(w)
		http.Redirect(w, r, "/sign-in", http.StatusFound)
		return
	}
	switch err := h.api.SetChatApprovalsBearer(r.Context(), bearer, v == "true"); {
	case errors.Is(err, apiclient.ErrForbidden):
		fail("Only the account's owner or an admin can change this.")
		return
	case err != nil:
		h.logger.WarnContext(r.Context(), "chat approvals: save failed", slog.String("err", err.Error()))
		fail("Your choice could not be saved. Try again in a moment.")
		return
	}
	msg := "Held calls are no longer offered in the agent's chat. Email and Needs you are unchanged."
	if v == "true" {
		msg = "Held calls are now also offered in the agent's chat, for clients KIFF has verified."
	}
	_ = h.sessions.IssueFlash(w, session.Flash{Kind: session.FlashNotificationSaved, Message: msg})
	http.Redirect(w, r, back, http.StatusSeeOther)
}
