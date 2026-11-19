package handlers

import (
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/kiff/cloud/web/internal/apiclient"
	"github.com/kiff/cloud/web/internal/pages"
	"github.com/kiff/cloud/web/internal/session"
)

// PeopleHandler is the People page: who is on the account and with
// which role, invitations, the separation-of-duties policy, and the
// trail of changes. Every change is decided by the API; the page shows
// admins the controls and everyone else the list.
type PeopleHandler struct {
	api      *apiclient.Client
	sessions *session.Manager
	bearers  BearerSource
	logger   *slog.Logger
}

// NewPeopleHandler wires the page.
func NewPeopleHandler(api *apiclient.Client, sessions *session.Manager, bearers BearerSource, logger *slog.Logger) *PeopleHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &PeopleHandler{api: api, sessions: sessions, bearers: bearers, logger: logger}
}

// Register mounts the page and its forms.
func (h *PeopleHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /dashboard/people", h.show)
	mux.HandleFunc("GET /dashboard/people/invite", h.inviteSheet)
	mux.HandleFunc("POST /dashboard/people/invite", h.invite)
	mux.HandleFunc("POST /dashboard/people/invites/{id}/revoke", h.revokeInvite)
	mux.HandleFunc("POST /dashboard/people/members/{subject}/role", h.changeRole)
	mux.HandleFunc("POST /dashboard/people/members/{subject}/{action}", h.memberAction)
	mux.HandleFunc("POST /dashboard/people/leave", h.leave)
	mux.HandleFunc("POST /dashboard/people/policy", h.setPolicy)
}

var peopleTabs = map[string]bool{"people": true, "invite": true, "policy": true, "trail": true, "usage": true}

func (h *PeopleHandler) bearer(w http.ResponseWriter, r *http.Request) (session.Session, string, bool) {
	s, ok := SessionFrom(r.Context())
	if !ok {
		http.Redirect(w, r, "/sign-in?next=/dashboard/people", http.StatusFound)
		return s, "", false
	}
	if tok, ok := r.Context().Value(requestBearerKey{}).(string); ok && tok != "" {
		return s, tok, true
	}
	b, err := h.bearers.BearerFor(r.Context(), s)
	if err != nil {
		h.sessions.Clear(w)
		http.Redirect(w, r, "/sign-in?next=/dashboard/people", http.StatusFound)
		return s, "", false
	}
	return s, b, true
}

// view loads the page's data for tab.
func (h *PeopleHandler) view(r *http.Request, s session.Session, bearer, tab string) pages.PeopleView {
	v := pages.PeopleView{DisplayName: s.DisplayName, Email: s.Email, Tab: tab}
	if h.api == nil {
		v.ErrMsg = "The dashboard is not connected to the KIFF API."
		return v
	}
	ctx := r.Context()
	if me, err := h.api.GetMeBearer(ctx, bearer); err == nil {
		v.Me = me
		v.IsAdmin = me.Can("members.manage")
	}
	var err error
	if v.Members, err = h.api.MembersBearer(ctx, bearer); err != nil {
		v.ErrMsg = "The people on this account could not be read. Reload the page in a moment."
	}
	for _, m := range v.Members {
		if m.Role == "approver" && m.Status == "active" {
			v.ActiveApprovers++
		}
		if m.Role == "admin" && m.Status == "active" {
			v.ActiveAdmins++
		}
	}
	if g, err := h.api.GovernanceBearer(ctx, bearer); err == nil {
		v.Governance = g
	}
	if tab == "invite" {
		if invs, err := h.api.InvitesBearer(ctx, bearer); err == nil {
			for _, i := range invs {
				if i.State == "pending" {
					v.Invites = append(v.Invites, i)
				}
			}
		}
	}
	if tab == "trail" && v.IsAdmin {
		v.Events, _ = h.api.MembershipAuditBearer(ctx, bearer)
	}
	if v.Me != nil {
		v.SeesUsage = v.Me.Can("billing.manage")
	}
	if tab == "usage" && v.SeesUsage {
		if u, err := h.api.UsagePeopleBearer(ctx, bearer); err == nil {
			v.Usage = &u
		} else {
			v.ErrMsg = "Usage by person could not be read. Reload the page in a moment."
		}
	}
	return v
}

