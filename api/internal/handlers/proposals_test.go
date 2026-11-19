package handlers_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	kiffadapter "github.com/kiff/kiff/pkg/kiff/adapter"
	kiffaudit "github.com/kiff/kiff/pkg/kiff/audit"
	kiffevent "github.com/kiff/kiff/pkg/kiff/event"
	kiffruntime "github.com/kiff/kiff/pkg/kiff/runtime"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/domain"
	"github.com/kiff/cloud/api/internal/executors"
	"github.com/kiff/cloud/api/internal/handlers"
	cloudruntime "github.com/kiff/cloud/api/internal/runtime"
	"github.com/kiff/cloud/api/internal/tenant"
	"github.com/kiff/cloud/api/internal/usage"
)

// proposalGateYAML is the fixture domain the proposal-gate tests
// run against. Three actions:
//
//   - AUTO_REFUND: low-risk, no approval, allowed in PAID state,
//     requires amount + reason parameters.
//   - REFUND_ORDER: high-risk, requires approval, same shape.
//   - GATED_REFUND: low-risk, no approval, but requires the
//     "orders.refund" permission. Used to prove the trusted-roles
//     refactor: the request body must not be able to grant it.
type proposalGateYAML = string

const proposalYAML proposalGateYAML = `
domain: refund-test
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
  - name: AUTO_REFUND
    allowed_states: [PAID]
    required_parameters: [amount, reason]
    risk: medium
    approval: never
    executor: refund.auto_refund
  - name: REFUND_ORDER
    allowed_states: [PAID]
    required_parameters: [amount, reason]
    risk: high
    approval: required
    executor: refund.issue_refund
  - name: GATED_REFUND
    allowed_states: [PAID]
    required_parameters: [amount, reason]
    required_permissions: [orders.refund]
    risk: low
    approval: never
    executor: refund.auto_refund
permissions:
  roles:
    ops_agent:
      - orders.refund
`

// proposalRig wires everything the proposal handler needs in
// tests: an in-memory tenant store seeded with one tenant, an
// in-memory domain source seeded with the YAML above, a runtime
// accessor that resolves through cloudruntime.MemoryResolver,
// and an http.ServeMux with a fake auth middleware so tests
// don't need to mint API keys.
type proposalRig struct {
	mux      http.Handler
	rt       *cloudruntime.TenantRuntime
	tenantID string
	source   *domain.MemorySource
	innerMux *http.ServeMux
}

func newProposalRig(t *testing.T) *proposalRig {
	return newProposalRigWithRoles(t, nil)
}

// newProposalRigWithRoles builds a rig where the synthetic auth
// middleware stamps an Identity carrying the given roles. The
// trusted-roles refactor pulls roles from this Identity, never
// from the request body.
func newProposalRigWithRoles(t *testing.T, roles []string) *proposalRig {
	t.Helper()
	tenantStore := tenant.NewMemoryStore()
	tnt := tenant.Tenant{
		ID:          tenant.NewID(),
		Slug:        "tenant-a",
		DisplayName: "tenant a",
		Status:      tenant.StatusActive,
	}
	if err := tenantStore.Create(context.Background(), tnt); err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	src := domain.NewMemorySource()
	if err := src.Save(context.Background(), tnt.ID, []byte(proposalYAML)); err != nil {
		t.Fatalf("seed yaml: %v", err)
	}
	resolver := cloudruntime.NewMemoryResolver(tenantStore, src, executors.DefaultRegistry())
	rt := cloudruntime.New(resolver)

	h := handlers.NewProposalsHandler(rt)
	innerMux := http.NewServeMux()
	h.Register(innerMux)

	wrapped := http.NewServeMux()
	wrapped.Handle("/", withFakeIdentity(innerMux, auth.Identity{
		Tenant:  tnt.ID,
		Subject: "test-subject",
		Roles:   append([]string(nil), roles...),
	}))

	return &proposalRig{
		mux:      wrapped,
		rt:       rt,
		tenantID: tnt.ID,
		source:   src,
		innerMux: innerMux,
	}
}

