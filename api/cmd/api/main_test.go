package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kiff/kiff/pkg/kiff/actor"
	kiffruntime "github.com/kiff/kiff/pkg/kiff/runtime"

	"github.com/kiff/cloud/api/internal/anchor"
	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/domain"
	"github.com/kiff/cloud/api/internal/executors"
	"github.com/kiff/cloud/api/internal/receipt"
	"github.com/kiff/cloud/api/internal/requestlog"
	cloudruntime "github.com/kiff/cloud/api/internal/runtime"
	"github.com/kiff/cloud/api/internal/tenant"
)

// Test_NewRouter_HealthIsPublic asserts the health endpoint stays
// outside the auth middleware. The load balancer hits /healthz
// without credentials; if this returns anything other than 200 the
// instance gets removed from rotation.
func Test_NewRouter_HealthIsPublic(t *testing.T) {
	t.Parallel()

	handler := buildTestRouter(t, nil)

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("GET /healthz: got %d, want %d (body=%q)", rr.Code, http.StatusOK, rr.Body.String())
	}
}

// Test_NewRouter_RobotsDisallowsAll asserts crawlers are told to keep
// off api.kiff.dev, without credentials.
func Test_NewRouter_RobotsDisallowsAll(t *testing.T) {
	t.Parallel()

	handler := buildTestRouter(t, nil)

	req := httptest.NewRequest(http.MethodGet, "/robots.txt", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK || rr.Body.String() != "User-agent: *\nDisallow: /\n" {
		t.Fatalf("GET /robots.txt: got %d %q", rr.Code, rr.Body.String())
	}
}

// Test_NewRouter_V1RequiresAuth asserts every /v1 route returns
// 401 without an Authorization header. The auth middleware is the
// boundary; per-route authorization comes later.
func Test_NewRouter_V1RequiresAuth(t *testing.T) {
	t.Parallel()

	handler := buildTestRouter(t, nil)

	cases := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/v1/keys"},
		{http.MethodPost, "/v1/keys"},
		{http.MethodGet, "/v1/me/wallet"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			t.Parallel()
			req := httptest.NewRequest(tc.method, tc.path, nil)
			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, req)
			if rr.Code != http.StatusUnauthorized {
				t.Fatalf("got %d, want 401 (body=%q)", rr.Code, rr.Body.String())
			}
			if got := rr.Header().Get("WWW-Authenticate"); !strings.Contains(got, "Bearer") {
				t.Fatalf("missing WWW-Authenticate Bearer challenge, got %q", got)
			}
		})
	}
}

// Test_NewRouter_V1UnknownSubjectIs401 exercises the stub provider
// path with a bearer token whose subject is not registered. The
// middleware should emit 401, not 5xx, and not reach the handler.
func Test_NewRouter_V1UnknownSubjectIs401(t *testing.T) {
	t.Parallel()

	handler := buildTestRouter(t, nil)

	req := httptest.NewRequest(http.MethodGet, "/v1/keys", nil)
	req.Header.Set("Authorization", "Bearer ghost-user")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("got %d, want 401 (body=%q)", rr.Code, rr.Body.String())
	}
}

// Test_NewRouter_V1KnownSubjectListsKeys exercises the happy path:
// a fixture subject seeded into the MapResolver authenticates,
// hits GET /v1/keys, gets a 200 with a JSON array. This is the
// minimum proof that the middleware → resolver → handler chain is
// wired end-to-end.
func Test_NewRouter_V1KnownSubjectListsKeys(t *testing.T) {
	t.Parallel()

	resolver := auth.NewMapResolver()
	resolver.Set("dev-user", actor.Actor{
		ID:          "actor-dev-user",
		Type:        actor.TypeHuman,
		DisplayName: "dev-user",
		Roles:       []string{"admin"},
	}, "demo-tenant")

	handler := buildTestRouter(t, resolver)

	req := httptest.NewRequest(http.MethodGet, "/v1/keys", nil)
	req.Header.Set("Authorization", "Bearer dev-user")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("got %d, want 200 (body=%q)", rr.Code, rr.Body.String())
	}
	body, _ := io.ReadAll(rr.Body)
	if !strings.Contains(string(body), "[") {
		t.Fatalf("expected JSON array body, got %q", string(body))
	}
}

