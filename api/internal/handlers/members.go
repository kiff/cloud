package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/kiff/cloud/api/internal/access"
	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/membership"
	"github.com/kiff/cloud/api/internal/notify"
	"github.com/kiff/cloud/api/internal/personlang"
	"github.com/kiff/cloud/api/internal/tenant"
	"github.com/kiff/cloud/api/internal/words"
)

// the governance-roles design: the people on an account. Membership is the security
// boundary; these routes change it, each change checked against the
// invariants and audited in the same transaction (membership.Service).
// The route policy (auth.DefaultRules) lets only admins reach the
// changing routes; this handler adds step-up, the people-facing errors
// and the side effects (emails, revoking a removed member's account
// keys and developer sessions).

// EmailLookup resolves a subject's email address (owneremail's Clerk
// lookup). Used for display and notices; never as an authority.
type EmailLookup interface {
	EmailForSubject(ctx context.Context, subject string) (string, error)
}

// MembersHandler serves /v1/tenant/* and the routes a person uses
// before they are on an account (/v1/me/memberships, /v1/invites/*).
type MembersHandler struct {
	svc         *membership.Service
	tenants     tenant.Store
	emails      EmailLookup
	notifier    notify.MembershipNotifier
	appBaseURL  string
	stepUp      *auth.StepUp
	apiKeys     auth.APIKeyStore
	access      access.Store
	devSessions auth.DeveloperSessionStore
	logger      *slog.Logger
	now         func() time.Time
	dispatch    func(func())
}

// MembersOption configures MembersHandler.
type MembersOption func(*MembersHandler)

// WithMembersEmails resolves member emails for display and notices.
func WithMembersEmails(e EmailLookup) MembersOption {
	return func(h *MembersHandler) { h.emails = e }
}

// WithMembersNotifier sends invitations and account notices.
func WithMembersNotifier(n notify.MembershipNotifier) MembersOption {
	return func(h *MembersHandler) { h.notifier = n }
}

// WithMembersAppBaseURL is the dashboard origin invitation links and
// notices point at (KIFF_APP_BASE_URL).
func WithMembersAppBaseURL(appBaseURL string) MembersOption {
	return func(h *MembersHandler) { h.appBaseURL = strings.TrimRight(strings.TrimSpace(appBaseURL), "/") }
}

// WithMembersStepUp requires a recent sign-in for the sensitive changes.
func WithMembersStepUp(s *auth.StepUp) MembersOption {
	return func(h *MembersHandler) { h.stepUp = s }
}

// WithMembersCredentials lets removal revoke the removed member's
// account keys and developer sessions in the account.
func WithMembersCredentials(keys auth.APIKeyStore, sessions auth.DeveloperSessionStore) MembersOption {
	return func(h *MembersHandler) { h.apiKeys, h.devSessions = keys, sessions }
}

// WithMembersLogger sets the logger.
// WithMembersAccess keeps groups and sharing in step with the account's
// people: someone removed leaves every group and grant, and someone who
// accepts an invitation pending when groups arrived joins Everyone.
func WithMembersAccess(s access.Store) MembersOption {
	return func(h *MembersHandler) { h.access = s }
}

func WithMembersLogger(l *slog.Logger) MembersOption {
	return func(h *MembersHandler) {
		if l != nil {
			h.logger = l
		}
	}
}

// WithMembersClock substitutes the clock (tests).
func WithMembersClock(now func() time.Time) MembersOption {
	return func(h *MembersHandler) { h.now = now }
}

// WithMembersDispatch runs email sends inline (tests).
func WithMembersDispatch(d func(func())) MembersOption {
	return func(h *MembersHandler) { h.dispatch = d }
}

// NewMembersHandler returns the handler, or nil without a service.
func NewMembersHandler(svc *membership.Service, tenants tenant.Store, opts ...MembersOption) *MembersHandler {
	if svc == nil {
		return nil
	}
	h := &MembersHandler{svc: svc, tenants: tenants, logger: slog.Default(),
		now: func() time.Time { return time.Now().UTC() }, dispatch: func(f func()) { go f() }}
	for _, o := range opts {
		o(h)
	}
	return h
}

