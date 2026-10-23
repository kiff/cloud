package auth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/kiff/kiff/pkg/kiff/actor"

	"github.com/kiff/cloud/api/internal/membership"
)

// TenantHeader names the tenant a person's session request operates on
// when they belong to more than one. It
// is a selector, never an authority: it only chooses which membership
// is looked up.
const TenantHeader = "Kiff-Tenant"

// Memberships is what the middleware needs from the membership store.
type Memberships interface {
	Latest(ctx context.Context, tenantID, subject string) (membership.Member, error)
	ForSubject(ctx context.Context, subject string) ([]membership.Member, error)
	HasRemoved(ctx context.Context, subject string) (bool, error)
	// EnsureFounder returns the subject's live membership in the tenant,
	// making them its founding admin when the tenant has no members;
	// membership.ErrNotMember when the tenant has members and not them.
	EnsureFounder(ctx context.Context, tenantID, subject string) (membership.Member, error)
}

// WithMemberships turns on membership resolution: a person's request
// resolves its tenant, then the membership in that tenant, and carries
// the membership's role instead of the owner widening. An account key
// acts with no more than its minter's current role (M15). Nil keeps the
// one-login behavior (in-memory smoke runs without the store).
func WithMemberships(store Memberships) MiddlewareOption {
	return func(m *Middleware) { m.members = store }
}

// Can reports whether the identity holds capability c. A person on an
// account answers from their membership's role. A key (or a person in a
// deploy without memberships) keeps today's rule: management needs the
// account roles on a credential not bound to an agent.
func (id Identity) Can(c membership.Capability) bool {
	if id.MemberRole != "" {
		return membership.Can(id.MemberRole, c)
	}
	if id.IsConnector() {
		// The domain writes a connection makes are allowed by their
		// handlers for the gateway's own domain only, so a connector key
		// does not hold domains.manage.
		return c == membership.CapRead || c == membership.CapPersonal || c == membership.CapToolsConnect
	}
	switch c {
	case membership.CapRead, membership.CapPersonal, membership.CapActionsPropose:
		return true
	case membership.CapHoldsAnswer:
		return id.AgentID == ""
	}
	return id.ManagesAccount()
}

// IsPerson reports whether the request comes from a person (a dashboard
// session or a developer session), not a key.
func (id Identity) IsPerson() bool {
	return !id.APIKey && (id.Method == MethodSession || id.Method == MethodDeveloper)
}

// membershipError is a refusal at resolution time, with a stable code.
type membershipError struct {
	status  int
	code    string
	message string
}

func (e *membershipError) Error() string { return e.code + ": " + e.message }

var (
	errTenantRequired = &membershipError{http.StatusConflict, "tenant_required",
		"you belong to more than one account; name one with the Kiff-Tenant header"}
	errNotMember = &membershipError{http.StatusForbidden, "not_a_member",
		"you are not a member of this account"}
	errMembershipSuspended = &membershipError{http.StatusForbidden, "membership_suspended",
		"your access to this account is suspended; an admin of the account can restore it"}
	errMembershipRemoved = &membershipError{http.StatusForbidden, "membership_removed",
		"you were removed from this account"}
)

