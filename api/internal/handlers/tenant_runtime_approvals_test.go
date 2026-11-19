package handlers_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	kiffapproval "github.com/kiff/kiff/pkg/kiff/approval"
	kiffevent "github.com/kiff/kiff/pkg/kiff/event"
	kiffpermission "github.com/kiff/kiff/pkg/kiff/permission"
	kiffruntime "github.com/kiff/kiff/pkg/kiff/runtime"
	kiffstore "github.com/kiff/kiff/pkg/kiff/store"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/domain"
	"github.com/kiff/cloud/api/internal/executors"
	"github.com/kiff/cloud/api/internal/handlers"
	"github.com/kiff/cloud/api/internal/templates"
)

// approvalReviewYAML is the canonical M1.2a-shape domain
// used by the gate tests. tenant_owner holds both the proposing
// and the .approve permissions; ops_proposer holds only the
// proposing permission; ops_approver holds only the .approve
// permission. Three roles cover the four test cases.
const approvalReviewYAML = `
domain: refund-flow
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
    required_permissions: [refund-flow.issue_refund, refund-flow.issue_refund.approve]
    risk: high
    approval: required
    executor: cloud.proxy

permissions:
  roles:
    tenant_owner:
      - refund-flow.issue_refund
      - refund-flow.issue_refund.approve
    ops_proposer:
      - refund-flow.issue_refund
    ops_approver:
      - refund-flow.issue_refund.approve
`

// legacyReviewYAML is the earlier shape: only the proposing
// permission is declared. Used to assert the gate is a no-op on
// legacy domains — it must not break tenants whose YAML predates
// M1.2a.
const legacyReviewYAML = `
domain: refund-flow
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

// buildApprovalReviewRuntime parses yaml into a runtime, pre-records
// one pending approval for ISSUE_REFUND on entity "order-1", and
// returns the runtime + approval id ready for the gate to evaluate.
// Matches MemoryResolver's wiring so the gate sees the same
// runtime shape as production, just without Postgres.
func buildApprovalReviewRuntime(t *testing.T, yaml string) (*kiffruntime.Runtime, string) {
	t.Helper()

	rt := buildEmptyApprovalRuntime(t, yaml)

	approvalID := "appr-1"
	if err := rt.RecordApproval(context.Background(), kiffapproval.Approval{
		ID:          approvalID,
		EntityID:    "order-1",
		EntityType:  "Order",
		ActionName:  "ISSUE_REFUND",
		Status:      kiffapproval.StatusPending,
		RequestedBy: "system",
		CreatedAt:   time.Now(),
	}); err != nil {
		t.Fatalf("RecordApproval: %v", err)
	}

	return rt, approvalID
}

// buildEmptyApprovalRuntime parses yaml into a runtime without
// pre-seeding any approvals. Used by tests that need full control
// over the approval store contents (e.g. the list-approvals tests
// in that assert ordering, status filtering, and tenant
// scoping).
func buildEmptyApprovalRuntime(t *testing.T, yaml string) *kiffruntime.Runtime {
	t.Helper()

	parsed, err := domain.ParseAndBuild([]byte(yaml), executors.DefaultRegistry())
	if err != nil {
		t.Fatalf("ParseAndBuild: %v", err)
	}

	bundle := kiffstore.NewInMemoryBundle()
	rt, err := kiffruntime.NewForDomain(parsed.Definition, kiffruntime.Config{
		Stores:           &bundle,
		PermissionPolicy: parsed.Policy,
	})
	if err != nil {
		t.Fatalf("NewForDomain: %v", err)
	}
	return rt
}

// TestApprovalReview_HolderOfBothPermissions_GrantSucceeds covers the
// solo-signup case: tenant_owner holds both the
// proposing permission and its .approve counterpart. The gate
// passes; the framework's ReviewApproval runs and returns 200.
func TestApprovalReview_HolderOfBothPermissions_GrantSucceeds(t *testing.T) {
	t.Parallel()
	rt, approvalID := buildApprovalReviewRuntime(t, approvalReviewYAML)
	accessor := &stubAccessor{rt: rt, spec: specOf(t, approvalReviewYAML)}
	h := handlers.NewTenantRuntimeHandler(accessor, "/v1", nil)

	req := requestWithIdentityRoles(http.MethodPost,
		"/v1/approvals/"+approvalID+"/grant",
		[]string{"tenant_owner"})
	// The framework's grant endpoint requires a JSON body with an
	// actor; replace the helper's "{}" body with a minimal valid
	// review request.
	req.Body = readerFromString(`{"actor":{"id":"reviewer-1","kind":"human"},"reason":"approved by reviewer"}`)
	req.ContentLength = -1

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%q", rec.Code, rec.Body.String())
	}
}

// TestApprovalReview_ProposerOnly_GrantReturns403 covers the M2
// multi-user split: a principal with only the proposing
// permission must be refused on the grant path. This is the
// behavior exists to enforce.
func TestApprovalReview_ProposerOnly_GrantReturns403(t *testing.T) {
	t.Parallel()
	rt, approvalID := buildApprovalReviewRuntime(t, approvalReviewYAML)
	accessor := &stubAccessor{rt: rt, spec: specOf(t, approvalReviewYAML)}
	h := handlers.NewTenantRuntimeHandler(accessor, "/v1", nil)

	req := requestWithIdentityRoles(http.MethodPost,
		"/v1/approvals/"+approvalID+"/grant",
		[]string{"ops_proposer"})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%q", rec.Code, rec.Body.String())
	}

	var body struct {
		Error      string `json:"error"`
		Permission string `json:"permission"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v (raw=%q)", err, rec.Body.String())
	}
	if body.Error != "permission_denied" {
		t.Fatalf("error = %q, want permission_denied", body.Error)
	}
	if body.Permission != "refund-flow.issue_refund.approve" {
		t.Fatalf("permission = %q, want refund-flow.issue_refund.approve", body.Permission)
	}
}