// Register mounts the account routes; they sit behind the auth
// middleware and the route policy.
func (h *MembersHandler) Register(mux *http.ServeMux) {
	if h == nil {
		return
	}
	mux.HandleFunc("GET /v1/tenant/members", h.listMembers)
	mux.HandleFunc("PUT /v1/tenant/members/{subject}/role", h.changeRole)
	mux.HandleFunc("POST /v1/tenant/members/{subject}/suspend", h.status("suspend"))
	mux.HandleFunc("POST /v1/tenant/members/{subject}/reactivate", h.status("reactivate"))
	mux.HandleFunc("POST /v1/tenant/members/{subject}/remove", h.status("remove"))
	mux.HandleFunc("POST /v1/tenant/leave", h.leave)
	mux.HandleFunc("GET /v1/tenant/invites", h.listInvites)
	mux.HandleFunc("POST /v1/tenant/invites", h.createInvite)
	mux.HandleFunc("POST /v1/tenant/invites/{id}/revoke", h.revokeInvite)
	mux.HandleFunc("GET /v1/tenant/governance", h.getGovernance)
	mux.HandleFunc("PUT /v1/tenant/governance", h.setGovernance)
	mux.HandleFunc("GET /v1/tenant/audit", h.listAudit)
}

// RegisterPersonal mounts the routes a person reaches before (or
// across) accounts. They must sit behind auth.Middleware.WrapPerson,
// which authenticates without resolving a tenant.
func (h *MembersHandler) RegisterPersonal(mux *http.ServeMux) {
	if h == nil {
		return
	}
	mux.HandleFunc("GET /v1/me/memberships", h.myMemberships)
	mux.HandleFunc("POST /v1/invites/preview", h.previewInvite)
	mux.HandleFunc("POST /v1/invites/accept", h.acceptInvite)
}

func (h *MembersHandler) person(w http.ResponseWriter, r *http.Request, needTenant bool) (auth.Identity, bool) {
	id, err := auth.IdentityFrom(r.Context())
	if err != nil || (needTenant && id.Tenant == "") {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "a session is required"})
		return auth.Identity{}, false
	}
	if !id.IsPerson() {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "people_only",
			"message": "the people on an account are managed by people, from a session, not by a key"})
		return auth.Identity{}, false
	}
	return id, true
}

func actorOf(id auth.Identity, r *http.Request) membership.Actor {
	return membership.Actor{Subject: id.Subject, AuthMethod: string(id.Method), RequestID: r.Header.Get("X-Request-Id")}
}

// stepUp answers false (having written reauth_required) when the
// caller's sign-in is not recent; on success the age goes on the actor
// for the audit row.
func (h *MembersHandler) requireStepUp(w http.ResponseWriter, r *http.Request, id auth.Identity, a *membership.Actor) bool {
	if h.stepUp == nil {
		return true
	}
	age, err := h.stepUp.Check(r.Context(), id)
	if err != nil {
		auth.WriteReauthRequired(w)
		return false
	}
	a.ReauthAgeS = &age
	return true
}

func writeMembershipError(w http.ResponseWriter, err error) {
	type e struct {
		status int
		code   string
	}
	for target, m := range map[error]e{
		membership.ErrNotFound:          {http.StatusNotFound, "not_found"},
		membership.ErrNotMember:         {http.StatusForbidden, "not_a_member"},
		membership.ErrAlreadyMember:     {http.StatusConflict, "already_member"},
		membership.ErrInviteNotPending:  {http.StatusGone, "invite_not_pending"},
		membership.ErrLastAdmin:         {http.StatusConflict, "last_admin"},
		membership.ErrLastApprover:      {http.StatusConflict, "last_approver"},
		membership.ErrNoApprover:        {http.StatusConflict, "no_approver"},
		membership.ErrSelfRoleChange:    {http.StatusForbidden, "self_role_change"},
		membership.ErrInvalidRole:       {http.StatusBadRequest, "invalid_role"},
		membership.ErrInvalidLifetime:   {http.StatusBadRequest, "invalid_lifetime"},
		membership.ErrInvalidEmail:      {http.StatusBadRequest, "invalid_email"},
		membership.ErrNoChange:          {http.StatusConflict, "no_change"},
		membership.ErrInvalidTransition: {http.StatusConflict, "invalid_transition"},
	} {
		if errors.Is(err, target) {
			writeJSON(w, m.status, map[string]string{"error": m.code, "message": strings.TrimPrefix(target.Error(), "membership: ")})
			return
		}
	}
	writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "the change could not be saved"})
}