// drivePost is a tiny helper for the JSON-body POST pattern.
func drivePost(t *testing.T, mux http.Handler, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(raw)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// seedEntityInState ingests the events needed to put orderID into
// the PAID state. The proposal gate reads current state via
// rt.States, so the entity must exist before the gate decides.
func (rig *proposalRig) seedEntityInState(t *testing.T, orderID string) {
	t.Helper()
	rt, err := rig.rt.For(context.Background(), rig.tenantID)
	if err != nil {
		t.Fatalf("resolve runtime: %v", err)
	}
	for _, kind := range []string{"ORDER_PLACED", "ORDER_PAID"} {
		raw := kiffadapter.RawInput{
			ID:         kind + "-" + orderID,
			Adapter:    "cloud",
			Type:       kind,
			Source:     "test",
			ReceivedAt: time.Now().UTC(),
			EntityID:   orderID,
			EntityType: "Order",
			ActorID:    "actor-test",
			Metadata:   kiffevent.Metadata{TraceID: "trace-" + orderID},
			Payload:    map[string]any{},
		}
		if _, err := rt.IngestRaw(context.Background(), raw); err != nil {
			t.Fatalf("ingest %s: %v", kind, err)
		}
	}
}

// ─── tests ────────────────────────────────────────────────────

func TestProposals_AllowsValidProposalInPaidState(t *testing.T) {
	t.Parallel()
	rig := newProposalRig(t)
	rig.seedEntityInState(t, "order-1")

	body := map[string]any{
		"entity_id":   "order-1",
		"entity_type": "Order",
		"action_name": "AUTO_REFUND",
		"actor_id":    "agent-refunds",
		"parameters":  map[string]any{"amount": 4900, "reason": "damaged"},
	}
	rec := drivePost(t, rig.mux, "/v1/proposals/decide", body)
	assertOutcome(t, rec, http.StatusOK, "allowed", nil)
}

func TestProposals_ApprovalRequiredForHighRiskAction(t *testing.T) {
	t.Parallel()
	rig := newProposalRig(t)
	rig.seedEntityInState(t, "order-2")

	body := map[string]any{
		"entity_id":   "order-2",
		"entity_type": "Order",
		"action_name": "REFUND_ORDER",
		"actor_id":    "agent-refunds",
		"parameters":  map[string]any{"amount": 9900, "reason": "policy override requested"},
	}
	rec := drivePost(t, rig.mux, "/v1/proposals/decide", body)
	assertOutcome(t, rec, http.StatusOK, "approval_required", []string{"approval_required"})
}

func TestProposals_BlocksWhenStateNotAllowed(t *testing.T) {
	t.Parallel()
	rig := newProposalRig(t)
	// Do not seed any events; entity does not exist, so currentState
	// is empty. AUTO_REFUND requires PAID; the gate must block.
	body := map[string]any{
		"entity_id":   "order-3",
		"entity_type": "Order",
		"action_name": "AUTO_REFUND",
		"actor_id":    "agent-refunds",
		"parameters":  map[string]any{"amount": 100, "reason": "x"},
	}
	rec := drivePost(t, rig.mux, "/v1/proposals/decide", body)
	assertOutcome(t, rec, http.StatusOK, "blocked", []string{"state_not_allowed"})
}

// A proposal missing a required parameter is a malformed request, not
// a governance "no": the framework's outcome taxonomy classifies it as
// `invalid` (Slice 3), which the cloud already renders as 400 and
// treats as non-billable / non-alerting. (Previously the cloud returned
// blocked/200 for this; the reclassification aligns the cloud with the
// framework and with its own definition of `invalid` = malformed.)
func TestProposals_InvalidWhenParameterMissing(t *testing.T) {
	t.Parallel()
	rig := newProposalRig(t)
	rig.seedEntityInState(t, "order-4")

	body := map[string]any{
		"entity_id":   "order-4",
		"entity_type": "Order",
		"action_name": "AUTO_REFUND",
		"actor_id":    "agent-refunds",
		"parameters":  map[string]any{"amount": 100}, // missing reason
	}
	rec := drivePost(t, rig.mux, "/v1/proposals/decide", body)
	assertOutcome(t, rec, http.StatusBadRequest, "invalid", []string{"missing_parameter"})
}

func TestProposals_InvalidWhenActionUnknown(t *testing.T) {
	t.Parallel()
	rig := newProposalRig(t)
	rig.seedEntityInState(t, "order-5")

	body := map[string]any{
		"entity_id":   "order-5",
		"entity_type": "Order",
		"action_name": "DELETE_TENANT_DATA", // not in the YAML
		"actor_id":    "agent-evil",
		"parameters":  map[string]any{},
	}
	rec := drivePost(t, rig.mux, "/v1/proposals/decide", body)
	assertOutcome(t, rec, http.StatusBadRequest, "invalid", []string{"unknown_action"})
}

func TestProposals_InvalidWhenRequiredFieldsMissing(t *testing.T) {
	t.Parallel()
	rig := newProposalRig(t)
	body := map[string]any{
		"entity_id":   "",
		"entity_type": "",
		"action_name": "",
		"actor_id":    "",
	}
	rec := drivePost(t, rig.mux, "/v1/proposals/decide", body)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", rec.Code, rec.Body.String())
	}
	var got map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got["outcome"] != "invalid" {
		t.Fatalf("outcome = %v", got["outcome"])
	}
	reasons, _ := got["reasons"].([]any)
	if len(reasons) != 4 {
		t.Fatalf("expected 4 missing-field reasons, got %v", reasons)
	}
}

