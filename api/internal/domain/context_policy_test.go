package domain

import (
	"context"
	"testing"

	kiffactor "github.com/kiff/kiff/pkg/kiff/actor"

	"github.com/kiff/cloud/api/internal/auth"
)

// roleGrants is the YAML-derived role→permission map the parser feeds to
// newContextPolicy.
var roleGrants = map[string][]string{
	"admin":    {"orders.refund", "orders.approve"},
	"operator": {"orders.refund"},
}

// withRoles returns a context carrying a trusted identity with the given
// resolved roles — the shape the auth middleware installs per request.
func withRoles(subject string, roles ...string) context.Context {
	return auth.WithIdentity(context.Background(), auth.Identity{
		Subject: subject,
		Method:  auth.MethodAPIKey,
		Tenant:  "t1",
		Roles:   roles,
	})
}

// The trusted identity's roles authorize; the actor-ID label on the
// ActionContext is attribution and does not gate authority.
func TestContextPolicy_AuthorizesFromTrustedIdentity(t *testing.T) {
	p := newContextPolicy(roleGrants)
	ctx := withRoles("key_123", "admin")

	// Attribution actor (e.g. the proposing agent) differs from the
	// authenticated principal; authority follows the identity.
	agent := kiffactor.Actor{ID: "ops-agent"}
	if !p.Can(ctx, agent, "orders.approve") {
		t.Fatal("admin identity should grant orders.approve")
	}
	if p.Can(ctx, agent, "orders.cancel") {
		t.Fatal("admin role does not grant orders.cancel")
	}
}

// Roles a caller puts on the actor carry no
// authority. Only the trusted identity on the context counts.
func TestContextPolicy_IgnoresCallerSuppliedActorRoles(t *testing.T) {
	p := newContextPolicy(roleGrants)

	// Identity holds only "operator"; the actor asserts "admin".
	ctx := withRoles("key_123", "operator")
	forged := kiffactor.Actor{ID: "key_123", Roles: []string{"admin"}}

	if !p.Can(ctx, forged, "orders.refund") {
		t.Fatal("operator identity should grant orders.refund")
	}
	if p.Can(ctx, forged, "orders.approve") {
		t.Fatal("caller-supplied admin role on the actor must NOT grant orders.approve")
	}
}

// With no authenticated identity on the context, role authority is
// denied (direct grants aside).
func TestContextPolicy_NoIdentityDeniesRoleAuthority(t *testing.T) {
	p := newContextPolicy(roleGrants)
	actor := kiffactor.Actor{ID: "x", Roles: []string{"admin"}}
	if p.Can(context.Background(), actor, "orders.approve") {
		t.Fatal("without a trusted identity, role authority must be denied")
	}
}

// Direct actor grants (GrantActor) are honored independent of roles.
func TestContextPolicy_DirectActorGrant(t *testing.T) {
	p := newContextPolicy(nil)
	p.GrantActor("svc-1", "orders.refund")
	// No identity needed for a direct grant.
	if !p.Can(context.Background(), kiffactor.Actor{ID: "svc-1"}, "orders.refund") {
		t.Fatal("direct actor grant should authorize")
	}
}