func decodeBody(r *http.Request, v any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, 8<<10))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

func (h *MembersHandler) emailOf(ctx context.Context, subject string) string {
	if h.emails == nil || subject == "" {
		return ""
	}
	e, err := h.emails.EmailForSubject(ctx, subject)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(e)
}

// accountName is the account's name as viewer sees it (accountLabel).
func (h *MembersHandler) accountName(ctx context.Context, tenantID, viewer string) string {
	if h.tenants == nil {
		return ""
	}
	t, err := h.tenants.Get(ctx, tenantID)
	if err != nil {
		return ""
	}
	return accountLabel(ctx, t, h.emails, viewer)
}

func memberView(m membership.Member, email string, self bool) map[string]any {
	v := map[string]any{
		"subject": m.Subject, "role": string(m.Role), "status": string(m.Status),
		"created_at": m.CreatedAt, "updated_at": m.UpdatedAt, "you": self,
		"capabilities": membership.Capabilities(m.Role),
	}
	if email != "" {
		v["email"] = email
	}
	if m.InvitedBy != "" {
		v["invited_by"] = m.InvitedBy
	}
	return v
}

func (h *MembersHandler) listMembers(w http.ResponseWriter, r *http.Request) {
	id, err := auth.IdentityFrom(r.Context())
	if err != nil || id.Tenant == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "a session or key is required"})
		return
	}
	ms, err := h.svc.Store.List(r.Context(), id.Tenant)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "the people on this account could not be read"})
		return
	}
	out := make([]map[string]any, 0, len(ms))
	for _, m := range ms {
		out = append(out, memberView(m, h.emailOf(r.Context(), m.Subject), m.Subject == id.Subject))
	}
	writeJSON(w, http.StatusOK, map[string]any{"members": out})
}

func (h *MembersHandler) changeRole(w http.ResponseWriter, r *http.Request) {
	id, ok := h.person(w, r, true)
	if !ok {
		return
	}
	var body struct {
		Role string `json:"role"`
	}
	if err := decodeBody(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": `the body must be {"role": "admin" | "editor" | "approver" | "viewer"}`})
		return
	}
	role, valid := membership.ParseRole(body.Role)
	if !valid {
		writeMembershipError(w, membership.ErrInvalidRole)
		return
	}
	target := r.PathValue("subject")
	current, err := h.svc.Store.Live(r.Context(), id.Tenant, target)
	if err != nil {
		writeMembershipError(w, err)
		return
	}
	a := actorOf(id, r)
	// Promoting to admin, or demoting an admin, is an admin-level change.
	if (role == membership.RoleAdmin || current.Role == membership.RoleAdmin) && !h.requireStepUp(w, r, id, &a) {
		return
	}
	if paysForSeat(role) && !paysForSeat(current.Role) && !h.seatFits(w, r, id.Tenant) {
		return
	}
	before, after, err := h.svc.ChangeRole(r.Context(), id.Tenant, a, target, role)
	if err != nil {
		writeMembershipError(w, err)
		return
	}
	if after.Role == membership.RoleApprover || after.Role == membership.RoleAdmin {
		h.noticeNewAuthority(id.Tenant, id.Subject, h.emailOf(r.Context(), target), after.Role, before.Role)
	}
	writeJSON(w, http.StatusOK, memberView(after, "", after.Subject == id.Subject))
}

func (h *MembersHandler) status(op string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := h.person(w, r, true)
		if !ok {
			return
		}
		h.changeStatus(w, r, id, op, r.PathValue("subject"))
	}
}

// leave is a person removing themselves; the last admin cannot.
func (h *MembersHandler) leave(w http.ResponseWriter, r *http.Request) {
	id, ok := h.person(w, r, true)
	if !ok {
		return
	}
	h.changeStatus(w, r, id, "remove", id.Subject)
}