func TestProposals_RejectsMalformedJSON(t *testing.T) {
	t.Parallel()
	rig := newProposalRig(t)
	req := httptest.NewRequest(http.MethodPost, "/v1/proposals/decide", strings.NewReader("not json"))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	rig.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d", rec.Code)
	}
	var got map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got["outcome"] != "invalid" {
		t.Fatalf("outcome = %v", got["outcome"])
	}
}

func TestProposals_RequiresAuth(t *testing.T) {
	t.Parallel()
	// No auth wrapper — the handler must reject 401.
	resolver := cloudruntime.NewMemoryResolver(
		tenant.NewMemoryStore(), domain.NewMemorySource(), executors.DefaultRegistry())
	rt := cloudruntime.New(resolver)
	h := handlers.NewProposalsHandler(rt)
	mux := http.NewServeMux()
	h.Register(mux)

	rec := drivePost(t, mux, "/v1/proposals/decide", map[string]any{"entity_id": "x"})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body = %s", rec.Code, rec.Body.String())
	}
}

func TestProposals_TenantWithNoDomainGetsRuntimeUnavailable(t *testing.T) {
	t.Parallel()
	tenantStore := tenant.NewMemoryStore()
	tnt := tenant.Tenant{
		ID:     tenant.NewID(),
		Slug:   "tenant-no-domain",
		Status: tenant.StatusActive,
	}
	if err := tenantStore.Create(context.Background(), tnt); err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	src := domain.NewMemorySource() // intentionally not seeded
	resolver := cloudruntime.NewMemoryResolver(tenantStore, src, executors.DefaultRegistry())
	rt := cloudruntime.New(resolver)

	h := handlers.NewProposalsHandler(rt)
	innerMux := http.NewServeMux()
	h.Register(innerMux)
	wrapped := http.NewServeMux()
	wrapped.Handle("/", withFakeIdentity(innerMux, auth.Identity{Tenant: tnt.ID, Subject: "test"}))

	body := map[string]any{
		"entity_id":   "order-x",
		"entity_type": "Order",
		"action_name": "AUTO_REFUND",
		"actor_id":    "agent",
		"parameters":  map[string]any{"amount": 1, "reason": "x"},
	}
	rec := drivePost(t, wrapped, "/v1/proposals/decide", body)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502; body = %s", rec.Code, rec.Body.String())
	}
	var got map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got["outcome"] != "invalid" {
		t.Fatalf("outcome = %v, want invalid", got["outcome"])
	}
}

// ─── helpers ──────────────────────────────────────────────────