// Test_NewRouter_V1ReceiptsEndpointReachable confirms the receipts
// handler is wired into the v1 sub-mux when a receiptStore is
// provided. The receipt does not exist in the fresh store, so the
// expected outcome is 404 — but the request reaches the handler
// (404 from the receipt store) rather than 401 from the middleware
// or 404 from the mux. Distinguishing the two is the point of the
// test.
func Test_NewRouter_V1ReceiptsEndpointReachable(t *testing.T) {
	t.Parallel()

	resolver := auth.NewMapResolver()
	resolver.Set("dev-user", actor.Actor{
		ID:          "actor-dev-user",
		Type:        actor.TypeHuman,
		DisplayName: "dev-user",
		Roles:       []string{"admin"},
	}, "demo-tenant")

	handler := newRouter(routerDeps{
		provider:         auth.NewStubProvider(resolver),
		apiKeyStore:      auth.NewMemoryAPIKeyStore(),
		walletNonceStore: auth.NewMemoryWalletNonceStore(),
		walletStore:      auth.NewMemoryWalletStore(),
		receiptStore:     receipt.NewMemoryStore(),
		resolver:         resolver,
		logger:           slog.New(slog.NewTextHandler(io.Discard, nil)),
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/receipts/trace-missing", nil)
	req.Header.Set("Authorization", "Bearer dev-user")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("got %d, want 404 (body=%q)", rr.Code, rr.Body.String())
	}

	// Body should be the receipt handler's JSON shape, not the
	// middleware's. Sanity-check the shape.
	body, _ := io.ReadAll(rr.Body)
	if !strings.Contains(string(body), `"error"`) {
		t.Fatalf("expected JSON error body, got %q", string(body))
	}
}

// buildTestRouter constructs the router with in-memory stores and
// the stub provider. When resolver is nil, callers get an empty
// MapResolver (any bearer token is rejected).
func buildTestRouter(t *testing.T, resolver *auth.MapResolver) http.Handler {
	t.Helper()
	if resolver == nil {
		resolver = auth.NewMapResolver()
	}
	return newRouter(routerDeps{
		provider:         auth.NewStubProvider(resolver),
		apiKeyStore:      auth.NewMemoryAPIKeyStore(),
		walletNonceStore: auth.NewMemoryWalletNonceStore(),
		walletStore:      auth.NewMemoryWalletStore(),
		resolver:         resolver,
		logger:           slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
}

// ─── Runtime surface ─────────────────────────────────

// Test_NewRouter_RuntimeRequiresAuth asserts that the framework
// surface (events, entities, approvals) is gated by the same auth
// middleware as the rest of /v1.
func Test_NewRouter_RuntimeRequiresAuth(t *testing.T) {
	t.Parallel()

	handler := buildTestRouterWithRuntime(t, nil, nil, nil)

	cases := []struct {
		method string
		path   string
	}{
		{http.MethodPost, "/v1/events/raw"},
		{http.MethodGet, "/v1/entities/some-id/timeline"},
		{http.MethodGet, "/v1/entities/some-id/approvals"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			t.Parallel()
			req := httptest.NewRequest(tc.method, tc.path, nil)
			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, req)
			if rr.Code != http.StatusUnauthorized {
				t.Fatalf("got %d, want 401", rr.Code)
			}
		})
	}
}

// Test_NewRouter_RuntimeRoundtrip drives the runtime end-to-end
// through HTTP: ingest an event, then read the entity's timeline
// back. This is its integration proof: the framework
// surface is reachable behind the cloud's auth boundary, with the
// tenant resolved from the credential.
func Test_NewRouter_RuntimeRoundtrip(t *testing.T) {
	t.Parallel()

	const tenantID = "01h0testtenant0000000000ab"
	const yaml = `
domain: refund
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

actions: []

permissions:
  roles:
    admin: []
`

	tenants := tenant.NewMemoryStore()
	if err := tenants.Create(context.Background(), tenant.Tenant{
		ID:          tenantID,
		Slug:        "test-tenant",
		DisplayName: "Test Tenant",
		Status:      tenant.StatusActive,
	}); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}

	source := domain.NewMemorySource()
	if err := source.Save(context.Background(), tenantID, []byte(yaml)); err != nil {
		t.Fatalf("save yaml: %v", err)
	}

	resolver := auth.NewMapResolver()
	resolver.Set("dev-user", actor.Actor{
		ID:          "actor-dev",
		Type:        actor.TypeHuman,
		DisplayName: "dev",
		Roles:       []string{"admin"},
	}, tenantID)

	handler := buildTestRouterWithRuntime(t, resolver, tenants, source)

	// 1) ingest an ORDER_PLACED event for entity order-1.
	body := `{
        "id":"evt-1",
        "adapter":"cloud",
        "type":"ORDER_PLACED",
        "source":"test",
        "received_at":"2026-05-23T12:00:00Z",
        "entity_id":"order-1",
        "entity_type":"Order",
        "actor_id":"actor-dev",
        "payload":{}
    }`
	req := httptest.NewRequest(http.MethodPost, "/v1/events/raw", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer dev-user")
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK && rr.Code != http.StatusCreated {
		t.Fatalf("POST /v1/events/raw: got %d, want 2xx (body=%q)", rr.Code, rr.Body.String())
	}

	// 2) read the timeline for order-1 — the event should be there.
	req2 := httptest.NewRequest(http.MethodGet, "/v1/entities/order-1/timeline", nil)
	req2.Header.Set("Authorization", "Bearer dev-user")
	rr2 := httptest.NewRecorder()
	handler.ServeHTTP(rr2, req2)
	if rr2.Code != http.StatusOK {
		t.Fatalf("GET timeline: got %d, want 200 (body=%q)", rr2.Code, rr2.Body.String())
	}
	if !strings.Contains(rr2.Body.String(), "ORDER_PLACED") {
		t.Fatalf("timeline missing ORDER_PLACED; body=%q", rr2.Body.String())
	}
}

// Test_NewRouter_RuntimeRejectsEmptyTenant asserts the defence-in
// -depth path: when an authenticated identity somehow lands with
// no tenant set, the runtime handler refuses with 401 rather than
// crashing or leaking another tenant's data.
func Test_NewRouter_RuntimeRejectsEmptyTenant(t *testing.T) {
	t.Parallel()

	resolver := auth.NewMapResolver()
	// Empty-string tenant — the resolver explicitly maps to "".
	resolver.Set("ghost", actor.Actor{ID: "ghost-actor"}, "")

	handler := buildTestRouterWithRuntime(t, resolver, tenant.NewMemoryStore(), domain.NewMemorySource())

	req := httptest.NewRequest(http.MethodPost, "/v1/events/raw", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer ghost")
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("got %d, want 401 (body=%q)", rr.Code, rr.Body.String())
	}
}

// buildTestRouterWithRuntime constructs the test router with the
// tenant runtime wired. When tenants/source are nil the runtime
// routes are still mounted but resolution will fail; tests that
// only assert auth gating can pass nil.
func buildTestRouterWithRuntime(t *testing.T, resolver *auth.MapResolver, tenants tenant.Store, source domain.Source) http.Handler {
	t.Helper()
	if resolver == nil {
		resolver = auth.NewMapResolver()
	}
	if tenants == nil {
		tenants = tenant.NewMemoryStore()
	}
	if source == nil {
		source = domain.NewMemorySource()
	}
	registry := executors.DefaultRegistry()
	memResolver := cloudruntime.NewMemoryResolver(tenants, source, registry)
	tenantRuntime := cloudruntime.New(memResolver)

	return newRouter(routerDeps{
		provider:         auth.NewStubProvider(resolver),
		apiKeyStore:      auth.NewMemoryAPIKeyStore(),
		walletNonceStore: auth.NewMemoryWalletNonceStore(),
		walletStore:      auth.NewMemoryWalletStore(),
		resolver:         resolver,
		tenantRuntime:    tenantRuntime,
		logger:           slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
}

// Test_NewRouter_PostExecuteHook_BuildsAndPersistsReceipt is the
// end-to-end proof for the post-execute hook. A real runtime is wired with a
// minimal refund domain that has one executable action; the test
// posts an event that drives an action_executed audit record, and
// asserts that the post-execute hook builds + persists a receipt
// retrievable via GET /v1/receipts/{trace_id}.
//
// The test uses receipt.NewLocalAttester so it does not depend on
// chain access. The Sepolia anchor path is exercised separately in
// internal/receipt/anchor_attester_test.go.
func Test_NewRouter_PostExecuteHook_BuildsAndPersistsReceipt(t *testing.T) {
	t.Parallel()

	const tenantID = "01h0testtenant0000000087a8"
	const yaml = `
domain: refund
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

	tenants := tenant.NewMemoryStore()
	if err := tenants.Create(context.Background(), tenant.Tenant{
		ID:          tenantID,
		Slug:        "receipt-test-tenant",
		DisplayName: "Receipt Test Tenant",
		Status:      tenant.StatusActive,
	}); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}

	source := domain.NewMemorySource()
	if err := source.Save(context.Background(), tenantID, []byte(yaml)); err != nil {
		t.Fatalf("save yaml: %v", err)
	}

	resolver := auth.NewMapResolver()
	resolver.Set("dev-user", actor.Actor{
		ID:          "actor-dev",
		Type:        actor.TypeHuman,
		DisplayName: "dev",
		Roles:       []string{"admin"},
	}, tenantID)

	registry := executors.DefaultRegistry()
	memResolver := cloudruntime.NewMemoryResolver(tenants, source, registry)
	tenantRuntime := cloudruntime.New(memResolver)

	receiptStore := receipt.NewMemoryStore()
	wallet, err := anchor.NewRandomWallet()
	if err != nil {
		t.Fatalf("wallet: %v", err)
	}
	pipeline, err := receipt.NewPipeline(receiptStore, receipt.NewLocalAttester(wallet, nil))
	if err != nil {
		t.Fatalf("pipeline: %v", err)
	}

	hook := func(ctx context.Context, tid string, rt *kiffruntime.Runtime) {
		if _, herr := pipeline.ReconcileTenant(ctx, tid, rt); herr != nil {
			t.Errorf("hook reconcile error: %v", herr)
		}
	}
	handler := newRouter(routerDeps{
		provider:         auth.NewStubProvider(resolver),
		apiKeyStore:      auth.NewMemoryAPIKeyStore(),
		walletNonceStore: auth.NewMemoryWalletNonceStore(),
		walletStore:      auth.NewMemoryWalletStore(),
		receiptStore:     receiptStore,
		resolver:         resolver,
		tenantRuntime:    tenantRuntime,
		postExecuteHook:  hook,
		logger:           slog.New(slog.NewTextHandler(io.Discard, nil)),
	})

	// 1) ingest ORDER_PLACED so the entity exists in CREATED state.
	body := `{
        "id":"evt-1",
        "adapter":"cloud",
        "type":"ORDER_PLACED",
        "source":"test",
        "received_at":"2026-05-23T12:00:00Z",
        "entity_id":"order-87",
        "entity_type":"Order",
        "actor_id":"actor-dev",
        "metadata":{"trace_id":"trace-87"},
        "payload":{}
    }`
	req := httptest.NewRequest(http.MethodPost, "/v1/events/raw", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer dev-user")
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusCreated && rr.Code != http.StatusOK {
		t.Fatalf("ingest event: %d (body=%q)", rr.Code, rr.Body.String())
	}

	// 2) execute MARK_PAID against the entity. This produces the
	// action_executed audit record the receipt builder needs. The
	// framework's path shape is /entities/{id}/actions/{name}/execute.
	execBody := `{
        "entity_type":"Order",
        "actor":{"id":"actor-dev","type":"human","display_name":"dev","roles":["admin"]},
        "parameters":{"payment_id":"pay-1"}
    }`
	execReq := httptest.NewRequest(http.MethodPost, "/v1/entities/order-87/actions/MARK_PAID/execute", strings.NewReader(execBody))
	execReq.Header.Set("Authorization", "Bearer dev-user")
	execReq.Header.Set("Content-Type", "application/json")
	execRR := httptest.NewRecorder()
	handler.ServeHTTP(execRR, execReq)
	if execRR.Code != http.StatusOK {
		t.Fatalf("execute action: %d (body=%q)", execRR.Code, execRR.Body.String())
	}

	// 3) The hook is synchronous: by the time ServeHTTP returns,
	// the receipt is already persisted. No polling needed — if it's
	// not there now, the contract is broken.
	pending, lerr := receiptStore.ListUnanchored(context.Background(), tenantID)
	if lerr != nil {
		t.Fatalf("list unanchored: %v", lerr)
	}
	if len(pending) == 0 {
		t.Fatalf("receipt not in store immediately after execute returned (sync hook contract violated)")
	}
	traceID := pending[0].TraceID

	// 4) GET /v1/receipts/{trace_id} returns the receipt 200.
	getReq := httptest.NewRequest(http.MethodGet, "/v1/receipts/"+traceID, nil)
	getReq.Header.Set("Authorization", "Bearer dev-user")
	getRR := httptest.NewRecorder()
	handler.ServeHTTP(getRR, getReq)
	if getRR.Code != http.StatusOK {
		t.Fatalf("GET receipt: %d (body=%q)", getRR.Code, getRR.Body.String())
	}
	if !strings.Contains(getRR.Body.String(), `"action_name":"MARK_PAID"`) {
		t.Fatalf("receipt body missing action_name=MARK_PAID; body=%q", getRR.Body.String())
	}
}

func Test_NewRouter_BlockedExecuteHook_BuildsStateNotAllowedReceipt(t *testing.T) {
	t.Parallel()

	const tenantID = "01KSEEK5GZMQ81T5B52SZRG418"
	const yaml = `
domain: duplicate-payment
entity: Invoice

events:
  - INVOICE_CREATED
  - INVOICE_PAID

states:
  - PENDING
  - PAID

transitions:
  - on: INVOICE_CREATED
    from: ""
    to: PENDING
  - on: INVOICE_PAID
    from: PENDING
    to: PAID

actions:
  - name: PAY_INVOICE
    allowed_states: [PENDING]
    required_parameters: [amount_cents]
    required_permissions: [duplicate-payment.pay_invoice]
    risk: medium
    approval: never
    executor: cloud.proxy

permissions:
  roles:
    admin: [duplicate-payment.pay_invoice]
`

	tenants := tenant.NewMemoryStore()
	if err := tenants.Create(context.Background(), tenant.Tenant{
		ID:          tenantID,
		Slug:        "blocked-receipt-test-tenant",
		DisplayName: "Blocked Receipt Test Tenant",
		Status:      tenant.StatusActive,
	}); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}

	source := domain.NewMemorySource()
	if err := source.Save(context.Background(), tenantID, []byte(yaml)); err != nil {
		t.Fatalf("save yaml: %v", err)
	}

	resolver := auth.NewMapResolver()
	resolver.Set("dev-user", actor.Actor{
		ID:          "actor-dev",
		Type:        actor.TypeHuman,
		DisplayName: "dev",
		Roles:       []string{"admin"},
	}, tenantID)

	registry := executors.DefaultRegistry()
	memResolver := cloudruntime.NewMemoryResolver(tenants, source, registry)
	tenantRuntime := cloudruntime.New(memResolver)

	receiptStore := receipt.NewMemoryStore()
	wallet, err := anchor.NewRandomWallet()
	if err != nil {
		t.Fatalf("wallet: %v", err)
	}
	pipeline, err := receipt.NewPipeline(receiptStore, receipt.NewLocalAttester(wallet, nil))
	if err != nil {
		t.Fatalf("pipeline: %v", err)
	}

	blockedHook := func(ctx context.Context, tid string, rt *kiffruntime.Runtime) {
		if _, herr := pipeline.ReconcileBlockedStateNotAllowed(ctx, tid, rt); herr != nil {
			t.Errorf("hook blocked reconcile error: %v", herr)
		}
	}
	handler := newRouter(routerDeps{
		provider:         auth.NewStubProvider(resolver),
		apiKeyStore:      auth.NewMemoryAPIKeyStore(),
		walletNonceStore: auth.NewMemoryWalletNonceStore(),
		walletStore:      auth.NewMemoryWalletStore(),
		receiptStore:     receiptStore,
		resolver:         resolver,
		tenantRuntime:    tenantRuntime,
		blockedHook:      blockedHook,
		logger:           slog.New(slog.NewTextHandler(io.Discard, nil)),
	})

	body := `{
        "id":"evt-1",
        "adapter":"cloud",
        "type":"INVOICE_CREATED",
        "source":"test",
        "received_at":"2026-06-03T12:00:00Z",
        "entity_id":"invoice-42",
        "entity_type":"Invoice",
        "actor_id":"actor-dev",
        "metadata":{"trace_id":"trace-invoice-42"},
        "payload":{}
    }`
	req := httptest.NewRequest(http.MethodPost, "/v1/events/raw", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer dev-user")
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusCreated && rr.Code != http.StatusOK {
		t.Fatalf("ingest event: %d (body=%q)", rr.Code, rr.Body.String())
	}

	paid := `{
        "id":"evt-2",
        "adapter":"cloud",
        "type":"INVOICE_PAID",
        "source":"test",
        "received_at":"2026-06-03T12:01:00Z",
        "entity_id":"invoice-42",
        "entity_type":"Invoice",
        "actor_id":"actor-dev",
        "metadata":{"trace_id":"trace-invoice-42"},
        "payload":{}
    }`
	paidReq := httptest.NewRequest(http.MethodPost, "/v1/events/raw", strings.NewReader(paid))
	paidReq.Header.Set("Authorization", "Bearer dev-user")
	paidReq.Header.Set("Content-Type", "application/json")
	paidRR := httptest.NewRecorder()
	handler.ServeHTTP(paidRR, paidReq)
	if paidRR.Code != http.StatusCreated && paidRR.Code != http.StatusOK {
		t.Fatalf("paid event: %d (body=%q)", paidRR.Code, paidRR.Body.String())
	}

	execBody := `{
        "entity_type":"Invoice",
        "actor":{"id":"actor-dev","type":"human","display_name":"dev","roles":["admin"]},
        "parameters":{"amount_cents":4200}
    }`
	execReq := httptest.NewRequest(http.MethodPost, "/v1/entities/invoice-42/actions/PAY_INVOICE/execute", strings.NewReader(execBody))
	execReq.Header.Set("Authorization", "Bearer dev-user")
	execReq.Header.Set("Content-Type", "application/json")
	execRR := httptest.NewRecorder()
	handler.ServeHTTP(execRR, execReq)
	if execRR.Code != http.StatusBadRequest {
		t.Fatalf("execute should be state_not_allowed: %d (body=%q)", execRR.Code, execRR.Body.String())
	}
	if !strings.Contains(execRR.Body.String(), "action state not allowed") {
		t.Fatalf("execute body missing state_not_allowed detail: %q", execRR.Body.String())
	}

	receipts, err := receiptStore.List(context.Background(), tenantID, 10)
	if err != nil {
		t.Fatalf("list receipts: %v", err)
	}
	if len(receipts) != 1 {
		t.Fatalf("receipts = %d, want 1", len(receipts))
	}
	got := receipts[0]
	if got.Outcome != "blocked" {
		t.Fatalf("receipt outcome = %q, want blocked", got.Outcome)
	}
	if got.ActionName != "PAY_INVOICE" || got.EntityID != "invoice-42" {
		t.Fatalf("receipt target = %s/%s, want invoice-42/PAY_INVOICE", got.EntityID, got.ActionName)
	}
	found := false
	for _, step := range got.CausalChain {
		text := strings.ToLower(step.Kind + " " + step.Message)
		if strings.Contains(text, "state_not_allowed") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("blocked receipt causal chain missing state_not_allowed: %+v", got.CausalChain)
	}
}

// The request log names the route the real router matched, through the auth
// middleware and the route policy that copy the request: the receipt's id
// never reaches the line, and an unauthenticated call is logged too.
func Test_NewRouter_RequestLogNamesTheRoute(t *testing.T) {
	t.Parallel()
	resolver := auth.NewMapResolver()
	resolver.Set("dev-user", actor.Actor{ID: "actor-dev-user", Type: actor.TypeHuman, DisplayName: "dev-user", Roles: []string{"admin"}}, "demo-tenant")
	var buf bytes.Buffer
	handler := requestlog.Wrap(newRouter(routerDeps{
		provider:         auth.NewStubProvider(resolver),
		apiKeyStore:      auth.NewMemoryAPIKeyStore(),
		walletNonceStore: auth.NewMemoryWalletNonceStore(),
		walletStore:      auth.NewMemoryWalletStore(),
		receiptStore:     receipt.NewMemoryStore(),
		resolver:         resolver,
		logger:           slog.New(slog.NewTextHandler(io.Discard, nil)),
	}), slog.New(slog.NewJSONHandler(&buf, nil)))

	req := httptest.NewRequest(http.MethodGet, "/v1/receipts/trace-SECRET-1", nil)
	req.Header.Set("Authorization", "Bearer dev-user")
	handler.ServeHTTP(httptest.NewRecorder(), req)
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/v1/receipts/trace-SECRET-2", nil))

	out := buf.String()
	if strings.Contains(out, "SECRET") {
		t.Fatalf("a path's id reached the log: %s", out)
	}
	var got []map[string]any
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("not JSON: %q", l)
		}
		got = append(got, m)
	}
	if len(got) != 2 {
		t.Fatalf("got %d lines, want 2: %s", len(got), out)
	}
	if r, _ := got[0]["route"].(string); !strings.HasPrefix(r, "GET /v1/receipts/{") || got[0]["status"] != float64(404) {
		t.Errorf("an authenticated read: %v", got[0])
	}
	// Refused before the sub-mux: the route is the public mux's.
	if got[1]["status"] != float64(401) || got[1]["route"] == "" {
		t.Errorf("an unauthenticated read: %v", got[1])
	}
}