func (h *MembersHandler) changeStatus(w http.ResponseWriter, r *http.Request, id auth.Identity, op, target string) {
	ctx := r.Context()
	current, err := h.svc.Store.Live(ctx, id.Tenant, target)
	if err != nil {
		writeMembershipError(w, err)
		return
	}
	a := actorOf(id, r)
	// Removal revokes keys, so it always needs step-up; suspending an
	// admin is an admin-level change.
	if (op == "remove" || (op == "suspend" && current.Role == membership.RoleAdmin)) && !h.requireStepUp(w, r, id, &a) {
		return
	}
	var m membership.Member
	switch op {
	case "suspend":
		m, err = h.svc.Suspend(ctx, id.Tenant, a, target)
	case "reactivate":
		m, err = h.svc.Reactivate(ctx, id.Tenant, a, target)
	case "remove":
		m, err = h.svc.Remove(ctx, id.Tenant, a, target)
	}
	if err != nil {
		writeMembershipError(w, err)
		return
	}
	if op == "remove" {
		h.revokeCredentials(ctx, id.Tenant, a, target)
		// Leaving revokes every grant: coming back restores none.
		if h.access != nil {
			if err := h.access.ForgetSubject(ctx, id.Tenant, target); err != nil {
				h.logger.ErrorContext(ctx, "members: forget a removed member's groups and grants", slog.String("err", err.Error()))
			}
		}
	}
	writeJSON(w, http.StatusOK, memberView(m, "", m.Subject == id.Subject))
}

// revokeCredentials ends a removed member's account keys and developer
// sessions in this account. The membership check already refuses them
// on every request (M4, M15); revoking makes the end permanent and
// visible. Agent keys they minted keep working and are flagged for
// rotation instead: revoking them would stop production.
func (h *MembersHandler) revokeCredentials(ctx context.Context, tenantID string, a membership.Actor, subject string) {
	now := h.now()
	keys, sessions := 0, 0
	if h.apiKeys != nil {
		if recs, err := h.apiKeys.ListActive(ctx, tenantID); err == nil {
			for _, rec := range recs {
				// Their account keys, read-only ones included; never an agent's.
				if rec.Subject != subject || rec.AgentID != "" || !(hasAccountRole(rec.Roles) || slices.Contains(rec.Roles, auth.RoleAccountReader)) {
					continue
				}
				if err := h.apiKeys.Revoke(ctx, rec.ID, now); err == nil {
					keys++
				}
			}
		} else {
			h.logger.ErrorContext(ctx, "members: list keys to revoke failed", slog.String("tenant", tenantID), slog.String("err", err.Error()))
		}
	}
	if h.devSessions != nil {
		if ss, err := h.devSessions.ListActiveForSubject(ctx, subject, now); err == nil {
			for _, s := range ss {
				if s.Tenant != tenantID {
					continue
				}
				if err := h.devSessions.Revoke(ctx, s.ID, now); err == nil {
					sessions++
				}
			}
		} else {
			h.logger.ErrorContext(ctx, "members: list developer sessions to revoke failed", slog.String("tenant", tenantID), slog.String("err", err.Error()))
		}
	}
	if err := h.svc.RecordKeysRevoked(ctx, tenantID, a, subject, keys, sessions); err != nil {
		h.logger.ErrorContext(ctx, "members: audit of revoked credentials failed", slog.String("tenant", tenantID), slog.String("err", err.Error()))
	}
}

func hasAccountRole(roles []string) bool {
	for _, r := range roles {
		if auth.IsAccountRole(r) {
			return true
		}
	}
	return false
}

func inviteView(i membership.Invite, now time.Time) map[string]any {
	v := map[string]any{
		"id": i.ID, "email": i.Email, "role": string(i.Role), "invited_by": i.InvitedBy,
		"created_at": i.CreatedAt, "expires_at": i.ExpiresAt, "state": string(i.StateAt(now)),
	}
	if i.AcceptedAt != nil {
		v["accepted_at"], v["accepted_by"] = i.AcceptedAt, i.AcceptedBy
	}
	if i.RevokedAt != nil {
		v["revoked_at"] = i.RevokedAt
	}
	return v
}

func (h *MembersHandler) listInvites(w http.ResponseWriter, r *http.Request) {
	id, err := auth.IdentityFrom(r.Context())
	if err != nil || id.Tenant == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "a session or key is required"})
		return
	}
	invs, err := h.svc.Store.Invites(r.Context(), id.Tenant)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "the invitations could not be read"})
		return
	}
	now := h.now()
	out := make([]map[string]any, 0, len(invs))
	for _, i := range invs {
		out = append(out, inviteView(i, now))
	}
	writeJSON(w, http.StatusOK, map[string]any{"invites": out})
}