// assertOutcome checks the response code, outcome string, and the
// presence of expected reasons. Non-empty reasons must all appear
// in the response (order-independent).
func assertOutcome(t *testing.T, rec *httptest.ResponseRecorder, wantStatus int, wantOutcome string, wantReasons []string) {
	t.Helper()
	if rec.Code != wantStatus {
		t.Fatalf("status = %d, want %d; body = %s", rec.Code, wantStatus, rec.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v; body = %s", err, rec.Body.String())
	}
	if got["outcome"] != wantOutcome {
		t.Fatalf("outcome = %v, want %s; body = %s", got["outcome"], wantOutcome, rec.Body.String())
	}
	if len(wantReasons) == 0 {
		return
	}
	reasonsAny, _ := got["reasons"].([]any)
	gotReasons := make([]string, 0, len(reasonsAny))
	for _, v := range reasonsAny {
		if s, ok := v.(string); ok {
			gotReasons = append(gotReasons, s)
		}
	}
	for _, want := range wantReasons {
		found := false
		for _, g := range gotReasons {
			if g == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("missing reason %q; got %v", want, gotReasons)
		}
	}
}

// TestProposals_RolesComeFromIdentity_NotBody is the
// trust-boundary regression test. The reviewer flagged that
// accepting actor_roles from the request body would let a
// caller self-grant the permission that makes its own action
// allowed. Now roles come from the authenticated Identity only.
//
// Setup:
//   - GATED_REFUND requires permission orders.refund
//   - the YAML grants orders.refund to the ops_agent role
//   - rig with no roles on the identity → blocked
//   - rig with [ops_agent] roles on the identity → allowed
//   - rig with no roles on the identity AND a body that
//     "asserts" actor_roles=[ops_agent] → still blocked
//     (proves the body field cannot grant authority)
func TestProposals_RolesComeFromIdentity_NotBody(t *testing.T) {
	t.Parallel()

	body := func() map[string]any {
		return map[string]any{
			"entity_id":   "order-roles",
			"entity_type": "Order",
			"action_name": "GATED_REFUND",
			"actor_id":    "agent-refunds",
			"parameters":  map[string]any{"amount": 100, "reason": "x"},
		}
	}

	t.Run("identity has no roles → blocked", func(t *testing.T) {
		rig := newProposalRigWithRoles(t, nil)
		rig.seedEntityInState(t, "order-roles")
		rec := drivePost(t, rig.mux, "/v1/proposals/decide", body())
		assertOutcome(t, rec, http.StatusOK, "blocked", []string{"permission_denied"})
	})

	t.Run("identity has the right role → allowed", func(t *testing.T) {
		rig := newProposalRigWithRoles(t, []string{"ops_agent"})
		rig.seedEntityInState(t, "order-roles")
		rec := drivePost(t, rig.mux, "/v1/proposals/decide", body())
		assertOutcome(t, rec, http.StatusOK, "allowed", nil)
	})

	t.Run("body actor_roles cannot grant authority", func(t *testing.T) {
		rig := newProposalRigWithRoles(t, nil)
		rig.seedEntityInState(t, "order-roles")
		bad := body()
		// Whatever the caller stuffs into the body, the handler
		// must ignore it. Both the flat list and the parameters
		// stash are exercised; both must be no-ops.
		bad["actor_roles"] = []string{"ops_agent"}
		params, _ := bad["parameters"].(map[string]any)
		params["actor_roles"] = []string{"ops_agent"}
		rec := drivePost(t, rig.mux, "/v1/proposals/decide", bad)
		assertOutcome(t, rec, http.StatusOK, "blocked", []string{"permission_denied"})
	})
}

// TestProposals_AuditCapturesAuthenticatedPrincipal is the
// regression test for the audit-attribution gap flagged on PR.
// recordOutcome / recordRejection must persist the
// authenticated subject + auth method alongside the caller-
// supplied actor_id, so a reviewer can distinguish the trusted
// credential from the attribution label.
func TestProposals_AuditCapturesAuthenticatedPrincipal(t *testing.T) {
	t.Parallel()

	tenantStore := tenant.NewMemoryStore()
	tnt := tenant.Tenant{
		ID:     tenant.NewID(),
		Slug:   "tenant-audit",
		Status: tenant.StatusActive,
	}
	if err := tenantStore.Create(context.Background(), tnt); err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	src := domain.NewMemorySource()
	if err := src.Save(context.Background(), tnt.ID, []byte(proposalYAML)); err != nil {
		t.Fatalf("seed yaml: %v", err)
	}
	resolver := cloudruntime.NewMemoryResolver(tenantStore, src, executors.DefaultRegistry())
	rt := cloudruntime.New(resolver)

	h := handlers.NewProposalsHandler(rt)
	innerMux := http.NewServeMux()
	h.Register(innerMux)

	wrapped := http.NewServeMux()
	wrapped.Handle("/", withFakeIdentity(innerMux, auth.Identity{
		Tenant:   tnt.ID,
		Subject:  "principal-subject-xyz",
		APIKey:   true,
		Roles:    []string{"ops_agent"},
		Metadata: map[string]string{"api_key_id": "kid-test-001"},
	}))

	// Drive an "allowed" proposal so we get a recorded outcome.
	body := map[string]any{
		"entity_id":   "order-attr",
		"entity_type": "Order",
		"action_name": "GATED_REFUND",
		"actor_id":    "untrusted-attribution-label",
		"parameters":  map[string]any{"amount": 100, "reason": "x"},
	}
	// Seed the entity into PAID first.
	resolved, err := rt.For(context.Background(), tnt.ID)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	for _, kind := range []string{"ORDER_PLACED", "ORDER_PAID"} {
		raw := kiffadapter.RawInput{
			ID: kind + "-order-attr", Adapter: "cloud", Type: kind, Source: "test",
			ReceivedAt: time.Now().UTC(),
			EntityID:   "order-attr", EntityType: "Order",
			ActorID:  "actor-test",
			Metadata: kiffevent.Metadata{TraceID: "trace-order-attr"},
			Payload:  map[string]any{},
		}
		if _, err := resolved.IngestRaw(context.Background(), raw); err != nil {
			t.Fatalf("ingest %s: %v", kind, err)
		}
	}

	rec := drivePost(t, wrapped, "/v1/proposals/decide", body)
	assertOutcome(t, rec, http.StatusOK, "allowed", nil)

	// Pull the audit records and find the proposal_decision row.
	records, err := resolved.Audit.Query(context.Background(), kiffaudit.Filter{})
	if err != nil {
		t.Fatalf("audit query: %v", err)
	}
	var found *kiffaudit.Record
	for i := range records {
		if records[i].Kind == kiffaudit.Kind("proposal_decision") {
			found = &records[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("no proposal_decision audit record found; got %d records", len(records))
	}

	if found.Data["principal_subject"] != "principal-subject-xyz" {
		t.Errorf("principal_subject = %v, want principal-subject-xyz", found.Data["principal_subject"])
	}
	if found.Data["principal_method"] != "api_key" {
		t.Errorf("principal_method = %v, want api_key", found.Data["principal_method"])
	}
	if found.Data["principal_api_key_id"] != "kid-test-001" {
		t.Errorf("principal_api_key_id = %v, want kid-test-001", found.Data["principal_api_key_id"])
	}
	if found.Data["proposed_actor_id"] != "untrusted-attribution-label" {
		t.Errorf("proposed_actor_id = %v, want untrusted-attribution-label",
			found.Data["proposed_actor_id"])
	}
	roles, _ := found.Data["principal_roles"].([]string)
	if len(roles) != 1 || roles[0] != "ops_agent" {
		t.Errorf("principal_roles = %v, want [ops_agent]", roles)
	}
}

// stubLimiter lets tests pin the limiter response without
// dragging the usage package's plan-resolver shape into the
// handler tests.
type stubLimiter struct {
	allowed bool
	used    uint64
	limit   int
	overage bool
	err     error
}

func (s *stubLimiter) AllowProposal(_ context.Context, _ string) (bool, uint64, int, bool, error) {
	return s.allowed, s.used, s.limit, s.overage, s.err
}

// TestProposals_LimitExceeded_RejectsBeforeRuntime is the M1
// regression test for. When the limiter reports allowed=false,
// the proposal handler must return outcome=limit_exceeded with
// 429, NOT touch the runtime, and bump the limit_exceeded
// counter so the dashboard / activation tracker can show the
// upgrade trigger.
func TestProposals_LimitExceeded_RejectsBeforeRuntime(t *testing.T) {
	t.Parallel()

	// Build a rig WITHOUT seeding the entity; if the handler
	// somehow reaches the runtime evaluation it would 502
	// "tenant runtime unavailable" — a different outcome the
	// test would catch.
	tenantStore := tenant.NewMemoryStore()
	tnt := tenant.Tenant{ID: tenant.NewID(), Slug: "tenant-limit", Status: tenant.StatusActive}
	if err := tenantStore.Create(context.Background(), tnt); err != nil {
		t.Fatalf("create: %v", err)
	}
	src := domain.NewMemorySource()
	if err := src.Save(context.Background(), tnt.ID, []byte(proposalYAML)); err != nil {
		t.Fatalf("seed: %v", err)
	}
	resolver := cloudruntime.NewMemoryResolver(tenantStore, src, executors.DefaultRegistry())
	rt := cloudruntime.New(resolver)

	rec := &recordingRecorder{}
	limiter := &stubLimiter{allowed: false, used: 1000, limit: 1000}

	h := handlers.NewProposalsHandler(rt,
		handlers.WithProposalsMetrics(func(_, _ string) kiffruntime.MetricsRecorder { return rec }),
		handlers.WithProposalsLimiter(limiter),
	)
	innerMux := http.NewServeMux()
	h.Register(innerMux)
	wrapped := http.NewServeMux()
	wrapped.Handle("/", withFakeIdentity(innerMux, auth.Identity{Tenant: tnt.ID, Subject: "principal"}))

	body := map[string]any{
		"entity_id":   "order-1",
		"entity_type": "Order",
		"action_name": "AUTO_REFUND",
		"actor_id":    "agent",
		"parameters":  map[string]any{"amount": 100, "reason": "x"},
	}
	resp := drivePost(t, wrapped, "/v1/proposals/decide", body)
	if resp.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429; body = %s", resp.Code, resp.Body.String())
	}
	var got map[string]any
	_ = json.Unmarshal(resp.Body.Bytes(), &got)
	if got["outcome"] != "limit_exceeded" {
		t.Errorf("outcome = %v, want limit_exceeded", got["outcome"])
	}
	reasons, _ := got["reasons"].([]any)
	if len(reasons) == 0 || reasons[0] != "plan_limit_reached" {
		t.Errorf("reasons = %v, want [plan_limit_reached]", reasons)
	}

	// Counter side-effects: limit_exceeded bumped once;
	// submitted/allowed/blocked/etc NOT bumped (we never
	// reached the runtime).
	if got := countByName(rec.calls, usage.CloudCounterProposalsLimitExceeded); got != 1 {
		t.Errorf("limit_exceeded counter = %d, want 1", got)
	}
	if got := countByName(rec.calls, usage.CloudCounterProposalsSubmitted); got != 0 {
		t.Errorf("submitted counter = %d, want 0 (must not count rejected request toward budget)", got)
	}
}

// TestProposals_PaidOverage_EvaluatesAndRecordsOverage is the
// core at the handler boundary. When the limiter reports a paid
// tenant past its allowance (allowed=true, overage=true), the
// proposal must NOT hard-stop: it is evaluated against the contract
// (here → allowed, 200), the submitted counter fires, and the
// overage counter is bumped exactly once for billing. limit_exceeded
// must NOT fire.
func TestProposals_PaidOverage_EvaluatesAndRecordsOverage(t *testing.T) {
	t.Parallel()

	tenantStore := tenant.NewMemoryStore()
	tnt := tenant.Tenant{ID: tenant.NewID(), Slug: "tenant-overage", Status: tenant.StatusActive}
	if err := tenantStore.Create(context.Background(), tnt); err != nil {
		t.Fatalf("create: %v", err)
	}
	src := domain.NewMemorySource()
	if err := src.Save(context.Background(), tnt.ID, []byte(proposalYAML)); err != nil {
		t.Fatalf("seed: %v", err)
	}
	resolver := cloudruntime.NewMemoryResolver(tenantStore, src, executors.DefaultRegistry())
	rt := cloudruntime.New(resolver)

	// Put order-ov into the PAID state so the gate reaches a real
	// "allowed" decision rather than a shape rejection.
	resolved, err := rt.For(context.Background(), tnt.ID)
	if err != nil {
		t.Fatalf("resolve runtime: %v", err)
	}
	for _, kind := range []string{"ORDER_PLACED", "ORDER_PAID"} {
		raw := kiffadapter.RawInput{
			ID:         kind + "-order-ov",
			Adapter:    "cloud",
			Type:       kind,
			Source:     "test",
			ReceivedAt: time.Now().UTC(),
			EntityID:   "order-ov",
			EntityType: "Order",
			ActorID:    "actor-test",
			Metadata:   kiffevent.Metadata{TraceID: "trace-order-ov"},
			Payload:    map[string]any{},
		}
		if _, err := resolved.IngestRaw(context.Background(), raw); err != nil {
			t.Fatalf("ingest %s: %v", kind, err)
		}
	}

	rec := &recordingRecorder{}
	// Paid tenant 50 over a 100 allowance: allowed, but flagged
	// as overage.
	limiter := &stubLimiter{allowed: true, used: 150, limit: 100, overage: true}

	h := handlers.NewProposalsHandler(rt,
		handlers.WithProposalsMetrics(func(_, _ string) kiffruntime.MetricsRecorder { return rec }),
		handlers.WithProposalsLimiter(limiter),
	)
	innerMux := http.NewServeMux()
	h.Register(innerMux)
	wrapped := http.NewServeMux()
	wrapped.Handle("/", withFakeIdentity(innerMux, auth.Identity{
		Tenant:  tnt.ID,
		Subject: "principal",
		Roles:   []string{"ops_agent"},
	}))

	body := map[string]any{
		"entity_id":   "order-ov",
		"entity_type": "Order",
		"action_name": "AUTO_REFUND",
		"actor_id":    "agent-refunds",
		"parameters":  map[string]any{"amount": 4900, "reason": "damaged"},
	}
	resp := drivePost(t, wrapped, "/v1/proposals/decide", body)

	// Not hard-stopped: a real governance outcome, not 429.
	if resp.Code == http.StatusTooManyRequests {
		t.Fatalf("paid overage must not hard-stop; got 429: %s", resp.Body.String())
	}
	var got map[string]any
	_ = json.Unmarshal(resp.Body.Bytes(), &got)
	if got["outcome"] == "limit_exceeded" {
		t.Errorf("paid overage must not return limit_exceeded; got %v", got["outcome"])
	}
	if got["outcome"] != "allowed" {
		t.Errorf("outcome = %v, want allowed (entity is PAID)", got["outcome"])
	}

	// Counter side-effects: overage and submitted each bumped once;
	// limit_exceeded never fires for a paid plan.
	if got := countByName(rec.calls, usage.CloudCounterProposalsOverage); got != 1 {
		t.Errorf("overage counter = %d, want 1", got)
	}
	if got := countByName(rec.calls, usage.CloudCounterProposalsSubmitted); got != 1 {
		t.Errorf("submitted counter = %d, want 1 (overage proposals are evaluated)", got)
	}
	if got := countByName(rec.calls, usage.CloudCounterProposalsLimitExceeded); got != 0 {
		t.Errorf("limit_exceeded counter = %d, want 0 for a paid overage", got)
	}
}

// TestProposals_LimitExceeded_RecordsAuditAttribution proves
// the audit row carries the trusted principal even on a
// limit_exceeded rejection. Same trust-boundary contract as the
// other rejection paths.
func TestProposals_LimitExceeded_RecordsAuditAttribution(t *testing.T) {
	t.Parallel()
	tenantStore := tenant.NewMemoryStore()
	tnt := tenant.Tenant{ID: tenant.NewID(), Slug: "tenant-attr", Status: tenant.StatusActive}
	if err := tenantStore.Create(context.Background(), tnt); err != nil {
		t.Fatalf("create: %v", err)
	}
	src := domain.NewMemorySource()
	if err := src.Save(context.Background(), tnt.ID, []byte(proposalYAML)); err != nil {
		t.Fatalf("seed: %v", err)
	}
	resolver := cloudruntime.NewMemoryResolver(tenantStore, src, executors.DefaultRegistry())
	rt := cloudruntime.New(resolver)

	limiter := &stubLimiter{allowed: false, used: 1000, limit: 1000}
	h := handlers.NewProposalsHandler(rt, handlers.WithProposalsLimiter(limiter))
	innerMux := http.NewServeMux()
	h.Register(innerMux)
	wrapped := http.NewServeMux()
	wrapped.Handle("/", withFakeIdentity(innerMux, auth.Identity{
		Tenant:   tnt.ID,
		Subject:  "principal-xyz",
		APIKey:   true,
		Roles:    []string{"ops_agent"},
		Metadata: map[string]string{"api_key_id": "kid-001"},
	}))

	body := map[string]any{
		"entity_id":   "order-1",
		"entity_type": "Order",
		"action_name": "AUTO_REFUND",
		"actor_id":    "untrusted-actor-label",
		"parameters":  map[string]any{"amount": 1, "reason": "x"},
	}
	resp := drivePost(t, wrapped, "/v1/proposals/decide", body)
	if resp.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d", resp.Code)
	}
	resolved, err := rt.For(context.Background(), tnt.ID)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	records, _ := resolved.Audit.Query(context.Background(), kiffaudit.Filter{})
	var found *kiffaudit.Record
	for i := range records {
		if records[i].Kind == kiffaudit.Kind("proposal_rejected") {
			found = &records[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("no proposal_rejected audit row")
	}
	if found.Data["principal_subject"] != "principal-xyz" {
		t.Errorf("principal_subject = %v, want principal-xyz", found.Data["principal_subject"])
	}
	if found.Data["principal_api_key_id"] != "kid-001" {
		t.Errorf("principal_api_key_id = %v, want kid-001", found.Data["principal_api_key_id"])
	}
}

// TestProposals_LimitNil_AllowsEverything documents that omitting
// the limiter (eg in dev mode without a usage store) must not
// break the proposal path.
func TestProposals_LimitNil_AllowsEverything(t *testing.T) {
	t.Parallel()
	rig := newProposalRig(t) // no limiter
	rig.seedEntityInState(t, "order-nm")
	body := map[string]any{
		"entity_id":   "order-nm",
		"entity_type": "Order",
		"action_name": "AUTO_REFUND",
		"actor_id":    "agent",
		"parameters":  map[string]any{"amount": 1, "reason": "x"},
	}
	resp := drivePost(t, rig.mux, "/v1/proposals/decide", body)
	assertOutcome(t, resp, http.StatusOK, "allowed", nil)
}

// TestProposals_LimitErrorIsAllowedWhenUncertain documents the
// allow-when-uncertain contract on the handler side: a limiter
// that returns an error AND allowed=true must let the request
// through.
func TestProposals_LimitErrorIsAllowedWhenUncertain(t *testing.T) {
	t.Parallel()
	tenantStore := tenant.NewMemoryStore()
	tnt := tenant.Tenant{ID: tenant.NewID(), Slug: "tenant-err", Status: tenant.StatusActive}
	if err := tenantStore.Create(context.Background(), tnt); err != nil {
		t.Fatalf("create: %v", err)
	}
	src := domain.NewMemorySource()
	if err := src.Save(context.Background(), tnt.ID, []byte(proposalYAML)); err != nil {
		t.Fatalf("seed: %v", err)
	}
	resolver := cloudruntime.NewMemoryResolver(tenantStore, src, executors.DefaultRegistry())
	rt := cloudruntime.New(resolver)

	// Seed PAID state.
	resolved, _ := rt.For(context.Background(), tnt.ID)
	for _, kind := range []string{"ORDER_PLACED", "ORDER_PAID"} {
		raw := kiffadapter.RawInput{
			ID: kind + "-eu", Adapter: "cloud", Type: kind, Source: "test",
			ReceivedAt: time.Now().UTC(),
			EntityID:   "order-eu", EntityType: "Order", ActorID: "actor-test",
			Metadata: kiffevent.Metadata{TraceID: "trace-eu"},
			Payload:  map[string]any{},
		}
		_, _ = resolved.IngestRaw(context.Background(), raw)
	}

	limiter := &stubLimiter{allowed: true, err: errors.New("simulated db blip")}
	h := handlers.NewProposalsHandler(rt, handlers.WithProposalsLimiter(limiter))
	innerMux := http.NewServeMux()
	h.Register(innerMux)
	wrapped := http.NewServeMux()
	wrapped.Handle("/", withFakeIdentity(innerMux, auth.Identity{Tenant: tnt.ID, Subject: "principal"}))

	body := map[string]any{
		"entity_id":   "order-eu",
		"entity_type": "Order",
		"action_name": "AUTO_REFUND",
		"actor_id":    "agent",
		"parameters":  map[string]any{"amount": 1, "reason": "x"},
	}
	resp := drivePost(t, wrapped, "/v1/proposals/decide", body)
	assertOutcome(t, resp, http.StatusOK, "allowed", nil)
}

// stubMeterLimits refuses with a fixed reason, and records what it was asked.
type stubMeterLimits struct {
	reason        string
	asked         int
	agent, action string
}

func (s *stubMeterLimits) Refusal(_ context.Context, _, agent, action string) (string, string, error) {
	s.asked++
	s.agent, s.action = agent, action
	if s.reason == "" {
		return "", "", nil
	}
	return s.reason, "Nothing was sent.", nil
}

// A call that would count past the plan's tasks or actions is refused
// before it's decided, with the meter's reason.
func TestProposals_MeterLimitRefusesBeforeRuntime(t *testing.T) {
	t.Parallel()
	tenantStore := tenant.NewMemoryStore()
	tnt := tenant.Tenant{ID: tenant.NewID(), Slug: "tenant-meter", Status: tenant.StatusActive}
	if err := tenantStore.Create(context.Background(), tnt); err != nil {
		t.Fatalf("create: %v", err)
	}
	src := domain.NewMemorySource()
	if err := src.Save(context.Background(), tnt.ID, []byte(proposalYAML)); err != nil {
		t.Fatalf("seed: %v", err)
	}
	rt := cloudruntime.New(cloudruntime.NewMemoryResolver(tenantStore, src, executors.DefaultRegistry()))
	limits := &stubMeterLimits{reason: "ceiling_reached"}
	h := handlers.NewProposalsHandler(rt, handlers.WithProposalsMeterLimits(limits))
	innerMux := http.NewServeMux()
	h.Register(innerMux)
	wrapped := http.NewServeMux()
	wrapped.Handle("/", withFakeIdentity(innerMux, auth.Identity{Tenant: tnt.ID, Subject: "principal"}))

	body := map[string]any{
		"entity_id": "order-1", "entity_type": "Order", "action_name": "AUTO_REFUND", "actor_id": "taf-refunds",
		"parameters": map[string]any{"amount": 100, "reason": "x"},
	}
	resp := drivePost(t, wrapped, "/v1/proposals/decide", body)
	var got map[string]any
	_ = json.Unmarshal(resp.Body.Bytes(), &got)
	reasons, _ := got["reasons"].([]any)
	if got["outcome"] != "limit_exceeded" || len(reasons) != 1 || reasons[0] != "ceiling_reached" || got["message"] != "Nothing was sent." {
		t.Fatalf("refused: %d %s", resp.Code, resp.Body.String())
	}
	if limits.agent != "taf-refunds" || limits.action != "AUTO_REFUND" {
		t.Errorf("asked about %q %q", limits.agent, limits.action)
	}

	// Within the plan, it's decided as before.
	limits.reason = ""
	body["entity_id"] = "order-2"
	if resp := drivePost(t, wrapped, "/v1/proposals/decide", body); strings.Contains(resp.Body.String(), "limit_exceeded") {
		t.Fatalf("within the plan: %d %s", resp.Code, resp.Body.String())
	}
}