// TestApprovalReview_ApproverOnly_GrantSucceeds covers the
// inverse: a principal that holds the .approve permission but
// not the proposing permission can still grant. The gate checks
// the approve permission specifically, not the proposing one.
// This is what makes the M2 split work without runtime changes.
func TestApprovalReview_ApproverOnly_GrantSucceeds(t *testing.T) {
	t.Parallel()
	rt, approvalID := buildApprovalReviewRuntime(t, approvalReviewYAML)
	accessor := &stubAccessor{rt: rt, spec: specOf(t, approvalReviewYAML)}
	h := handlers.NewTenantRuntimeHandler(accessor, "/v1", nil)

	req := requestWithIdentityRoles(http.MethodPost,
		"/v1/approvals/"+approvalID+"/grant",
		[]string{"ops_approver"})
	req.Body = readerFromString(`{"actor":{"id":"reviewer-2","kind":"human"},"reason":"approved by reviewer"}`)
	req.ContentLength = -1

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%q", rec.Code, rec.Body.String())
	}
	var body struct {
		Approval kiffapproval.Approval `json:"approval"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Approval.ReviewedBy != "key-test-1" {
		t.Fatalf("reviewed_by = %q, want authenticated API key id", body.Approval.ReviewedBy)
	}
}

func TestApprovalReview_DirectAPIKeyGrantSucceeds(t *testing.T) {
	t.Parallel()
	rt, approvalID := buildApprovalReviewRuntime(t, approvalReviewYAML)
	policy, ok := rt.Permissions.(*domain.ContextPolicy)
	if !ok {
		t.Fatalf("permission policy = %T, want *domain.ContextPolicy", rt.Permissions)
	}
	policy.GrantActor("key-test-1", kiffpermission.Permission("refund-flow.issue_refund.approve"))
	h := handlers.NewTenantRuntimeHandler(&stubAccessor{rt: rt, spec: specOf(t, approvalReviewYAML)}, "/v1", nil)

	req := requestWithIdentityRoles(http.MethodPost,
		"/v1/approvals/"+approvalID+"/grant", nil)
	req.Body = readerFromString(`{"actor":{"id":"caller-supplied"},"reason":"approved by runtime"}`)
	req.ContentLength = -1

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%q", rec.Code, rec.Body.String())
	}
	var body struct {
		Approval kiffapproval.Approval `json:"approval"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Approval.ReviewedBy != "key-test-1" {
		t.Fatalf("reviewed_by = %q, want directly authorized API key id", body.Approval.ReviewedBy)
	}
}