func (h *MembersHandler) createInvite(w http.ResponseWriter, r *http.Request) {
	id, ok := h.person(w, r, true)
	if !ok {
		return
	}
	var body struct {
		Email         string `json:"email"`
		Role          string `json:"role"`
		LifetimeHours int    `json:"lifetime_hours,omitempty"`
	}
	if err := decodeBody(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": `the body must be {"email": "...", "role": "...", "lifetime_hours": n}`})
		return
	}
	role, valid := membership.ParseRole(body.Role)
	if !valid {
		writeMembershipError(w, membership.ErrInvalidRole)
		return
	}
	a := actorOf(id, r)
	if role == membership.RoleAdmin && !h.requireStepUp(w, r, id, &a) {
		return
	}
	if paysForSeat(role) && !h.seatFits(w, r, id.Tenant) {
		return
	}
	// The people invited see the account by its name, never its owner's
	// email: an account nobody named is named first.
	if h.tenants != nil {
		if t, err := h.tenants.Get(r.Context(), id.Tenant); err == nil {
			if name, _ := tenant.NameFor(t); name == "" {
				writeJSON(w, http.StatusConflict, map[string]string{"error": "account_unnamed", "message": "Name the account first: it's what the people you invite will see it called."})
				return
			}
		}
	}
	inv, token, err := h.svc.CreateInvite(r.Context(), id.Tenant, a, membership.InviteRequest{
		Email: body.Email, Role: role, Lifetime: time.Duration(body.LifetimeHours) * time.Hour,
	})
	if err != nil {
		writeMembershipError(w, err)
		return
	}
	acceptURL := h.appBaseURL + "/invite/" + url.PathEscape(token)
	if h.notifier != nil {
		// The person invited sees the account by its name and the inviter as an admin of
		// it, never by their email.
		// The person invited has no language yet: the account's last one.
		lang := personlang.Account(h.account(r.Context(), id.Tenant))
		account, by := h.accountName(r.Context(), id.Tenant, ""), words.Of(lang).AnAdmin
		if account == Unnamed {
			account = "" // "a KIFF account", not a name in quotes
		}
		h.send("invitation", func(ctx context.Context) error {
			return h.notifier.MemberInvited(ctx, inv.Email, notify.MemberInvite{
				Account: account, Role: string(inv.Role), InvitedBy: by, AcceptURL: acceptURL, ExpiresAt: inv.ExpiresAt, Lang: lang,
			})
		})
	}
	if role == membership.RoleApprover || role == membership.RoleAdmin {
		h.noticeNewAuthority(id.Tenant, id.Subject, inv.Email, role, "")
	}
	v := inviteView(inv, h.now())
	// The link is returned once so an admin can share it another way when
	// email is not configured. It is never stored or listed again.
	v["accept_url"] = acceptURL
	writeJSON(w, http.StatusCreated, v)
}

func (h *MembersHandler) revokeInvite(w http.ResponseWriter, r *http.Request) {
	id, ok := h.person(w, r, true)
	if !ok {
		return
	}
	inv, err := h.svc.RevokeInvite(r.Context(), id.Tenant, actorOf(id, r), r.PathValue("id"))
	if err != nil {
		writeMembershipError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, inviteView(inv, h.now()))
}

func (h *MembersHandler) getGovernance(w http.ResponseWriter, r *http.Request) {
	id, err := auth.IdentityFrom(r.Context())
	if err != nil || id.Tenant == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "a session or key is required"})
		return
	}
	g, err := h.svc.Store.Governance(r.Context(), id.Tenant)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "the account's policy could not be read"})
		return
	}
	writeJSON(w, http.StatusOK, governanceView(g))
}

func governanceView(g membership.Governance) map[string]any {
	v := map[string]any{"separation_of_duties": g.SeparationOfDuties}
	if !g.UpdatedAt.IsZero() && g.UpdatedBy != "" {
		v["updated_by"], v["updated_at"] = g.UpdatedBy, g.UpdatedAt
	}
	return v
}