func (h *PeopleHandler) show(w http.ResponseWriter, r *http.Request) {
	s, bearer, ok := h.bearer(w, r)
	if !ok {
		return
	}
	tab := r.URL.Query().Get("tab")
	if !peopleTabs[tab] {
		tab = "people"
	}
	v := h.view(r, s, bearer, tab)
	v.Notice = peopleNotice(r.URL.Query().Get("done"))
	if code := r.URL.Query().Get("err"); code != "" {
		v.ErrMsg, v.ReauthURL = peopleError(code, tab)
	}
	h.render(w, r, v)
}

// inviteSheet is the invitation on its own full screen. Only an admin
// gets it; anyone else goes back to the tab, which says why.
func (h *PeopleHandler) inviteSheet(w http.ResponseWriter, r *http.Request) {
	_, bearer, ok := h.bearer(w, r)
	if !ok {
		return
	}
	if h.api != nil {
		if me, err := h.api.GetMeBearer(r.Context(), bearer); err != nil || !me.Can("members.manage") {
			http.Redirect(w, r, "/dashboard/people?tab=invite", http.StatusSeeOther)
			return
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = pages.InviteSheet().Render(r.Context(), w)
}

func (h *PeopleHandler) render(w http.ResponseWriter, r *http.Request, v pages.PeopleView) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = pages.People(v).Render(r.Context(), w)
}

// done redirects back to tab with the outcome of a change.
func (h *PeopleHandler) done(w http.ResponseWriter, r *http.Request, tab string, err error, ok string) {
	q := url.Values{"tab": {tab}}
	switch {
	case err == nil:
		q.Set("done", ok)
	case errors.Is(err, apiclient.ErrUnauthorized):
		http.Redirect(w, r, "/sign-in?next="+url.QueryEscape("/dashboard/people?tab="+tab), http.StatusFound)
		return
	default:
		code := apiclient.ErrorCode(err)
		if code == "" {
			h.logger.WarnContext(r.Context(), "people: change failed", slog.String("err", err.Error()))
			code = "failed"
		}
		q.Set("err", code)
	}
	http.Redirect(w, r, "/dashboard/people?"+q.Encode(), http.StatusSeeOther)
}

func (h *PeopleHandler) invite(w http.ResponseWriter, r *http.Request) {
	s, bearer, ok := h.bearer(w, r)
	if !ok || h.api == nil {
		return
	}
	email := strings.TrimSpace(r.PostFormValue("email"))
	role := strings.TrimSpace(r.PostFormValue("role"))
	hours, _ := strconv.Atoi(r.PostFormValue("lifetime_hours"))
	inv, err := h.api.CreateInviteBearer(r.Context(), bearer, email, role, hours)
	if err != nil {
		h.done(w, r, "invite", err, "")
		return
	}
	// Rendered, not redirected: the link carries the single-use token
	// and is shown once, for an admin to share another way if the email
	// does not arrive. It never goes into a URL or a log.
	v := h.view(r, s, bearer, "invite")
	v.Notice = "Invitation sent to " + inv.Email + "."
	v.NewInvite = &inv
	h.render(w, r, v)
}

func (h *PeopleHandler) revokeInvite(w http.ResponseWriter, r *http.Request) {
	_, bearer, ok := h.bearer(w, r)
	if !ok || h.api == nil {
		return
	}
	h.done(w, r, "invite", h.api.RevokeInviteBearer(r.Context(), bearer, r.PathValue("id")), "invite_revoked")
}

func (h *PeopleHandler) changeRole(w http.ResponseWriter, r *http.Request) {
	_, bearer, ok := h.bearer(w, r)
	if !ok || h.api == nil {
		return
	}
	err := h.api.ChangeRoleBearer(r.Context(), bearer, r.PathValue("subject"), r.PostFormValue("role"))
	h.done(w, r, "people", err, "role_changed")
}

func (h *PeopleHandler) memberAction(w http.ResponseWriter, r *http.Request) {
	_, bearer, ok := h.bearer(w, r)
	if !ok || h.api == nil {
		return
	}
	action := r.PathValue("action")
	if action != "suspend" && action != "reactivate" && action != "remove" {
		http.NotFound(w, r)
		return
	}
	err := h.api.MemberActionBearer(r.Context(), bearer, r.PathValue("subject"), action)
	h.done(w, r, "people", err, "member_"+action)
}

func (h *PeopleHandler) leave(w http.ResponseWriter, r *http.Request) {
	s, bearer, ok := h.bearer(w, r)
	if !ok || h.api == nil {
		return
	}
	if err := h.api.LeaveBearer(r.Context(), bearer); err != nil {
		h.done(w, r, "people", err, "")
		return
	}
	s.Tenant, s.LastDomain = "", ""
	_ = h.sessions.Issue(w, s)
	http.Redirect(w, r, "/cards", http.StatusSeeOther)
}

func (h *PeopleHandler) setPolicy(w http.ResponseWriter, r *http.Request) {
	_, bearer, ok := h.bearer(w, r)
	if !ok || h.api == nil {
		return
	}
	on := r.PostFormValue("separation_of_duties") == "on"
	done := "sod_off"
	if on {
		done = "sod_on"
	}
	h.done(w, r, "policy", h.api.SetSeparationOfDutiesBearer(r.Context(), bearer, on), done)
}

func peopleNotice(code string) string {
	return map[string]string{
		"invite_revoked":    "The invitation was withdrawn. Its link no longer works.",
		"role_changed":      "The role was changed. It applies from the person's next request.",
		"member_suspend":    "Access suspended. It can be restored at any time.",
		"member_reactivate": "Access restored, with the same role as before.",
		"member_remove":     "Removed from the account. Their account keys and CLI sessions were revoked; agent keys they created keep working and are flagged on API keys.",
		"sod_on":            "Separation of duties is on. Only approvers answer held actions now.",
		"sod_off":           "Separation of duties is off. Approvers were told by email.",
	}[code]
}

// peopleError turns an API refusal into what the page says, and for a
// change that needs a recent sign-in, where to sign in again.
func peopleError(code, tab string) (msg, reauthURL string) {
	if code == "reauth_required" {
		return "This change needs a recent sign-in. Sign in again, then repeat it within ten minutes.",
			"/sign-out?next=" + url.QueryEscape("/dashboard/people?tab="+tab)
	}
	if m, ok := map[string]string{
		"last_admin":         "An account always keeps at least one active admin. Make someone else an admin first.",
		"last_approver":      "Separation of duties is on, so the account keeps at least one active approver. Add another approver, or turn separation of duties off first.",
		"no_approver":        "Separation of duties needs someone to answer held actions. Invite an approver first.",
		"self_role_change":   "You cannot change your own role, except to step down from admin while another admin remains.",
		"invalid_role":       "Choose one of the four roles.",
		"invalid_email":      "Enter a valid email address.",
		"invalid_lifetime":   "That expiry is out of range: admin invitations last at most 72 hours, others at most 90 days.",
		"no_change":          "Nothing changed.",
		"invalid_transition": "That change does not apply to this person right now. Reload the page.",
		"not_found":          "That person or invitation is no longer on the account.",
		"invite_not_pending": "That invitation was already used, withdrawn, or has expired.",
		"role_forbidden":     "Your role on this account does not allow this.",
		"account_unnamed":    "Name the account first, in Settings: it's what the people you invite see it called, never your email.",
	}[code]; ok {
		return m, ""
	}
	return "The change could not be made. Try again in a moment.", ""
}