func (m *Middleware) refuseMembership(w http.ResponseWriter, r *http.Request, err *membershipError) {
	m.logger.InfoContext(r.Context(), "auth: membership refused",
		"path", r.URL.Path, "method", r.Method, "code", err.code)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(err.status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": err.code, "message": err.message})
}

// resolveSession fixes the tenant for a dashboard session and loads the
// membership in it (M13): the Kiff-Tenant header selects among the
// person's live memberships; with no header, a single active membership
// is used and several answer tenant_required. A person with no live
// membership falls through to the resolver (first sign-in provisions
// their tenant), and becomes its founding admin only if it has no
// members yet.
func (m *Middleware) resolveSession(ctx context.Context, r *http.Request, id Identity) (string, membership.Member, actor.Actor, error) {
	live, err := m.members.ForSubject(ctx, id.Subject)
	if err != nil {
		return "", membership.Member{}, actor.Actor{}, errors.Join(ErrResolverUnavailable, err)
	}
	pick := func(mem membership.Member) (string, membership.Member, actor.Actor, error) {
		switch mem.Status {
		case membership.StatusActive:
			return mem.TenantID, mem, humanActor(id), nil
		case membership.StatusSuspended:
			return "", membership.Member{}, actor.Actor{}, errMembershipSuspended
		}
		return "", membership.Member{}, actor.Actor{}, errMembershipRemoved
	}
	if sel := strings.TrimSpace(r.Header.Get(TenantHeader)); sel != "" {
		for _, mem := range live {
			if mem.TenantID == sel {
				return pick(mem)
			}
		}
		return "", membership.Member{}, actor.Actor{}, errNotMember
	}
	var active []membership.Member
	suspended := false
	for _, mem := range live {
		if mem.Active() {
			active = append(active, mem)
		} else {
			suspended = true
		}
	}
	switch {
	case len(active) == 1:
		return pick(active[0])
	case len(active) > 1:
		return "", membership.Member{}, actor.Actor{}, errTenantRequired
	case suspended:
		return "", membership.Member{}, actor.Actor{}, errMembershipSuspended
	}
	// No live membership. Someone removed from an account they joined by
	// invitation has no home tenant, and the resolver would provision a
	// fresh one; they are told they were removed instead.
	// Someone removed from their own home account reaches
	// the same answer through it below.
	if removed, err := m.members.HasRemoved(ctx, id.Subject); err != nil {
		return "", membership.Member{}, actor.Actor{}, errors.Join(ErrResolverUnavailable, err)
	} else if removed {
		return "", membership.Member{}, actor.Actor{}, errMembershipRemoved
	}
	// First sign-in (provision), or a tenant from before memberships
	// existed.
	resolved, tenantID, err := m.resolver.Resolve(ctx, id)
	if err != nil {
		return "", membership.Member{}, actor.Actor{}, err
	}
	mem, err := m.members.EnsureFounder(ctx, tenantID, id.Subject)
	if errors.Is(err, membership.ErrNotMember) {
		if latest, lerr := m.members.Latest(ctx, tenantID, id.Subject); lerr == nil && latest.Status == membership.StatusRemoved {
			return "", membership.Member{}, actor.Actor{}, errMembershipRemoved
		}
		return "", membership.Member{}, actor.Actor{}, errNotMember
	}
	if err != nil {
		return "", membership.Member{}, actor.Actor{}, errors.Join(ErrResolverUnavailable, err)
	}
	if !mem.Active() {
		return pick(mem)
	}
	return tenantID, mem, resolved, nil
}

// resolveDeveloper checks a developer session against its minter's
// membership in the session's tenant on every request (M4): suspended
// stops it, reactivation restores it, removal ends it.
func (m *Middleware) resolveDeveloper(ctx context.Context, id Identity) (membership.Member, error) {
	mem, err := m.members.EnsureFounder(ctx, id.Tenant, id.Subject)
	if errors.Is(err, membership.ErrNotMember) {
		return membership.Member{}, errMembershipRemoved
	}
	if err != nil {
		return membership.Member{}, errors.Join(ErrResolverUnavailable, err)
	}
	if mem.Status == membership.StatusSuspended {
		return membership.Member{}, errMembershipSuspended
	}
	return mem, nil
}

// checkAccountKey enforces M15: an account key (not bound to an agent,
// holding an account role) acts with no more than its minter's current
// role, so it is refused once the minter is suspended, removed or no
// longer an admin. A connector key follows the same rule: only an admin
// mints one. A minter that never had a membership in the tenant is
// a KIFF operator (kiff-tenant mint-key, Subject "ops"); those keys keep
// their roles under the operator audit trail.
func (m *Middleware) checkAccountKey(ctx context.Context, id Identity) error {
	if id.AgentID != "" || !(hasAccountRole(id.Roles) || id.IsConnector()) {
		return nil
	}
	mem, err := m.members.Latest(ctx, id.Tenant, id.Subject)
	if errors.Is(err, membership.ErrNotFound) {
		return nil
	}
	if err != nil {
		return errors.Join(ErrResolverUnavailable, err)
	}
	if mem.Active() && mem.Role == membership.RoleAdmin {
		return nil
	}
	return errors.New("auth: the person who minted this account key is no longer an active admin of the account")
}

// humanActor is the actor a person's request is attributed to.
func humanActor(id Identity) actor.Actor {
	a := actor.Actor{
		ID:          "actor-" + id.Subject,
		Type:        actor.TypeHuman,
		DisplayName: defaultDisplayName(id),
		Roles:       append([]string(nil), id.Roles...),
	}
	if id.Wallet != nil {
		a.DisplayName = annotateDisplay(a.DisplayName, id.Wallet.Address)
	}
	return a
}

// StepUpWindow is how recent an authentication must be for the actions
// that need a recent sign-in (step-up authentication).
const StepUpWindow = 10 * time.Minute

// ErrReauthRequired: the action needs a recent sign-in.
var ErrReauthRequired = errors.New("auth: this action needs a recent sign-in")

// StepUp checks that a person authenticated recently enough for a
// sensitive change. It reads the session token's factor-verification
// age (Clerk's fva claim). When the claim is absent it falls back to the
// Clerk session's own creation time: the action is allowed only if the
// person signed in within the window. Anything else refuses; there is no
// third path.
type StepUp struct {
	Window time.Duration
	// SessionCreatedAt looks up when a Clerk session was created (the
	// fallback). Nil: no fallback, so a token without fva refuses.
	SessionCreatedAt func(ctx context.Context, sessionID string) (time.Time, error)
	// TrustAll accepts every session: the stub provider used in local
	// runs has no sign-in to repeat. Never set with Clerk.
	TrustAll bool
	Now      func() time.Time
}

// Check returns the age, in seconds, of the authentication the action
// rests on, or ErrReauthRequired. Keys and developer sessions never pass:
// they carry no sign-in to repeat.
func (s *StepUp) Check(ctx context.Context, id Identity) (int, error) {
	if s == nil {
		return 0, nil
	}
	if id.Method != MethodSession || id.APIKey {
		return 0, ErrReauthRequired
	}
	if s.TrustAll {
		return 0, nil
	}
	window := s.Window
	if window <= 0 {
		window = StepUpWindow
	}
	if id.FactorAge != nil {
		if *id.FactorAge <= window {
			return int(id.FactorAge.Seconds()), nil
		}
		return 0, ErrReauthRequired
	}
	sid := strings.TrimSpace(id.Metadata["clerk_session_id"])
	if s.SessionCreatedAt == nil || sid == "" {
		return 0, ErrReauthRequired
	}
	created, err := s.SessionCreatedAt(ctx, sid)
	if err != nil || created.IsZero() {
		return 0, ErrReauthRequired
	}
	now := time.Now()
	if s.Now != nil {
		now = s.Now()
	}
	age := now.Sub(created)
	if age < 0 || age > window {
		return 0, ErrReauthRequired
	}
	return int(age.Seconds()), nil
}

// WriteReauthRequired renders the step-up refusal the dashboard turns
// into a re-sign-in prompt.
func WriteReauthRequired(w http.ResponseWriter) {
	writeJSON(w, http.StatusForbidden, map[string]string{
		"error":   "reauth_required",
		"message": "this change needs a recent sign-in; sign in again and retry within ten minutes",
	})
}

// serveWithMemberships is Wrap's membership path. The order is fixed:
// the tenant is settled first (the key's or developer session's record,
// or the session's selected membership), then the membership in that
// tenant, then the role it carries.
func (m *Middleware) serveWithMemberships(w http.ResponseWriter, r *http.Request, id Identity, next http.Handler) {
	ctx := r.Context()
	var resolved actor.Actor
	fail := func(err error) {
		var me *membershipError
		switch {
		case errors.As(err, &me):
			m.refuseMembership(w, r, me)
		case errors.Is(err, ErrResolverUnavailable):
			m.unavailable(w, r, err)
		default:
			m.unauthorized(w, r, err)
		}
	}
	switch {
	case id.APIKey:
		if maybe, _, err := m.resolver.Resolve(ctx, id); err == nil {
			resolved = maybe
		} else {
			resolved = actor.Actor{ID: "actor-" + id.Subject, Type: actor.TypeService,
				DisplayName: id.Subject, Roles: append([]string(nil), id.Roles...)}
		}
		if err := m.checkAccountKey(ctx, id); err != nil {
			fail(err)
			return
		}
	case id.Method == MethodDeveloper:
		mem, err := m.resolveDeveloper(ctx, id)
		if err != nil {
			fail(err)
			return
		}
		id.MemberRole, id.Roles = mem.Role, membership.IdentityRoles(mem.Role)
		resolved = humanActor(id)
	default:
		tenantID, mem, a, err := m.resolveSession(ctx, r, id)
		if err != nil {
			fail(err)
			return
		}
		id.Tenant = tenantID
		id.MemberRole, id.Roles = mem.Role, membership.IdentityRoles(mem.Role)
		resolved = a
	}
	if id.MemberRole != "" {
		resolved.Roles = append([]string(nil), id.Roles...)
	}
	ctx = WithIdentity(ctx, id)
	ctx = WithActor(ctx, resolved)
	next.ServeHTTP(w, r.WithContext(ctx))
}