func (h *MembersHandler) setGovernance(w http.ResponseWriter, r *http.Request) {
	id, ok := h.person(w, r, true)
	if !ok {
		return
	}
	var body struct {
		SeparationOfDuties *bool `json:"separation_of_duties"`
	}
	if err := decodeBody(r, &body); err != nil || body.SeparationOfDuties == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": `the body must be {"separation_of_duties": true | false}`})
		return
	}
	on := *body.SeparationOfDuties
	a := actorOf(id, r)
	// Turning it off cannot be done silently: step-up, audit, and every
	// active approver is told.
	if !on && !h.requireStepUp(w, r, id, &a) {
		return
	}
	g, err := h.svc.SetSeparationOfDuties(r.Context(), id.Tenant, a, on)
	if err != nil {
		writeMembershipError(w, err)
		return
	}
	if !on {
		who := h.emailOf(r.Context(), id.Subject)
		h.noticeApprovers(id.Tenant, func(w *words.Words, account string) (string, string) {
			return w.SoDOffSubject, w.SoDOffBody(orWord(who, w.AnAdmin), account)
		})
	}
	writeJSON(w, http.StatusOK, governanceView(g))
}

func (h *MembersHandler) listAudit(w http.ResponseWriter, r *http.Request) {
	id, err := auth.IdentityFrom(r.Context())
	if err != nil || id.Tenant == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "a session or key is required"})
		return
	}
	evs, err := h.svc.Store.Audit(r.Context(), id.Tenant, 200)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "the trail could not be read"})
		return
	}
	out := make([]map[string]any, 0, len(evs))
	for _, e := range evs {
		v := map[string]any{"id": e.ID, "actor": e.Actor, "action": e.Action, "at": e.At}
		for k, s := range map[string]string{"target": e.Target, "old_value": e.OldValue, "new_value": e.NewValue, "auth_method": e.AuthMethod} {
			if s != "" {
				v[k] = s
			}
		}
		if e.ReauthAgeS != nil {
			v["reauth_age_s"] = *e.ReauthAgeS
		}
		out = append(out, v)
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": out})
}

// myMemberships lists every account the person belongs to, for the
// dashboard's account switcher. It runs without a selected tenant.
func (h *MembersHandler) myMemberships(w http.ResponseWriter, r *http.Request) {
	id, ok := h.person(w, r, false)
	if !ok {
		return
	}
	ms, err := h.svc.Store.ForSubject(r.Context(), id.Subject)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "your accounts could not be read"})
		return
	}
	out := make([]map[string]any, 0, len(ms))
	for _, m := range ms {
		out = append(out, map[string]any{
			"tenant_id": m.TenantID, "account": h.accountName(r.Context(), m.TenantID, id.Subject),
			"role": string(m.Role), "status": string(m.Status),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"memberships": out})
}

func (h *MembersHandler) tokenBody(w http.ResponseWriter, r *http.Request) (string, bool) {
	var body struct {
		Token string `json:"token"`
	}
	if err := decodeBody(r, &body); err != nil || strings.TrimSpace(body.Token) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": `the body must be {"token": "..."}`})
		return "", false
	}
	return strings.TrimSpace(body.Token), true
}

// previewInvite shows the signed-in person what an invitation offers
// before they accept: the account, the role and who invited them.
func (h *MembersHandler) previewInvite(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.person(w, r, false); !ok {
		return
	}
	token, ok := h.tokenBody(w, r)
	if !ok {
		return
	}
	inv, err := h.svc.PendingInvite(r.Context(), token, h.now())
	if err != nil {
		if errors.Is(err, membership.ErrNotFound) {
			err = membership.ErrInviteNotPending
		}
		writeMembershipError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"tenant_id": inv.TenantID, "account": h.accountName(r.Context(), inv.TenantID, ""),
		"role": string(inv.Role), "invited_by": "An admin", // never the inviter's email
		"expires_at": inv.ExpiresAt,
	})
}

// acceptInvite: only the token plus the authenticated person's explicit
// acceptance grants anything (M5).
func (h *MembersHandler) acceptInvite(w http.ResponseWriter, r *http.Request) {
	id, ok := h.person(w, r, false)
	if !ok {
		return
	}
	if id.Method != auth.MethodSession {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "session_required", "message": "accept an invitation from the dashboard, signed in"})
		return
	}
	token, ok := h.tokenBody(w, r)
	if !ok {
		return
	}
	inv, _ := h.svc.PendingInvite(r.Context(), token, h.now())
	m, err := h.svc.AcceptInvite(r.Context(), token, actorOf(id, r))
	if err != nil {
		if errors.Is(err, membership.ErrNotFound) {
			err = membership.ErrInviteNotPending
		}
		writeMembershipError(w, err)
		return
	}
	if h.access != nil && inv.ID != "" {
		if err := h.access.WelcomeInvite(r.Context(), m.TenantID, inv.ID, m.Subject, h.now()); err != nil {
			h.logger.ErrorContext(r.Context(), "members: join Everyone from an invitation", slog.String("err", err.Error()))
		}
	}
	h.noticeJoined(m, inv.Email)
	writeJSON(w, http.StatusOK, map[string]any{
		"tenant_id": m.TenantID, "account": h.accountName(r.Context(), m.TenantID, m.Subject), "role": string(m.Role),
	})
}

