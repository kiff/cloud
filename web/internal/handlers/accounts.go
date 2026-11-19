package handlers

import (
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/kiff/cloud/web/internal/apiclient"
	"github.com/kiff/cloud/web/internal/components"
	"github.com/kiff/cloud/web/internal/pages"
	"github.com/kiff/cloud/web/internal/session"
)

// the governance-roles design: a person can belong to several accounts.
// SelectTenant keeps the session's selected account in line with the
// person's live memberships on every request, sends it to the API
// (apiclient.WithTenant adds the Kiff-Tenant header), and gives the shell
// its account switcher. The API checks the membership; the selection
// grants nothing.

// AccountsHandler serves the account switch and the invitation page.
type AccountsHandler struct {
	api      *apiclient.Client
	sessions *session.Manager
	bearers  BearerSource
	logger   *slog.Logger
}

// NewAccountsHandler wires the switch and invitation routes.
func NewAccountsHandler(api *apiclient.Client, sessions *session.Manager, bearers BearerSource, logger *slog.Logger) *AccountsHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &AccountsHandler{api: api, sessions: sessions, bearers: bearers, logger: logger}
}

// SelectTenant is the middleware; it runs inside RequireSession.
func (h *AccountsHandler) SelectTenant(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s, ok := SessionFrom(r.Context())
		if !ok || h.api == nil {
			next.ServeHTTP(w, r)
			return
		}
		bearer, err := h.bearers.BearerFor(r.Context(), s)
		if err != nil {
			next.ServeHTTP(w, r)
			return
		}
		ctx := withRequestBearer(r.Context(), bearer)
		ms, err := h.api.MembershipsBearer(ctx, bearer)
		if err != nil {
			// An API without memberships, or a hiccup: keep the last
			// selection and let the page's own calls decide.
			if !errors.Is(err, apiclient.ErrUnauthorized) {
				h.logger.WarnContext(ctx, "accounts: memberships read failed", slog.String("err", err.Error()))
			}
			next.ServeHTTP(w, r.WithContext(apiclient.WithTenant(ctx, s.Tenant)))
			return
		}
		var active []apiclient.Membership
		suspended := false
		for _, m := range ms {
			if m.Status == "active" {
				active = append(active, m)
			} else {
				suspended = true
			}
		}
		chosen := ""
		for _, m := range active {
			if m.TenantID == s.Tenant {
				chosen = m.TenantID
			}
		}
		if chosen == "" && len(active) > 0 {
			chosen = active[0].TenantID
		}
		if len(active) == 0 {
			if suspended {
				h.noAccess(w, r, "suspended")
				return
			}
			// No membership yet: a first sign-in, which the API turns into
			// the person's own account on their first call. A person
			// removed from their only account is told so here.
			if _, err := h.api.GetMeBearer(ctx, bearer); err != nil {
				switch {
				case strings.Contains(err.Error(), "membership_removed"):
					h.noAccess(w, r, "removed")
					return
				case strings.Contains(err.Error(), "not_a_member"):
					h.noAccess(w, r, "not_member")
					return
				}
			}
		}
		if chosen != s.Tenant {
			if s.Tenant != "" {
				s.LastDomain = "" // domains belong to the account
			}
			s.Tenant = chosen
			_ = h.sessions.Issue(w, s)
			ctx = withSession(ctx, s)
		}
		sw := components.AccountSwitch{}
		for _, m := range active {
			label := m.Account
			if label == "" {
				label = m.TenantID
			}
			sw.Accounts = append(sw.Accounts, components.AccountOption{TenantID: m.TenantID, Label: label,
				Role: m.Role, Current: m.TenantID == chosen})
		}
		ctx = components.WithAccountSwitch(apiclient.WithTenant(ctx, chosen), sw)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (h *AccountsHandler) noAccess(w http.ResponseWriter, r *http.Request, why string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusForbidden)
	_ = pages.NoAccess(why).Render(r.Context(), w)
}

// Register mounts the switch on the signed-in app's mux.
func (h *AccountsHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /dashboard/account", h.switchAccount)
}