// Invariant: A caller cannot escalate its own authority by decorating
// the actor it submits. Authority is resolved from policy-owned
// membership keyed by actor id; the roles on the request body carry no
// authorization power.
// Category: authority
// Attack: Submit an approval request whose body carries actor.roles =
// ["admin"]
// Refused: Roles are resolved from policy for that actor id. The roles in
// the payload are descriptive metadata with no authorization power
func TestApprovalRequest_BodyActorCannotEscalate(t *testing.T) {
	t.Parallel()
	rt := buildEmptyApprovalRuntime(t, approvalReviewYAML)
	seedReadyForRefund(t, rt, "order-forged")
	h := handlers.NewTenantRuntimeHandler(&stubAccessor{rt: rt, spec: specOf(t, approvalReviewYAML)}, "/v1", nil)

	req := requestWithIdentityRoles(http.MethodPost,
		"/v1/entities/order-forged/actions/ISSUE_REFUND/approvals", nil)
	req.Body = readerFromString(`{
        "entity_type":"Order",
        "actor":{"id":"forged-admin","roles":["tenant_owner"]},
        "parameters":{"amount":"10","reason":"requested"},
        "approval_id":"appr-forged"
    }`)
	req.ContentLength = -1
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code == http.StatusCreated {
		t.Fatalf("approval request accepted body-asserted authority: %s", rec.Body.String())
	}
	if _, ok, err := rt.Approvals.Get(context.Background(), "appr-forged"); err != nil || ok {
		t.Fatalf("forged approval persisted: ok=%v err=%v", ok, err)
	}
}

func TestApprovalRequest_UsesAuthenticatedActor(t *testing.T) {
	t.Parallel()
	rt := buildEmptyApprovalRuntime(t, approvalReviewYAML)
	seedReadyForRefund(t, rt, "order-real")
	h := handlers.NewTenantRuntimeHandler(&stubAccessor{rt: rt, spec: specOf(t, approvalReviewYAML)}, "/v1", nil)

	req := requestWithIdentityRoles(http.MethodPost,
		"/v1/entities/order-real/actions/ISSUE_REFUND/approvals", []string{"tenant_owner"})
	req.Body = readerFromString(`{
        "entity_type":"Order",
        "actor":{"id":"forged-admin","roles":["other"]},
        "parameters":{"amount":"10","reason":"requested"},
        "approval_id":"appr-real"
    }`)
	req.ContentLength = -1
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%q", rec.Code, rec.Body.String())
	}
	stored, ok, err := rt.Approvals.Get(context.Background(), "appr-real")
	if err != nil || !ok {
		t.Fatalf("approval not stored: ok=%v err=%v", ok, err)
	}
	if stored.RequestedBy != "key-test-1" {
		t.Fatalf("requested_by = %q, want authenticated API key id", stored.RequestedBy)
	}
}

// A domain-bound key is no longer refused outright on the approval
// routes. Scoping moved to the approval store, which is
// wired per (tenant, domain) by the resolver, so the route must be
// reachable and another domain's approval must read as not-found
// rather than as a blanket 403 on the whole surface.
//
// This test asserts only that the blanket refusal is gone: the stub
// accessor here serves an in-memory framework runtime, which has no
// domain dimension to enforce. The scoping itself is covered against a
// real store in internal/approvals (cross-domain Save/Get/List/
// IsGranted), because that is where it lives.
func TestDomainBoundKey_ReachesApprovalRoutes(t *testing.T) {
	t.Parallel()
	rt, approvalID := buildApprovalReviewRuntime(t, approvalReviewYAML)
	h := handlers.NewTenantRuntimeHandler(&stubAccessor{rt: rt, spec: specOf(t, approvalReviewYAML)}, "/v1", nil)

	for _, tc := range []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/v1/entities/order-1/approvals"},
		{http.MethodPost, "/v1/approvals/" + approvalID + "/grant"},
	} {
		req := requestWithScopedIdentity(tc.method, tc.path, []string{"tenant_owner"}, "refunds")
		if tc.method == http.MethodPost {
			req.Body = readerFromString(`{"actor":{"id":"subj-1"}}`)
			req.ContentLength = -1
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code == http.StatusForbidden {
			t.Errorf("%s %s: still 403; the blanket domain-bound refusal should be gone (body=%q)",
				tc.method, tc.path, rec.Body.String())
		}
	}
}