// orWord is s, or the language's word for who it is when KIFF can't
// name them ("An admin", "Someone").
func orWord(s, word string) string {
	if s != "" {
		return s
	}
	return word
}

// account is the tenant for its people's languages; empty when it can't
// be read, which reads English.
func (h *MembersHandler) account(ctx context.Context, tenantID string) tenant.Tenant {
	if h.tenants == nil {
		return tenant.Tenant{}
	}
	t, err := h.tenants.Get(ctx, tenantID)
	if err != nil {
		return tenant.Tenant{}
	}
	return t
}

func (h *MembersHandler) send(what string, f func(ctx context.Context) error) {
	h.dispatch(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := f(ctx); err != nil {
			h.logger.ErrorContext(ctx, "members: email failed", slog.String("email", what), slog.String("err", err.Error()))
		}
	})
}

// noticeNewAuthority tells every other active admin and approver that
// someone was invited or promoted to approver or admin (M14). It is the
// control for the case no rule can stop: one person holding two sign-ins.
// from is the role before a change; "" for an invitation.
func (h *MembersHandler) noticeNewAuthority(tenantID, actorSubject, who string, role membership.Role, from membership.Role) {
	if h.notifier == nil {
		return
	}
	h.send("authority notice", func(ctx context.Context) error {
		by := h.emailOf(ctx, actorSubject)
		return h.noticeRoles(ctx, tenantID, actorSubject, func(w *words.Words, account string) (string, string) {
			body := w.AuthorityInvited(orWord(who, w.Someone), w.Role(string(role)), account, orWord(by, w.AnAdmin))
			if from != "" {
				body = w.AuthorityChanged(orWord(who, w.Someone), w.Role(string(from)), w.Role(string(role)), account, orWord(by, w.AnAdmin))
			}
			return w.AuthoritySubject(w.Role(string(role)), account), body + " " + w.NoticeWhy
		}, membership.RoleAdmin, membership.RoleApprover)
	})
}

// noticeJoined tells the account's admins and approvers that someone
// accepted an invitation. When the account that accepted is not the
// address the invitation was sent to, the notice says so: that is the
// one sign of a forwarded link.
func (h *MembersHandler) noticeJoined(m membership.Member, invited string) {
	if h.notifier == nil {
		return
	}
	h.send("joined notice", func(ctx context.Context) error {
		who := h.emailOf(ctx, m.Subject)
		return h.noticeRoles(ctx, m.TenantID, m.Subject, func(w *words.Words, account string) (string, string) {
			role := w.Role(string(m.Role))
			var body string
			switch {
			case who == "":
				body = w.JoinedUnknown(invited, role, account)
			case invited != "" && !strings.EqualFold(who, invited):
				body = w.JoinedOther(invited, who, role, account)
			default:
				body = w.JoinedSelf(who, role, account)
			}
			return w.JoinedSubject(account), body + " " + w.NoticeWhy
		}, membership.RoleAdmin, membership.RoleApprover)
	})
}

// NoticeCardChange emails every active approver, in the background,
// that a held action was let through by changing its Card while
// separation of duties is on. It names the person by email and the Card
// by an agent that holds it ("a Card", since a shared Card has several),
// so an approver can read it without
// looking anything up; the Card ids follow as a reference.
func (h *MembersHandler) NoticeCardChange(_ context.Context, tenantID string, c CardChange) {
	if h == nil || h.notifier == nil {
		return
	}
	h.send("card change notice", func(ctx context.Context) error {
		who := c.Changer
		if !strings.Contains(who, "@") {
			if e := h.emailOf(ctx, who); e != "" {
				who = e
			}
		}
		return h.noticeRoles(ctx, tenantID, "", func(w *words.Words, _ string) (string, string) {
			body := w.CardChangeBody(orWord(who, w.Someone), c.Agent, c.Action, c.Entity)
			if len(c.Cards) > 0 {
				body += " " + w.CardChangeCards(strings.Join(c.Cards, ", "))
			}
			return w.CardChangeSubject, body
		}, membership.RoleApprover)
	})
}

