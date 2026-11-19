package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kiff/kiff/pkg/kiff/actor"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/domain"
	"github.com/kiff/cloud/api/internal/executors"
	"github.com/kiff/cloud/api/internal/receipt"
	cloudruntime "github.com/kiff/cloud/api/internal/runtime"
	"github.com/kiff/cloud/api/internal/tenant"
)

// execute_role_gate_test.go is the regression guard for:
// the execute path must NOT trust roles asserted in the request body.
// The cloud rewrites actor.roles to the caller's resolved roles before
// delegating, so required_permissions on execute is load-bearing.

// gateYAML is a one-action refund domain whose MARK_PAID requires the
// orders.mark_paid permission, granted only to the "admin" role.
const gateYAML = `
domain: refund-gate
entity: Order

events:
  - ORDER_PLACED
  - ORDER_PAID

states:
  - CREATED
  - PAID

transitions:
  - on: ORDER_PLACED
    from: ""
    to: CREATED
  - on: ORDER_PAID
    from: CREATED
    to: PAID

actions:
  - name: MARK_PAID
    allowed_states: [CREATED]
    required_parameters: [payment_id]
    required_permissions: [orders.mark_paid]
    risk: low
    approval: never
    executor: refund.mark_paid

permissions:
  roles:
    admin: [orders.mark_paid]
`

// buildGateRouter wires the real router + runtime for a tenant whose
// dev-user subject is seeded with the given roles. Returns the handler.
func buildGateRouter(t *testing.T, callerRoles []string) http.Handler {
	t.Helper()
	const tenantID = "01HZZZGATE0000000000000000"

	tenants := tenant.NewMemoryStore()
	if err := tenants.Create(context.Background(), tenant.Tenant{
		ID:          tenantID,
		Slug:        "gate-tenant",
		DisplayName: "Gate Tenant",
		Status:      tenant.StatusActive,
	}); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	source := domain.NewMemorySource()
	if err := source.Save(context.Background(), tenantID, []byte(gateYAML)); err != nil {
		t.Fatalf("save yaml: %v", err)
	}

	resolver := auth.NewMapResolver()
	resolver.Set("dev-user", actor.Actor{
		ID:          "actor-dev",
		Type:        actor.TypeHuman,
		DisplayName: "dev",
		Roles:       callerRoles,
	}, tenantID)

	registry := executors.DefaultRegistry()
	memResolver := cloudruntime.NewMemoryResolver(tenants, source, registry)
	tenantRuntime := cloudruntime.New(memResolver)

	return newRouter(routerDeps{
		provider:         auth.NewStubProvider(resolver),
		apiKeyStore:      auth.NewMemoryAPIKeyStore(),
		walletNonceStore: auth.NewMemoryWalletNonceStore(),
		walletStore:      auth.NewMemoryWalletStore(),
		receiptStore:     receipt.NewMemoryStore(),
		resolver:         resolver,
		tenantRuntime:    tenantRuntime,
		logger:           slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
}

// ingestOrder puts the entity in CREATED so MARK_PAID is state-allowed.
func ingestOrder(t *testing.T, handler http.Handler, entityID string) {
	t.Helper()
	body := `{
        "id":"evt-` + entityID + `",
        "adapter":"cloud",
        "type":"ORDER_PLACED",
        "source":"test",
        "received_at":"2026-05-23T12:00:00Z",
        "entity_id":"` + entityID + `",
        "entity_type":"Order",
        "actor_id":"actor-dev",
        "metadata":{"trace_id":"trace-` + entityID + `"},
        "payload":{}
    }`
	req := httptest.NewRequest(http.MethodPost, "/v1/events/raw", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer dev-user")
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusCreated && rr.Code != http.StatusOK {
		t.Fatalf("ingest: %d (%s)", rr.Code, rr.Body.String())
	}
}

func execMarkPaid(t *testing.T, handler http.Handler, entityID, bodyActorRoles string) *httptest.ResponseRecorder {
	t.Helper()
	execBody := `{
        "entity_type":"Order",
        "actor":{"id":"actor-dev","type":"human","display_name":"dev"` + bodyActorRoles + `},
        "parameters":{"payment_id":"pay-1"}
    }`
	req := httptest.NewRequest(http.MethodPost, "/v1/entities/"+entityID+"/actions/MARK_PAID/execute", strings.NewReader(execBody))
	req.Header.Set("Authorization", "Bearer dev-user")
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	return rr
}

// TestExecuteGate_BodyRolesCannotEscalate is the core security
// assertion: a caller whose key holds NO roles cannot execute a
// permissioned action by asserting roles:["admin"] in the body. The
// cloud strips body roles, so the action is denied.
//
// Invariant: A caller cannot grant itself a permission by putting a
// role on the actor in the request body. The execute gate resolves
// roles from policy, never from the payload.
// Category: authority
// Attack: POST an action with actor.roles = ["approver"] in the request
// body
// Refused: The execute gate reads roles from policy, never from the
// payload, and refuses
func TestExecuteGate_BodyRolesCannotEscalate(t *testing.T) {
	t.Parallel()

	// Caller's resolved roles: none. They hold no permissions.
	handler := buildGateRouter(t, nil)
	ingestOrder(t, handler, "order-esc")

	// Body lies: claims admin.
	rr := execMarkPaid(t, handler, "order-esc", `,"roles":["admin"]`)

	if rr.Code == http.StatusOK {
		t.Fatalf("execute with body-asserted roles SUCCEEDED — self-escalation is possible (got 200; body=%s)", rr.Body.String())
	}
	if rr.Code != http.StatusForbidden {
		t.Logf("note: denied with %d (expected a permission error, not 200)", rr.Code)
	}
}

// TestExecuteGate_RealRolesAuthorize confirms the gate does not break
// legitimate callers: a key that genuinely holds the admin role can
// execute, and crucially does NOT need to send roles in the body — the
// cloud fills in the caller's resolved roles.
func TestExecuteGate_RealRolesAuthorize(t *testing.T) {
	t.Parallel()

	handler := buildGateRouter(t, []string{"admin"})
	ingestOrder(t, handler, "order-ok")

	// Body sends NO roles; the gate fills in the caller's real roles.
	rr := execMarkPaid(t, handler, "order-ok", "")

	if rr.Code != http.StatusOK {
		t.Fatalf("execute with real admin role failed: %d (body=%s)", rr.Code, rr.Body.String())
	}
}

// TestExecuteGate_InsufficientRealRolesDenied confirms a caller with a
// real-but-wrong role is denied even though the body asserts admin.
func TestExecuteGate_InsufficientRealRolesDenied(t *testing.T) {
	t.Parallel()

	// Holds "viewer" — not granted orders.mark_paid by the domain.
	handler := buildGateRouter(t, []string{"viewer"})
	ingestOrder(t, handler, "order-weak")

	rr := execMarkPaid(t, handler, "order-weak", `,"roles":["admin"]`)

	if rr.Code == http.StatusOK {
		t.Fatalf("execute SUCCEEDED for a viewer asserting admin in the body (got 200; body=%s)", rr.Body.String())
	}
}