func seedReadyForRefund(t *testing.T, rt *kiffruntime.Runtime, entityID string) {
	t.Helper()
	err := rt.IngestEvent(context.Background(), kiffevent.Event{
		ID:         "ready-" + entityID,
		Type:       "ORDER_READY_FOR_REFUND",
		EntityID:   entityID,
		EntityType: "Order",
		Source:     "test",
		ActorID:    "system",
		OccurredAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("seed ready state: %v", err)
	}
}

// TestApprovalReview_NoApproveDeclared_FailsClosed covers the
// earlier / legacy domain shape: the action requires approval but
// declares no .approve permission. The gate used to be a no-op here,
// so any key able to request the approval could also grant it.
// It now refuses both review verbs from any API key — including one
// holding the proposing permission, or even admin — until the domain
// declares a reviewer.
func TestApprovalReview_NoApproveDeclared_FailsClosed(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ verb, role string }{
		{"grant", "operator"}, {"deny", "operator"}, {"grant", "admin"}, {"grant", "tenant_owner"},
	} {
		verb := c.verb
		rt, approvalID := buildApprovalReviewRuntime(t, legacyReviewYAML)
		accessor := &stubAccessor{rt: rt, spec: specOf(t, legacyReviewYAML)}
		h := handlers.NewTenantRuntimeHandler(accessor, "/v1", nil)

		req := requestWithIdentityRoles(http.MethodPost,
			"/v1/approvals/"+approvalID+"/"+verb,
			[]string{c.role})
		req.Body = readerFromString(`{"actor":{"id":"reviewer-3","kind":"human"},"reason":"legacy"}`)
		req.ContentLength = -1

		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "reviewer_permission_undeclared") {
			t.Fatalf("%s: status = %d, want 403 reviewer_permission_undeclared; body=%q", verb, rec.Code, rec.Body.String())
		}
		got, ok, err := rt.Approvals.Get(context.Background(), approvalID)
		if err != nil || !ok || got.Status != kiffapproval.StatusPending {
			t.Fatalf("%s: approval changed despite the refusal: %+v (ok=%v err=%v)", verb, got, ok, err)
		}
	}
}

// With no reviewer declared, the signed-in tenant owner can still
// resolve the approval: a Studio-built domain declares none yet, and
// refusing the owner would leave approvals nobody can resolve.
func TestApprovalReview_NoApproveDeclared_OwnerSessionMayReview(t *testing.T) {
	t.Parallel()
	rt, approvalID := buildApprovalReviewRuntime(t, legacyReviewYAML)
	h := handlers.NewTenantRuntimeHandler(&stubAccessor{rt: rt, spec: specOf(t, legacyReviewYAML)}, "/v1", nil)

	req := httptest.NewRequest(http.MethodPost, "/v1/approvals/"+approvalID+"/grant",
		strings.NewReader(`{"actor":{"id":"owner","kind":"human"},"reason":"checked"}`))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(auth.WithIdentity(req.Context(), auth.Identity{
		Subject: "user_owner", Tenant: "t1", Method: auth.MethodSession,
		Roles: []string{"operator", "tenant_owner"},
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("owner session grant: status = %d, want 200; body=%q", rec.Code, rec.Body.String())
	}
}

// When the domain spec cannot be read, the reviewer cannot be checked,
// so review is refused rather than delegated.
func TestApprovalReview_SpecUnavailable_FailsClosed(t *testing.T) {
	t.Parallel()
	rt, approvalID := buildApprovalReviewRuntime(t, approvalReviewYAML)
	h := handlers.NewTenantRuntimeHandler(&specErrAccessor{&stubAccessor{rt: rt}}, "/v1", nil)

	req := requestWithIdentityRoles(http.MethodPost, "/v1/approvals/"+approvalID+"/grant",
		[]string{"tenant_owner"})
	req.Body = readerFromString(`{"actor":{"id":"subj-1"}}`)
	req.ContentLength = -1
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body=%q", rec.Code, rec.Body.String())
	}
}

type specErrAccessor struct{ *stubAccessor }

func (*specErrAccessor) DomainIDFor(_ context.Context, _ string, domainID string) (string, error) {
	return domainID, nil
}

func (a *specErrAccessor) SpecForDomain(context.Context, string, string) (domain.Spec, error) {
	return domain.Spec{}, errors.New("spec store unavailable")
}

// specOf is the cloud-side spec for yaml, from the same parse as its
// runtime, which is where the gate reads the reviewer permission.
func specOf(t *testing.T, yaml string) domain.Spec {
	t.Helper()
	parsed, err := domain.ParseAndBuild([]byte(yaml), executors.DefaultRegistry())
	if err != nil {
		t.Fatalf("ParseAndBuild: %v", err)
	}
	return parsed.Spec
}

// TestApprovalReview_DenyPath_AppliesSameGate covers the second
// review verb: deny. Same trust-boundary logic; same .approve
// permission required. Without it, the deny is also refused.
func TestApprovalReview_DenyPath_AppliesSameGate(t *testing.T) {
	t.Parallel()
	rt, approvalID := buildApprovalReviewRuntime(t, approvalReviewYAML)
	accessor := &stubAccessor{rt: rt, spec: specOf(t, approvalReviewYAML)}
	h := handlers.NewTenantRuntimeHandler(accessor, "/v1", nil)

	req := requestWithIdentityRoles(http.MethodPost,
		"/v1/approvals/"+approvalID+"/deny",
		[]string{"ops_proposer"})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%q", rec.Code, rec.Body.String())
	}
}

