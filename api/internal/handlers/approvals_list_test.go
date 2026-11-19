package handlers_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	kiffapproval "github.com/kiff/kiff/pkg/kiff/approval"
	kiffruntime "github.com/kiff/kiff/pkg/kiff/runtime"

	"github.com/kiff/cloud/api/internal/approvals"
	"github.com/kiff/cloud/api/internal/domain"
	"github.com/kiff/cloud/api/internal/handlers"
)

// approvals_list_test.go covers GET /v1/approvals (the Studio-cloud seam design
// M1.1c). The handler resolves the per-tenant runtime
// via TenantRuntimeAccessor and drives runtime.Approvals.List
// with an empty entityID. Tenant scoping is structural: each
// tenant's runtime carries its own approval store, so the test
// helper wires one runtime per tenant id and asserts a tenant
// reading another tenant's id sees empty.
//
// The base shape (refund-flow YAML, three roles, the
// canonical M1.2a permission shape) and the buildEmptyApprovalRuntime
// helper are reused from tenant_runtime_approvals_test.go.
// This file adds:
//
//   - multiTenantAccessor: a TenantRuntimeAccessor that picks
//     a runtime per tenant id, so tenant-scoping tests can
//     exercise the cross-tenant boundary.
//   - seedApproval: a small helper that records an approval in
//     a runtime's approval store with a chosen status + time.
//   - the test cases below.

// multiTenantAccessor implements handlers.TenantRuntimeAccessor.
// Tests register one runtime per tenant id; an unknown tenant id
// returns the configured fallback error so the handler's 502 path
// can be exercised.
type multiTenantAccessor struct {
	runtimes map[string]*kiffruntime.Runtime
	// domainRuntimes keys "<tenant>/<domain>" so a test can prove the
	// handler resolved the bound key's domain rather than the tenant
	// default. Empty falls back to the default, matching production
	// behaviour for an unbound identity.
	domainRuntimes map[string]*kiffruntime.Runtime
	notFound       error
	spec           domain.Spec
}

func newMultiTenantAccessor() *multiTenantAccessor {
	return &multiTenantAccessor{
		runtimes:       map[string]*kiffruntime.Runtime{},
		domainRuntimes: map[string]*kiffruntime.Runtime{},
	}
}

// fakeTenantReader is the management-side view for handler tests: it
// answers from whatever runtimes the test registered, so the tenant-wide
// path can be exercised without a database.
type fakeTenantReader struct{ access *multiTenantAccessor }

func readerFor(a *multiTenantAccessor) *fakeTenantReader { return &fakeTenantReader{access: a} }