func (h *MembersHandler) noticeApprovers(tenantID string, compose noticeWords) {
	if h.notifier == nil {
		return
	}
	h.send("approver notice", func(ctx context.Context) error {
		return h.noticeRoles(ctx, tenantID, "", compose, membership.RoleApprover)
	})
}

// noticeWords composes a notice's subject and body in w, with the
// account's label in the same language.
type noticeWords func(w *words.Words, account string) (subject, body string)

// noticeRoles emails the account's active people in roles, except one,
// each in their own language: the one their app last showed them
// (personlang), else the account's, else English.
func (h *MembersHandler) noticeRoles(ctx context.Context, tenantID, except string, compose noticeWords, roles ...membership.Role) error {
	ms, err := h.svc.Store.List(ctx, tenantID)
	if err != nil {
		return err
	}
	t := h.account(ctx, tenantID)
	label := ""
	if h.tenants != nil && t.ID != "" {
		label = accountLabel(ctx, t, h.emails, "")
	}
	var errs []error
	for _, m := range ms {
		if !m.Active() || m.Subject == except || !hasRole(roles, m.Role) {
			continue
		}
		to := h.emailOf(ctx, m.Subject)
		if to == "" {
			continue
		}
		lang := personlang.Of(t, m.Subject)
		w := words.Of(lang)
		account := label
		if account == Unnamed || account == "" {
			account = w.UnnamedAccount
		}
		subject, body := compose(w, account)
		if err := h.notifier.AccountNotice(ctx, to, notify.AccountNotice{Subject: subject, Body: body, URL: h.appBaseURL + "/dashboard/people", Lang: lang}); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func hasRole(roles []membership.Role, r membership.Role) bool {
	for _, x := range roles {
		if x == r {
			return true
		}
	}
	return false
}

// paysForSeat reports that a role is one the plan pays for: the people
// who run things, admins and editors. Approvers and
// viewers are free, on every plan.
func paysForSeat(r membership.Role) bool {
	return r == membership.RoleAdmin || r == membership.RoleEditor
}

// seatFits reports that one more person who runs things fits the plan,
// writing the refusal when not. The trial and Solo have one; Team's seats
// follow its people, billed from the next invoice; company and internal
// have no limit. A seat is taken by a live admin or editor, or a pending
// invitation as one. On a store error it allows, and logs: a plan that
// can't be read must not stop running the account.
func (h *MembersHandler) seatFits(w http.ResponseWriter, r *http.Request, tenantID string) bool {
	if h.tenants == nil {
		return true
	}
	t, err := h.tenants.Get(r.Context(), tenantID)
	if err != nil {
		h.logger.WarnContext(r.Context(), "members: plan unreadable; allowing", slog.String("tenant", tenantID), slog.String("err", err.Error()))
		return true
	}
	l := tenant.PlanFor(t)
	if l.Canonical == tenant.PlanTeam || l.Seats == 0 {
		return true
	}
	members, err := h.svc.Store.List(r.Context(), tenantID)
	if err != nil {
		h.logger.WarnContext(r.Context(), "members: members unreadable; allowing", slog.String("tenant", tenantID), slog.String("err", err.Error()))
		return true
	}
	invites, err := h.svc.Store.Invites(r.Context(), tenantID)
	if err != nil {
		h.logger.WarnContext(r.Context(), "members: invitations unreadable; allowing", slog.String("tenant", tenantID), slog.String("err", err.Error()))
		return true
	}
	used := 0
	for _, m := range members {
		if paysForSeat(m.Role) {
			used++
		}
	}
	now := h.now()
	for _, inv := range invites {
		if paysForSeat(inv.Role) && inv.StateAt(now) == membership.InvitePending {
			used++
		}
	}
	if used < l.Seats {
		return true
	}
	writeJSON(w, http.StatusConflict, map[string]string{
		"error":   "seats_full",
		"message": "Your plan has one person who runs things. Approvers and viewers are free.",
	})
	return false
}