// TestApprovalReview_MissingApproval_Returns404 covers the path
// where the approval id doesn't resolve. The gate fails closed
// (404) rather than letting the request reach the framework with
// a missing record — the framework would also return an error,
// but the cloud's response is more uniform if the gate handles
// it. Tenant scoping is unaffected (the id is per-tenant by
// virtue of the runtime resolution).
func TestApprovalReview_MissingApproval_Returns404(t *testing.T) {
	t.Parallel()
	rt, _ := buildApprovalReviewRuntime(t, approvalReviewYAML)
	accessor := &stubAccessor{rt: rt, spec: specOf(t, approvalReviewYAML)}
	h := handlers.NewTenantRuntimeHandler(accessor, "/v1", nil)

	req := requestWithIdentityRoles(http.MethodPost,
		"/v1/approvals/does-not-exist/grant",
		[]string{"tenant_owner"})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%q", rec.Code, rec.Body.String())
	}
}

// readerFromString wraps a string body in an io.ReadCloser suitable
// for assignment to http.Request.Body. The framework's
// approval-review handler decodes JSON from the request body; we
// hand it a valid JSON document for the actor + reason fields.
func readerFromString(s string) io.ReadCloser {
	return io.NopCloser(strings.NewReader(s))
}

// fakeApprovalDomains maps approval ids to owning domains for the
// review-routing tests.
type fakeApprovalDomains struct {
	owners map[string]string
	err    error
}

func (f fakeApprovalDomains) DomainOf(_ context.Context, _, approvalID string) (string, bool, error) {
	if f.err != nil {
		return "", false, f.err
	}
	owner, ok := f.owners[approvalID]
	return owner, ok, nil
}

// A management identity reviewing from the tenant-wide queue addresses
// an approval by id alone. The handler must resolve the owning domain
// and route there — resolving the tenant default would fail for every
// approval belonging to any other domain.
func TestApprovalReview_ManagementRoutesToOwningDomain(t *testing.T) {
	t.Parallel()
	owning, approvalID := buildApprovalReviewRuntime(t, approvalReviewYAML)
	defaultRT := buildEmptyApprovalRuntime(t, approvalReviewYAML)

	access := newMultiTenantAccessor()
	access.spec = specOf(t, approvalReviewYAML)
	access.runtimes["t1"] = defaultRT
	access.domainRuntimes["t1/shipments"] = owning

	h := handlers.NewTenantRuntimeHandler(access, "/v1", nil).
		WithApprovalDomains(fakeApprovalDomains{owners: map[string]string{approvalID: "shipments"}})

	req := requestWithIdentityRoles(http.MethodPost, "/v1/approvals/"+approvalID+"/grant",
		[]string{"tenant_owner"})
	req.Body = readerFromString(`{"actor":{"id":"subj-1"}}`)
	req.ContentLength = -1
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	// The owning runtime holds the approval; the default one does not.
	// A 404 here means the handler routed to the default domain.
	if rec.Code == http.StatusNotFound {
		t.Fatalf("review routed to the default domain instead of the owner; body=%q", rec.Body.String())
	}
}