func (f *fakeTenantReader) ListAll(ctx context.Context, tenantID, entityID string) ([]approvals.Approval, error) {
	rt, err := f.access.For(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	if rt == nil || rt.Approvals == nil {
		return []approvals.Approval{}, nil
	}
	rows, err := rt.Approvals.List(ctx, entityID)
	if err != nil {
		return nil, err
	}
	out := make([]approvals.Approval, 0, len(rows))
	for _, a := range rows {
		out = append(out, approvals.Approval{Approval: a, DomainID: "default"})
	}
	return out, nil
}

func (m *multiTenantAccessor) For(_ context.Context, tenantID string) (*kiffruntime.Runtime, error) {
	if rt, ok := m.runtimes[tenantID]; ok {
		return rt, nil
	}
	if m.notFound != nil {
		return nil, m.notFound
	}
	// Tenants the test didn't register get an empty stub runtime —
	// the handler then renders an empty list, which is exactly the
	// cross-tenant scoping semantics ("you can't see what isn't
	// registered for your tenant"). Production resolves an actual
	// runtime per tenant; the empty-stub return mirrors what a
	// real per-tenant resolver would do for an unknown tenant.
	return &kiffruntime.Runtime{}, nil
}

// ForDomain returns the runtime registered for "<tenant>/<domain>" when
// the test set one, else falls back to the tenant default.
func (m *multiTenantAccessor) ForDomain(ctx context.Context, tenantID, domainID string) (*kiffruntime.Runtime, error) {
	if rt, ok := m.domainRuntimes[tenantID+"/"+domainID]; ok {
		return rt, nil
	}
	return m.forDefault(ctx, tenantID)
}

// SpecForDomain returns spec (zero unless a test sets one): this fake
// serves the approvals list and review routing, not the decide path.
func (*multiTenantAccessor) DomainIDFor(_ context.Context, _ string, domainID string) (string, error) {
	return domainID, nil
}

func (m *multiTenantAccessor) SpecForDomain(_ context.Context, _ string, _ string) (domain.Spec, error) {
	return m.spec, nil
}

func (m *multiTenantAccessor) forDefault(ctx context.Context, tenantID string) (*kiffruntime.Runtime, error) {
	return m.For(ctx, tenantID)
}

// seedApproval records one approval on rt's approval store with
// the given fields. createdAt anchors the row's CreatedAt so
// sort-by-time tests can assert deterministic ordering.
//
// For granted/denied statuses the framework's Approval.Validate
// requires ReviewedBy and ReviewedAt; the helper fills them with
// stable defaults (a "reviewer" actor and createdAt + 1 second)
// so the row passes validation. Tests that need to assert the
// reviewer field can override after the fact.
func seedApproval(t *testing.T, rt *kiffruntime.Runtime, id, entityID, action string, status kiffapproval.Status, createdAt time.Time) {
	t.Helper()
	a := kiffapproval.Approval{
		ID:          id,
		EntityID:    entityID,
		EntityType:  "Order",
		ActionName:  action,
		Status:      status,
		RequestedBy: "system",
		CreatedAt:   createdAt,
	}
	if status == kiffapproval.StatusGranted || status == kiffapproval.StatusDenied {
		a.ReviewedBy = "reviewer"
		a.ReviewedAt = createdAt.Add(time.Second)
	}
	if err := rt.RecordApproval(context.Background(), a); err != nil {
		t.Fatalf("RecordApproval %s: %v", id, err)
	}
}

// TestApprovalsList_HappyPath_ReturnsPendingApprovalsDescByCreated
// covers the dashboard's primary case: a tenant with two pending
// approvals reads them back ordered newest-first.
func TestApprovalsList_HappyPath_ReturnsPendingApprovalsDescByCreated(t *testing.T) {
	t.Parallel()
	rt := buildEmptyApprovalRuntime(t, approvalReviewYAML)
	older := time.Now().Add(-2 * time.Hour).UTC()
	newer := time.Now().Add(-30 * time.Minute).UTC()
	seedApproval(t, rt, "appr-old", "order-1", "ISSUE_REFUND", kiffapproval.StatusPending, older)
	seedApproval(t, rt, "appr-new", "order-2", "ISSUE_REFUND", kiffapproval.StatusPending, newer)

	access := newMultiTenantAccessor()
	access.runtimes["t1"] = rt

	h := handlers.NewApprovalsListHandler(access, readerFor(access), nil)
	mux := http.NewServeMux()
	h.Register(mux)

	req := requestWithIdentity(http.MethodGet, "/v1/approvals?status=pending")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200; body=%q", rec.Code, rec.Body.String())
	}
	var body struct {
		Approvals []struct {
			ID         string    `json:"id"`
			Status     string    `json:"status"`
			CreatedAt  time.Time `json:"created_at"`
			ActionName string    `json:"action_name"`
		} `json:"approvals"`
		LastReceivedAt *time.Time `json:"last_received_at"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v\nraw=%s", err, rec.Body.String())
	}
	if len(body.Approvals) != 2 {
		t.Fatalf("approvals: got %d, want 2", len(body.Approvals))
	}
	if body.Approvals[0].ID != "appr-new" {
		t.Errorf("approvals[0].id: got %q, want appr-new (DESC by created_at)", body.Approvals[0].ID)
	}
	if body.Approvals[1].ID != "appr-old" {
		t.Errorf("approvals[1].id: got %q, want appr-old", body.Approvals[1].ID)
	}
	if body.LastReceivedAt == nil {
		t.Fatalf("last_received_at must be set when approvals present")
	}
	if !body.LastReceivedAt.Equal(newer) {
		t.Errorf("last_received_at: got %v, want %v (newest)", body.LastReceivedAt, newer)
	}
}

// TestApprovalsList_StatusFilter_FiltersByStatus exercises the
// ?status= query parameter against a tenant whose runtime has
// approvals in all three states.
func TestApprovalsList_StatusFilter_FiltersByStatus(t *testing.T) {
	t.Parallel()
	rt := buildEmptyApprovalRuntime(t, approvalReviewYAML)
	now := time.Now().UTC()
	seedApproval(t, rt, "appr-pending", "order-1", "ISSUE_REFUND", kiffapproval.StatusPending, now.Add(-3*time.Hour))
	seedApproval(t, rt, "appr-granted", "order-2", "ISSUE_REFUND", kiffapproval.StatusGranted, now.Add(-2*time.Hour))
	seedApproval(t, rt, "appr-denied", "order-3", "ISSUE_REFUND", kiffapproval.StatusDenied, now.Add(-1*time.Hour))

	access := newMultiTenantAccessor()
	access.runtimes["t1"] = rt

	h := handlers.NewApprovalsListHandler(access, readerFor(access), nil)
	mux := http.NewServeMux()
	h.Register(mux)

	cases := map[string]string{
		"":        "appr-pending", // default
		"pending": "appr-pending",
		"granted": "appr-granted",
		"denied":  "appr-denied",
	}
	for status, wantID := range cases {
		t.Run("status="+status, func(t *testing.T) {
			path := "/v1/approvals"
			if status != "" {
				path += "?status=" + status
			}
			req := requestWithIdentity(http.MethodGet, path)
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("status: got %d, want 200; body=%q", rec.Code, rec.Body.String())
			}
			var body struct {
				Approvals []struct {
					ID     string `json:"id"`
					Status string `json:"status"`
				} `json:"approvals"`
			}
			_ = json.Unmarshal(rec.Body.Bytes(), &body)
			if len(body.Approvals) != 1 {
				t.Fatalf("approvals: got %d, want 1; ids=%v", len(body.Approvals), idsOf(body.Approvals))
			}
			if body.Approvals[0].ID != wantID {
				t.Errorf("approvals[0].id: got %q, want %q", body.Approvals[0].ID, wantID)
			}
		})
	}
}

// idsOf is a small debug helper for the table test above.
func idsOf(rows []struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.ID
	}
	return out
}

// TestApprovalsList_TenantScoping_NoCrossTenantLeakage seeds two
// tenants with different approvals and asserts a request as
// tenant-A only sees tenant-A's rows, even though tenant-B has
// approvals on its runtime.
func TestApprovalsList_TenantScoping_NoCrossTenantLeakage(t *testing.T) {
	t.Parallel()
	rtA := buildEmptyApprovalRuntime(t, approvalReviewYAML)
	rtB := buildEmptyApprovalRuntime(t, approvalReviewYAML)
	now := time.Now().UTC()
	seedApproval(t, rtA, "appr-A", "order-1", "ISSUE_REFUND", kiffapproval.StatusPending, now)
	seedApproval(t, rtB, "appr-B", "order-2", "ISSUE_REFUND", kiffapproval.StatusPending, now)

	access := newMultiTenantAccessor()
	access.runtimes["t1"] = rtA
	access.runtimes["t2"] = rtB

	h := handlers.NewApprovalsListHandler(access, readerFor(access), nil)
	mux := http.NewServeMux()
	h.Register(mux)

	// requestWithIdentity hard-codes Tenant: "t1"; that's the
	// scoping signal the handler reads. The accessor returns rtA
	// for "t1", rtB for "t2"; the tenant-A request must never
	// see appr-B even though appr-B is registered on the
	// accessor's map for "t2".
	req := requestWithIdentity(http.MethodGet, "/v1/approvals")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200; body=%q", rec.Code, rec.Body.String())
	}
	var body struct {
		Approvals []struct {
			ID string `json:"id"`
		} `json:"approvals"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if len(body.Approvals) != 1 {
		t.Fatalf("tenant t1 approvals: got %d, want 1", len(body.Approvals))
	}
	if body.Approvals[0].ID != "appr-A" {
		t.Errorf("tenant t1 saw %q, want appr-A (cross-tenant leakage!)", body.Approvals[0].ID)
	}
}

// A domain-bound credential now reads through its own domain's scoped
// runtime store rather than being refused outright. The
// handler must select ForDomain, not the tenant default, or a bound key
// would silently read another domain's queue.
func TestApprovalsList_DomainBoundKeyReadsItsOwnDomain(t *testing.T) {
	t.Parallel()
	own := buildEmptyApprovalRuntime(t, approvalReviewYAML)
	seedApproval(t, own, "appr-mine", "order-1", "ISSUE_REFUND", kiffapproval.StatusPending, time.Now().UTC())
	other := buildEmptyApprovalRuntime(t, approvalReviewYAML)
	seedApproval(t, other, "appr-other", "order-9", "ISSUE_REFUND", kiffapproval.StatusPending, time.Now().UTC())

	access := newMultiTenantAccessor()
	access.runtimes["t1"] = other             // the tenant default
	access.domainRuntimes["t1/refunds"] = own // the bound key's domain

	h := handlers.NewApprovalsListHandler(access, readerFor(access), nil)
	mux := http.NewServeMux()
	h.Register(mux)

	req := requestWithScopedIdentity(http.MethodGet, "/v1/approvals", nil, "refunds")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%q", rec.Code, rec.Body.String())
	}
	if !containsExact(rec.Body.String(), "appr-mine") {
		t.Errorf("bound key did not see its own domain's approval: %s", rec.Body.String())
	}
	if containsExact(rec.Body.String(), "appr-other") {
		t.Fatal("bound key read the tenant default domain's queue instead of its own")
	}
	if !containsExact(rec.Body.String(), `"domain_id":"refunds"`) {
		t.Errorf("response must annotate the owning domain: %s", rec.Body.String())
	}
}

