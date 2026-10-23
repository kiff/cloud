package domain

import (
	"context"

	kiffactor "github.com/kiff/kiff/pkg/kiff/actor"
	kiffpermission "github.com/kiff/kiff/pkg/kiff/permission"

	"github.com/kiff/cloud/api/internal/auth"
)

// ContextPolicy is the cloud's permission.Policy. It resolves an
// actor's role membership from the trusted identity on the request
// context (auth.IdentityFrom), never from the roles a caller put on the
// actor it submitted.
//
// Background: framework v0.3.0 stopped
// trusting actor.Roles in permission checks — role membership is now
// policy-owned. The cloud's per-tenant runtime (and its policy) is
// cached and shared across all requests for a tenant, so per-request
// AssignRole on a shared SimplePolicy would race and leak roles between
// concurrent callers. Instead ContextPolicy is stateless: it holds only
// the tenant's role→permission and direct actor→permission grants (from
// the YAML), and reads the *trusted* role membership per request from
// the authenticated identity on the context. No shared mutable state,
// no race, no cross-request leakage.
//
// The identity's roles are authoritative because the auth middleware
// resolves them from the API-key record or session claim and never from
// the request body. ContextPolicy applies those roles regardless of the
// actor ID on the ActionContext. The actor ID keys only direct grants; Cloud
// framework routes replace it with the authenticated identity's ActorID
// before evaluation. It never supplies role-derived authority, so a caller
// cannot borrow or self-grant another principal's roles.
type ContextPolicy struct {
	// rolePermissions maps a role name to the permissions it grants.
	rolePermissions map[string]map[kiffpermission.Permission]struct{}
	// actorPermissions maps an actor ID to permissions granted directly
	// (independent of roles). Mirrors SimplePolicy.GrantActor.
	actorPermissions map[string]map[kiffpermission.Permission]struct{}
}

// compile-time guarantee ContextPolicy satisfies the framework contract.
var _ kiffpermission.Policy = (*ContextPolicy)(nil)

// newContextPolicy builds a ContextPolicy from the YAML role grants.
func newContextPolicy(roles map[string][]string) *ContextPolicy {
	p := &ContextPolicy{
		rolePermissions:  map[string]map[kiffpermission.Permission]struct{}{},
		actorPermissions: map[string]map[kiffpermission.Permission]struct{}{},
	}
	for role, perms := range roles {
		set := make(map[kiffpermission.Permission]struct{}, len(perms))
		for _, perm := range perms {
			set[kiffpermission.Permission(perm)] = struct{}{}
		}
		p.rolePermissions[role] = set
	}
	return p
}

// GrantActor grants a permission directly to an actor ID, independent of
// roles. Kept for parity with SimplePolicy; the cloud's YAML path uses
// roles, but direct grants remain available for tests and future use.
func (p *ContextPolicy) GrantActor(actorID string, perm kiffpermission.Permission) {
	if p.actorPermissions[actorID] == nil {
		p.actorPermissions[actorID] = map[kiffpermission.Permission]struct{}{}
	}
	p.actorPermissions[actorID][perm] = struct{}{}
}

// Can reports whether the action is authorized. Direct actor grants are
// honored when they match the actor ID being checked. Role-derived
// permissions are resolved from the trusted identity on ctx — the
// authenticated principal (API key or session) whose roles the auth
// middleware resolved server-side. Roles on a.Roles are ignored
// entirely; they carry no authority.
//
// Cloud framework routes derive a.ID from the authenticated identity before
// evaluation: API-key requests use the key ID, while interactive requests use
// the session subject. The request body cannot influence either that ID or
// id.Roles, so direct and role-derived grants operate on trusted values.
func (p *ContextPolicy) Can(ctx context.Context, a kiffactor.Actor, perm kiffpermission.Permission) bool {
	if ctx.Err() != nil {
		return false
	}
	if _, ok := p.actorPermissions[a.ID][perm]; ok {
		return true
	}
	id, err := auth.IdentityFrom(ctx)
	if err != nil {
		// No authenticated identity on the context: no role authority.
		return false
	}
	for _, role := range id.Roles {
		if perms, ok := p.rolePermissions[role]; ok {
			if _, ok := perms[perm]; ok {
				return true
			}
		}
	}
	return false
}