// A legacy approval carries no domain, so no domain runtime can review
// it. Say so explicitly rather than letting the default domain answer
// for a row it does not own.
func TestApprovalReview_LegacyUnattributedIsRefused(t *testing.T) {
	t.Parallel()
	rt, approvalID := buildApprovalReviewRuntime(t, approvalReviewYAML)
	access := newMultiTenantAccessor()
	access.runtimes["t1"] = rt

	h := handlers.NewTenantRuntimeHandler(access, "/v1", nil).
		WithApprovalDomains(fakeApprovalDomains{owners: map[string]string{approvalID: ""}})

	req := requestWithIdentityRoles(http.MethodPost, "/v1/approvals/"+approvalID+"/grant",
		[]string{"tenant_owner"})
	req.Body = readerFromString(`{"actor":{"id":"subj-1"}}`)
	req.ContentLength = -1
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 for an unattributed approval; body=%q", rec.Code, rec.Body.String())
	}
}

// acceptance on the shipped refund-agno template: the ops_agent
// role holds orders.refund, so it can request REFUND_ORDER's approval,
// but it cannot grant or deny it. admin holds the declared reviewer
// permission, orders.approve, and can.
func TestApprovalReview_RefundAgno_ProposerCannotReviewItsOwnRefund(t *testing.T) {
	t.Parallel()
	tmpl, err := templates.Get("refund-agno")
	if err != nil {
		t.Fatalf("template: %v", err)
	}
	newRT := func() (*kiffruntime.Runtime, string) {
		rt := buildEmptyApprovalRuntime(t, string(tmpl.YAML))
		if err := rt.RecordApproval(context.Background(), kiffapproval.Approval{
			ID: "appr-refund", EntityID: "order-1", EntityType: "Order", ActionName: "REFUND_ORDER",
			Status: kiffapproval.StatusPending, RequestedBy: "refund-agent", CreatedAt: time.Now(),
		}); err != nil {
			t.Fatalf("RecordApproval: %v", err)
		}
		return rt, "appr-refund"
	}
	spec := specOf(t, string(tmpl.YAML))

	for _, verb := range []string{"grant", "deny"} {
		rt, id := newRT()
		h := handlers.NewTenantRuntimeHandler(&stubAccessor{rt: rt, spec: spec}, "/v1", nil)
		req := requestWithIdentityRoles(http.MethodPost, "/v1/approvals/"+id+"/"+verb, []string{"ops_agent"})
		req.Body = readerFromString(`{"actor":{"id":"refund-agent"},"reason":"self"}`)
		req.ContentLength = -1
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "orders.approve") {
			t.Errorf("ops_agent %s: status = %d, want 403 naming orders.approve; body=%q", verb, rec.Code, rec.Body.String())
		}
	}

	rt, id := newRT()
	h := handlers.NewTenantRuntimeHandler(&stubAccessor{rt: rt, spec: spec}, "/v1", nil)
	req := requestWithIdentityRoles(http.MethodPost, "/v1/approvals/"+id+"/grant", []string{"admin"})
	req.Body = readerFromString(`{"actor":{"id":"reviewer-1","kind":"human"},"reason":"checked"}`)
	req.ContentLength = -1
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("admin grant: status = %d, want 200; body=%q", rec.Code, rec.Body.String())
	}
}