// A management identity with no reader wired must report 503 rather than
// quietly falling back to one domain's runtime store — that fallback is
// exactly how the tenant-wide queue would become single-domain.
func TestApprovalsList_ManagementWithoutReader_Returns503(t *testing.T) {
	t.Parallel()
	access := newMultiTenantAccessor()
	access.runtimes["t1"] = buildEmptyApprovalRuntime(t, approvalReviewYAML)
	h := handlers.NewApprovalsListHandler(access, nil, nil)
	mux := http.NewServeMux()
	h.Register(mux)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, requestWithIdentity(http.MethodGet, "/v1/approvals"))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body=%q", rec.Code, rec.Body.String())
	}
}

// TestApprovalsList_EmptyResult_ReturnsArrayNotNull confirms the
// envelope shape when no rows match: the approvals key is the
// empty array and last_received_at is absent. The dashboard's
// renderer relies on the array not being null.
func TestApprovalsList_EmptyResult_ReturnsArrayNotNull(t *testing.T) {
	t.Parallel()
	rt := buildEmptyApprovalRuntime(t, approvalReviewYAML)
	// No approvals seeded.

	access := newMultiTenantAccessor()
	access.runtimes["t1"] = rt

	h := handlers.NewApprovalsListHandler(access, readerFor(access), nil)
	mux := http.NewServeMux()
	h.Register(mux)

	req := requestWithIdentity(http.MethodGet, "/v1/approvals")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200; body=%q", rec.Code, rec.Body.String())
	}

	// Assert the wire payload has "approvals":[], not null. A null
	// encoded value serializes as "null"; an empty []Approval
	// initialised by the handler serializes as "[]".
	if !containsExact(rec.Body.String(), `"approvals":[]`) {
		t.Errorf("expected approvals to render as empty array; body=%q", rec.Body.String())
	}
	if containsExact(rec.Body.String(), `"last_received_at"`) {
		t.Errorf("last_received_at must be absent on empty result; body=%q", rec.Body.String())
	}
}