// switchAccount selects another of the person's accounts.
func (h *AccountsHandler) switchAccount(w http.ResponseWriter, r *http.Request) {
	s, ok := SessionFrom(r.Context())
	if !ok || h.api == nil {
		http.Redirect(w, r, "/cards", http.StatusSeeOther)
		return
	}
	want := strings.TrimSpace(r.PostFormValue("tenant"))
	bearer, err := h.bearers.BearerFor(r.Context(), s)
	if err != nil {
		http.Redirect(w, r, "/sign-in?next=/cards", http.StatusFound)
		return
	}
	ms, err := h.api.MembershipsBearer(r.Context(), bearer)
	if err == nil {
		for _, m := range ms {
			if m.TenantID == want && m.Status == "active" && s.Tenant != want {
				s.Tenant, s.LastDomain = want, ""
				_ = h.sessions.Issue(w, s)
			}
		}
	}
	http.Redirect(w, r, "/cards", http.StatusSeeOther)
}

// RegisterInvite mounts the invitation page. It must sit behind
// RequireSession only: SelectTenant and the shell would create an
// account for a person who is arriving to join someone else's.
func (h *AccountsHandler) RegisterInvite(mux *http.ServeMux) {
	mux.HandleFunc("GET /invite/{token}", h.showInvite)
	mux.HandleFunc("POST /invite/{token}", h.acceptInvite)
}

func (h *AccountsHandler) inviteBearer(w http.ResponseWriter, r *http.Request) (session.Session, string, bool) {
	s, ok := SessionFrom(r.Context())
	if !ok {
		http.Redirect(w, r, "/sign-in?next="+url.QueryEscape(r.URL.Path), http.StatusFound)
		return s, "", false
	}
	bearer, err := h.bearers.BearerFor(r.Context(), s)
	if err != nil {
		h.sessions.Clear(w)
		http.Redirect(w, r, "/sign-in?next="+url.QueryEscape(r.URL.Path), http.StatusFound)
		return s, "", false
	}
	return s, bearer, true
}

func (h *AccountsHandler) showInvite(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	v := pages.InviteView{Token: r.PathValue("token")}
	if h.api == nil {
		v.ErrMsg = "The dashboard is not connected to the KIFF API."
		_ = pages.InvitePage(v).Render(r.Context(), w)
		return
	}
	_, bearer, ok := h.inviteBearer(w, r)
	if !ok {
		return
	}
	p, err := h.api.PreviewInviteBearer(r.Context(), bearer, v.Token)
	if err != nil {
		v.ErrMsg = inviteError(err)
	} else {
		v.Preview = &p
	}
	_ = pages.InvitePage(v).Render(r.Context(), w)
}

func (h *AccountsHandler) acceptInvite(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	v := pages.InviteView{Token: r.PathValue("token")}
	if h.api == nil {
		v.ErrMsg = "The dashboard is not connected to the KIFF API."
		_ = pages.InvitePage(v).Render(r.Context(), w)
		return
	}
	s, bearer, ok := h.inviteBearer(w, r)
	if !ok {
		return
	}
	m, err := h.api.AcceptInviteBearer(r.Context(), bearer, v.Token)
	if err != nil {
		v.ErrMsg = inviteError(err)
		_ = pages.InvitePage(v).Render(r.Context(), w)
		return
	}
	// Work in the account just joined.
	s.Tenant, s.LastDomain = m.TenantID, ""
	_ = h.sessions.Issue(w, s)
	http.Redirect(w, r, "/cards", http.StatusSeeOther)
}

func inviteError(err error) string {
	switch apiclient.ErrorCode(err) {
	case "invite_not_pending":
		return "This invitation was already used, was withdrawn, or has expired. Ask an admin of the account for a new one."
	case "already_member":
		return "You are already a member of this account. If your access is suspended, an admin of the account can restore it."
	}
	if errors.Is(err, apiclient.ErrUnauthorized) {
		return "Your sign-in expired. Sign in again and open the link once more."
	}
	return "The invitation could not be read. Try the link again in a moment."
}
