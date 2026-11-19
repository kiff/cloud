package handlers_test

// This file proves the fix end-to-end at the handler layer:
// a self-service signup sees outcome=approval_required
// (or allowed, depending on the action's approval rule), not
// permission_denied.
//
// Two layers cover the fix:
//   - the auth middleware defaults Identity.Roles to ["operator"]
//     for session-method paths with no role attribution, see
//     auth.TestMiddleware_SessionToken_DefaultsOperatorWhenNoRoles
//     for the upstream proof;
//   - this handler-level test pins what the gate returns when
//     that defaulted Identity reaches it: a builder-shaped
//     domain whose action requires approval lands as
//     approval_required, not permission_denied.
//
// The setup mirrors what the middleware would have produced for
// a Clerk self-signup with no organization role: synthetic
// Identity carrying Roles=["operator"]. Without the fix this
// test would not exist — the previous shape (Roles: nil + a
// "blocked" assertion) is preserved in git history as the
// repro evidence cited in.

import (
	"context"
	"net/http"
	"testing"
	"time"

	kiffadapter "github.com/kiff/kiff/pkg/kiff/adapter"
	kiffevent "github.com/kiff/kiff/pkg/kiff/event"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/domain"
	"github.com/kiff/cloud/api/internal/executors"
	"github.com/kiff/cloud/api/internal/handlers"
	cloudruntime "github.com/kiff/cloud/api/internal/runtime"
	"github.com/kiff/cloud/api/internal/tenant"
)

// builderShapedYAML mirrors the canonical output of the apps/web
// guided builder for the AC's smoke-run input. Same bytes
// pinned in api/internal/domain/builder_yaml_test.go.
const builderShapedYAML = `domain: refund-flow
entity: Order

events:
  - ORDER_READY_FOR_REFUND
  - ORDER_REFUNDED

states:
  - READY_FOR_REFUND
  - REFUNDED

transitions:
  - on: ORDER_READY_FOR_REFUND
    from: ""
    to: READY_FOR_REFUND
  - on: ORDER_REFUNDED
    from: READY_FOR_REFUND
    to: REFUNDED

actions:
  - name: ISSUE_REFUND
    allowed_states: [READY_FOR_REFUND]
    required_parameters: [amount, reason]
    required_permissions: [refund-flow.issue_refund]
    risk: high
    approval: required
    executor: cloud.proxy

permissions:
  roles:
    operator:
      - refund-flow.issue_refund
`

// TestSelfServiceSignupReceivesApprovalRequired is the
// regression. A self-service signup who saved a builder-shaped
// domain submits the generated first proposal: the gate
// returns approval_required (the builder emits approval=required
// for high-risk actions), not permission_denied.
//
// The synthetic Identity carries Roles=["operator"] — that's
// what the auth middleware now defaults for session-method
// paths with empty role attribution (+ auth.middleware).
// Tests that bypass the middleware via withFakeIdentity must
// stamp the same default to exercise the production code path.
func TestSelfServiceSignupReceivesApprovalRequired(t *testing.T) {
	t.Parallel()

	// 1. Fresh tenant, in-memory store.
	tenantStore := tenant.NewMemoryStore()
	tnt := tenant.Tenant{
		ID:          tenant.NewID(),
		Slug:        "self-service-tenant",
		DisplayName: "self service",
		Status:      tenant.StatusActive,
	}
	if err := tenantStore.Create(context.Background(), tnt); err != nil {
		t.Fatalf("create tenant: %v", err)
	}

	// 2. Save the builder-shaped YAML against this tenant.
	src := domain.NewMemorySource()
	if err := src.Save(context.Background(), tnt.ID, []byte(builderShapedYAML)); err != nil {
		t.Fatalf("save builder yaml: %v", err)
	}

	// Wire up the runtime + handler the same way production does.
	resolver := cloudruntime.NewMemoryResolver(tenantStore, src, executors.DefaultRegistry())
	rt := cloudruntime.New(resolver)
	h := handlers.NewProposalsHandler(rt)
	innerMux := http.NewServeMux()
	h.Register(innerMux)

	// 3. Caller identity carrying ["operator"]. This is what the
	// auth middleware now produces for a Clerk self-signup with
	// no organization role: empty incoming Roles, defaulted to
	// ["operator"] before the proposal handler sees them. See
	// auth.TestMiddleware_SessionToken_DefaultsOperatorWhenNoRoles
	// for the upstream proof.
	wrapped := http.NewServeMux()
	wrapped.Handle("/", withFakeIdentity(innerMux, auth.Identity{
		Tenant:  tnt.ID,
		Subject: "user_self_signup",
		Roles:   []string{"operator"},
	}))

	// 4. Seed entity into READY_FOR_REFUND state.
	rtForTenant, err := rt.For(context.Background(), tnt.ID)
	if err != nil {
		t.Fatalf("resolve runtime: %v", err)
	}
	raw := kiffadapter.RawInput{
		ID:         "ORDER_READY_FOR_REFUND-order-1",
		Adapter:    "cloud",
		Type:       "ORDER_READY_FOR_REFUND",
		Source:     "test",
		ReceivedAt: time.Now().UTC(),
		EntityID:   "order-1",
		EntityType: "Order",
		ActorID:    "test",
		Metadata:   kiffevent.Metadata{TraceID: "trace-order-1"},
		Payload:    map[string]any{},
	}
	if _, err := rtForTenant.IngestRaw(context.Background(), raw); err != nil {
		t.Fatalf("ingest seed event: %v", err)
	}

	// 5. Submit the step-4 sample proposal.
	body := map[string]any{
		"entity_id":   "order-1",
		"entity_type": "Order",
		"action_name": "ISSUE_REFUND",
		"actor_id":    "onboarding",
		"parameters": map[string]any{
			"amount": "sample-amount",
			"reason": "sample-reason",
		},
	}
	rec := drivePost(t, wrapped, "/v1/proposals/decide", body)

	// Fixed behavior: the action declares approval=required and
	// the call principal has the bound role, so the gate
	// returns approval_required (NOT permission_denied).
	assertOutcome(t, rec, http.StatusOK, "approval_required", []string{"approval_required"})
}