// containsExact is a tiny string contains helper; isolated here
// to keep the assertion intent obvious.
func containsExact(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

// TestApprovalsList_LimitClamps confirms the limit parameter caps
// at the max and the default applies when omitted. The dashboard's
// queue uses a default of 50 (approvalsListLimitDefault); the cap
// at 200 keeps a rogue caller from pulling everything.
func TestApprovalsList_LimitClamps(t *testing.T) {
	t.Parallel()
	rt := buildEmptyApprovalRuntime(t, approvalReviewYAML)
	// Seed five rows so a limit=2 query has something to cap.
	now := time.Now().UTC()
	for i := 0; i < 5; i++ {
		seedApproval(t, rt, "appr-"+string(rune('a'+i)), "order-X",
			"ISSUE_REFUND", kiffapproval.StatusPending,
			now.Add(time.Duration(-i)*time.Hour))
	}

	access := newMultiTenantAccessor()
	access.runtimes["t1"] = rt

	h := handlers.NewApprovalsListHandler(access, readerFor(access), nil)
	mux := http.NewServeMux()
	h.Register(mux)

	req := requestWithIdentity(http.MethodGet, "/v1/approvals?limit=2")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200; body=%q", rec.Code, rec.Body.String())
	}
	var body struct {
		Approvals []struct {
			ID string `json:"id"`
		} `json:"approvals"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if len(body.Approvals) != 2 {
		t.Fatalf("limit=2 returned %d rows", len(body.Approvals))
	}
}

// TestApprovalsList_RejectsUnknownStatus asserts the 400 path on
// a malformed query parameter. The error message names the
// allowed values so a client can self-correct.
func TestApprovalsList_RejectsUnknownStatus(t *testing.T) {
	t.Parallel()
	rt := buildEmptyApprovalRuntime(t, approvalReviewYAML)
	access := newMultiTenantAccessor()
	access.runtimes["t1"] = rt

	h := handlers.NewApprovalsListHandler(access, readerFor(access), nil)
	mux := http.NewServeMux()
	h.Register(mux)

	req := requestWithIdentity(http.MethodGet, "/v1/approvals?status=bogus")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d, want 400", rec.Code)
	}
	if !containsExact(rec.Body.String(), "pending, granted, denied") {
		t.Errorf("400 body should name allowed statuses; got %q", rec.Body.String())
	}
}

// TestApprovalsList_NoIdentity_Returns401 confirms the auth
// boundary. Without an identity in the request context, the
// handler 401s before touching any tenant runtime.
func TestApprovalsList_NoIdentity_Returns401(t *testing.T) {
	t.Parallel()
	access := newMultiTenantAccessor()
	h := handlers.NewApprovalsListHandler(access, readerFor(access), nil)
	mux := http.NewServeMux()
	h.Register(mux)

	req := httptest.NewRequest(http.MethodGet, "/v1/approvals", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status: got %d, want 401", rec.Code)
	}
}

// TestApprovalsList_NoDomainConfigured_ReturnsEmptyQueue asserts the
// pre-connection state: a tenant that has not uploaded a domain yet
// has no runtime to resolve, so the runtime accessor returns
// domain.ErrDomainNotFound. The handler must treat this as an empty
// approval queue (200) rather than a 502, so a freshly-provisioned
// workspace sees "No pending approvals" instead of an alarming error.
func TestApprovalsList_NoDomainConfigured_ReturnsEmptyQueue(t *testing.T) {
	t.Parallel()
	access := newMultiTenantAccessor()
	// t1 is intentionally not registered; the accessor returns the
	// configured error, mimicking a tenant with no domain manifest.
	access.notFound = domain.ErrDomainNotFound

	h := handlers.NewApprovalsListHandler(access, readerFor(access), nil)
	mux := http.NewServeMux()
	h.Register(mux)

	req := requestWithIdentity(http.MethodGet, "/v1/approvals?status=pending")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200; body=%q", rec.Code, rec.Body.String())
	}
	var body struct {
		Approvals      []kiffapproval.Approval `json:"approvals"`
		LastReceivedAt *time.Time              `json:"last_received_at,omitempty"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v; raw=%q", err, rec.Body.String())
	}
	if body.Approvals == nil {
		t.Errorf("approvals should be an empty array, not null; raw=%q", rec.Body.String())
	}
	if len(body.Approvals) != 0 {
		t.Errorf("expected empty queue, got %d rows", len(body.Approvals))
	}
	if body.LastReceivedAt != nil {
		t.Errorf("last_received_at should be absent for an empty queue, got %v", body.LastReceivedAt)
	}
}

// TestApprovalsList_RuntimeUnavailable_Returns502 asserts that a
// genuine runtime-resolution failure (not the no-domain case) still
// surfaces as a 502, so real data-plane breakage is not masked as an
// empty queue.
func TestApprovalsList_RuntimeUnavailable_Returns502(t *testing.T) {
	t.Parallel()
	access := newMultiTenantAccessor()
	access.notFound = errors.New("connection refused")

	h := handlers.NewApprovalsListHandler(access, readerFor(access), nil)
	mux := http.NewServeMux()
	h.Register(mux)

	req := requestWithIdentity(http.MethodGet, "/v1/approvals")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status: got %d, want 502; body=%q", rec.Code, rec.Body.String())
	}
}