// agentKeyReview sends a review with an API key bound to agentID ("" =
// unbound) that the policy grants the action's reviewer permission.
func agentKeyReview(t *testing.T, h http.Handler, approvalID, verb, agentID string) *httptest.ResponseRecorder {
	t.Helper()
	req := requestWithIdentityRoles(http.MethodPost, "/v1/approvals/"+approvalID+"/"+verb, []string{"tenant_owner"})
	id, _ := auth.IdentityFrom(req.Context())
	id.AgentID = agentID
	req = req.WithContext(auth.WithIdentity(req.Context(), id))
	req.Body = readerFromString(`{"actor":{"id":"caller-supplied"},"reason":"reviewed"}`)
	req.ContentLength = -1
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// Invariant: an agent's own key cannot grant or deny an approval, even one it may review by role or by grant.
// Category: authority
// Attack: request an approval with an agent's key, then grant it with the same key, which holds the reviewer permission
// Refused: a key bound to an agent never reviews; a person on the account answers the approval
func TestApprovalReview_AgentKeyCannotReview(t *testing.T) {
	t.Parallel()
	rt, approvalID := buildApprovalReviewRuntime(t, approvalReviewYAML)
	policy, ok := rt.Permissions.(*domain.ContextPolicy)
	if !ok {
		t.Fatalf("permission policy = %T, want *domain.ContextPolicy", rt.Permissions)
	}
	policy.GrantActor("key-test-1", kiffpermission.Permission("refund-flow.issue_refund.approve"))
	h := handlers.NewTenantRuntimeHandler(&stubAccessor{rt: rt, spec: specOf(t, approvalReviewYAML)}, "/v1", nil)

	for _, verb := range []string{"grant", "deny"} {
		rec := agentKeyReview(t, h, approvalID, verb, "refund-agent")
		if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "review_by_agent") {
			t.Fatalf("%s with an agent key: %d %s", verb, rec.Code, rec.Body.String())
		}
	}
	got, ok, err := rt.Approvals.Get(context.Background(), approvalID)
	if err != nil || !ok || got.Status != kiffapproval.StatusPending {
		t.Fatalf("approval after refused reviews: %+v ok=%v err=%v, want pending", got, ok, err)
	}
	// The same key unbound may review: the refusal is the agent binding.
	if rec := agentKeyReview(t, h, approvalID, "grant", ""); rec.Code != http.StatusOK {
		t.Fatalf("grant with an unbound key: %d %s", rec.Code, rec.Body.String())
	}
}

// requesterStore is an approval store that reports which key asked for
// each approval, as the cloud's Postgres store does.
type requesterStore struct {
	kiffapproval.Store
	keys map[string]string
}

func (s requesterStore) RequesterKey(_ context.Context, id string) (string, error) {
	return s.keys[id], nil
}

// Invariant: a key cannot grant or deny an approval it asked for, even when it holds the reviewer permission.
// Category: authority
// Attack: ask for an approval with a key that may also review it, naming another actor as the requester, then grant it with the same key
// Refused: the key that asked is recorded with the approval and refused its review; another reviewer answers it
func TestApprovalReview_KeyCannotReviewWhatItAskedFor(t *testing.T) {
	t.Parallel()
	review := func(t *testing.T, requestedBy, requesterKey, verb string) *httptest.ResponseRecorder {
		t.Helper()
		rt := buildEmptyApprovalRuntime(t, approvalReviewYAML)
		if err := rt.RecordApproval(context.Background(), kiffapproval.Approval{
			ID: "appr-1", EntityID: "order-1", EntityType: "Order", ActionName: "ISSUE_REFUND",
			Status: kiffapproval.StatusPending, RequestedBy: requestedBy, CreatedAt: time.Now(),
		}); err != nil {
			t.Fatal(err)
		}
		rt.Approvals = requesterStore{Store: rt.Approvals, keys: map[string]string{"appr-1": requesterKey}}
		policy := rt.Permissions.(*domain.ContextPolicy)
		policy.GrantActor("key-test-1", kiffpermission.Permission("refund-flow.issue_refund.approve"))
		h := handlers.NewTenantRuntimeHandler(&stubAccessor{rt: rt, spec: specOf(t, approvalReviewYAML)}, "/v1", nil)
		req := requestWithIdentityRoles(http.MethodPost, "/v1/approvals/appr-1/"+verb, nil)
		req.Body = readerFromString(`{"actor":{"id":"caller-supplied"},"reason":"reviewed"}`)
		req.ContentLength = -1
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	for _, verb := range []string{"grant", "deny"} {
		// Asked for on a proposal: the approval names the actor the
		// caller sent, and the store recorded the key.
		if rec := review(t, "agent-billing", "key-test-1", verb); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "self_review") {
			t.Errorf("%s by the key that asked (recorded): %d %s", verb, rec.Code, rec.Body.String())
		}
		// Asked for on the framework's route, which records the key
		// itself as the requester.
		if rec := review(t, "key-test-1", "", verb); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "self_review") {
			t.Errorf("%s by the key that asked (requested_by): %d %s", verb, rec.Code, rec.Body.String())
		}
	}
	// Another key asked: this one may review.
	if rec := review(t, "agent-billing", "key-other", "grant"); rec.Code != http.StatusOK {
		t.Errorf("grant of another key's approval: %d %s", rec.Code, rec.Body.String())
	}
}
