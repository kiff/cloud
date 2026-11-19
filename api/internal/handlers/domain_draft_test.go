package handlers_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/domain"
	"github.com/kiff/cloud/api/internal/executors"
	"github.com/kiff/cloud/api/internal/handlers"
)

// newDraftTestRig wires a DraftHandler against in-memory
// sources and returns the http.ServeMux + the underlying
// stores so tests can assert state. tenantID is what
// IdentityFrom will return on every request.
func newDraftTestRig(t *testing.T, tenantID string) (*http.ServeMux, *domain.MemorySource, *domain.MemoryDraftSource, *fakeResetter) {
	t.Helper()
	src := domain.NewMemorySource()
	drafts := domain.NewMemoryDraftSource()
	resetter := &fakeResetter{}
	h := handlers.NewDraftHandler(src, drafts, executors.DefaultRegistry(), resetter, nil)
	mux := http.NewServeMux()
	h.Register(mux)
	wrapped := http.NewServeMux()
	// tenant_owner models the dashboard session; the activate route
	// requires the management role.
	wrapped.Handle("/", withFakeIdentity(mux, auth.Identity{
		Tenant: tenantID, Subject: "subj", Roles: []string{"tenant_owner"},
	}))
	return wrapped, src, drafts, resetter
}

// TestDraftPut_AcceptsValidYAMLAndReportsParse confirms a clean
// PUT round-trips: the draft saves, the response carries the
// parsed summary, and no issues fire.
func TestDraftPut_AcceptsValidYAMLAndReportsParse(t *testing.T) {
	t.Parallel()
	mux, _, drafts, _ := newDraftTestRig(t, "tenant-A")
	req := httptest.NewRequest(http.MethodPut, "/v1/me/domain/draft", strings.NewReader(validYAML))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var body struct {
		YAML   string `json:"yaml"`
		Parsed struct {
			Domain string `json:"domain"`
		} `json:"parsed"`
		Issues []map[string]string `json:"issues"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body.Parsed.Domain != "test-domain" {
		t.Errorf("parsed.domain = %q, want test-domain", body.Parsed.Domain)
	}
	if len(body.Issues) != 0 {
		t.Errorf("expected no issues on valid yaml, got %v", body.Issues)
	}
	td, err := drafts.Load(context.Background(), "tenant-A")
	if err != nil {
		t.Fatalf("draft must persist: %v", err)
	}
	if !strings.Contains(string(td.YAML), "test-domain") {
		t.Errorf("stored draft missing domain: %q", td.YAML)
	}
}

// TestDraftPut_AcceptsInvalidYAMLButReportsIssues is the
// auto-save promise: a draft mid-edit may not parse, but the
// handler still saves it. The response carries the issues so the
// editor can render them inline.
func TestDraftPut_AcceptsInvalidYAMLButReportsIssues(t *testing.T) {
	t.Parallel()
	mux, _, drafts, _ := newDraftTestRig(t, "tenant-A")
	bad := strings.ReplaceAll(validYAML, "cafe.start_shift", "tenant.custom_code")
	req := httptest.NewRequest(http.MethodPut, "/v1/me/domain/draft", strings.NewReader(bad))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (drafts always save); body = %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Parsed *struct{}           `json:"parsed"`
		Issues []map[string]string `json:"issues"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body.Parsed != nil {
		t.Errorf("invalid yaml should not produce a parsed summary")
	}
	if len(body.Issues) == 0 {
		t.Errorf("invalid yaml should report issues")
	}
	if _, err := drafts.Load(context.Background(), "tenant-A"); err != nil {
		t.Fatalf("draft must persist even when invalid: %v", err)
	}
}

// TestDraftGet_ReturnsLastSavedYAML confirms the save → reload
// cycle. The handler must echo back exactly what was last
// PUT, byte-for-byte.
func TestDraftGet_ReturnsLastSavedYAML(t *testing.T) {
	t.Parallel()
	mux, _, _, _ := newDraftTestRig(t, "tenant-A")
	put := httptest.NewRequest(http.MethodPut, "/v1/me/domain/draft", strings.NewReader(validYAML))
	mux.ServeHTTP(httptest.NewRecorder(), put)

	get := httptest.NewRequest(http.MethodGet, "/v1/me/domain/draft", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, get)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var body struct {
		YAML      string `json:"yaml"`
		UpdatedAt int64  `json:"updated_at"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body.YAML != validYAML {
		t.Errorf("yaml mismatch on round-trip:\n--- got ---\n%s\n--- want ---\n%s", body.YAML, validYAML)
	}
	if body.UpdatedAt == 0 {
		t.Errorf("updated_at should be populated; got 0")
	}
}

// TestDraftGet_404WhenNoDraft confirms the empty-state contract.
// The Studio handler treats this as "render the placeholder
// stage 1 draft" rather than as an error.
func TestDraftGet_404WhenNoDraft(t *testing.T) {
	t.Parallel()
	mux, _, _, _ := newDraftTestRig(t, "tenant-A")
	req := httptest.NewRequest(http.MethodGet, "/v1/me/domain/draft", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

// TestDraftDelete_DiscardsDraft confirms DELETE is wired and
// removes the row.
func TestDraftDelete_DiscardsDraft(t *testing.T) {
	t.Parallel()
	mux, _, drafts, _ := newDraftTestRig(t, "tenant-A")
	put := httptest.NewRequest(http.MethodPut, "/v1/me/domain/draft", strings.NewReader(validYAML))
	mux.ServeHTTP(httptest.NewRecorder(), put)

	del := httptest.NewRequest(http.MethodDelete, "/v1/me/domain/draft", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, del)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	if _, err := drafts.Load(context.Background(), "tenant-A"); err == nil {
		t.Fatalf("draft must be gone after DELETE")
	}
}

// TestDraftActivate_HappyPath_PromotesAndResets is the core
// transactional contract: activate parses, upserts the active
// row, deletes the draft, and resets the runtime cache.
func TestDraftActivate_HappyPath_PromotesAndResets(t *testing.T) {
	t.Parallel()
	mux, src, drafts, resetter := newDraftTestRig(t, "tenant-A")
	put := httptest.NewRequest(http.MethodPut, "/v1/me/domain/draft", strings.NewReader(validYAML))
	mux.ServeHTTP(httptest.NewRecorder(), put)

	act := httptest.NewRequest(http.MethodPost, "/v1/me/domain/draft/activate", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, act)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Domain  string `json:"domain"`
		Version int    `json:"version"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body.Domain != "test-domain" {
		t.Errorf("domain = %q, want test-domain", body.Domain)
	}
	if body.Version != 1 {
		t.Errorf("version = %d, want 1 on first activate", body.Version)
	}
	// Active row exists.
	td, err := src.Load(context.Background(), "tenant-A")
	if err != nil {
		t.Fatalf("active row missing after activate: %v", err)
	}
	if string(td.YAML) != validYAML {
		t.Errorf("active yaml mismatch")
	}
	// Draft row gone.
	if _, err := drafts.Load(context.Background(), "tenant-A"); err == nil {
		t.Fatalf("draft must be deleted on activate")
	}
	// Runtime reset called.
	if got := resetter.Calls(); len(got) != 1 || got[0] != "tenant-A" {
		t.Fatalf("ResetTenant calls = %v, want [tenant-A]", got)
	}
}

// TestDraftActivate_RejectsInvalidDraft confirms an invalid
// draft cannot reach the active row. The draft stays put; the
// runtime is not reset.
func TestDraftActivate_RejectsInvalidDraft(t *testing.T) {
	t.Parallel()
	mux, src, drafts, resetter := newDraftTestRig(t, "tenant-A")
	bad := strings.ReplaceAll(validYAML, "cafe.start_shift", "tenant.custom_code")
	put := httptest.NewRequest(http.MethodPut, "/v1/me/domain/draft", strings.NewReader(bad))
	mux.ServeHTTP(httptest.NewRecorder(), put)

	act := httptest.NewRequest(http.MethodPost, "/v1/me/domain/draft/activate", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, act)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422; body = %s", rec.Code, rec.Body.String())
	}
	if _, err := src.Load(context.Background(), "tenant-A"); err == nil {
		t.Fatalf("active row must not exist after rejected activate")
	}
	if _, err := drafts.Load(context.Background(), "tenant-A"); err != nil {
		t.Fatalf("draft must remain on rejected activate: %v", err)
	}
	if got := resetter.Calls(); len(got) != 0 {
		t.Fatalf("runtime must not reset on rejected activate; got %v", got)
	}
}

// TestDraftActivate_404WhenNoDraft confirms calling activate
// without a draft is a clean 404, not a 5xx.
func TestDraftActivate_404WhenNoDraft(t *testing.T) {
	t.Parallel()
	mux, _, _, _ := newDraftTestRig(t, "tenant-A")
	act := httptest.NewRequest(http.MethodPost, "/v1/me/domain/draft/activate", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, act)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

// TestDraftActivate_BumpsVersionOnRepeatedActivate confirms the
// upsert path: a tenant with an existing active domain who
// activates a new draft gets version 2.
func TestDraftActivate_BumpsVersionOnRepeatedActivate(t *testing.T) {
	t.Parallel()
	mux, _, _, _ := newDraftTestRig(t, "tenant-A")

	// First activate: version = 1.
	mux.ServeHTTP(httptest.NewRecorder(),
		httptest.NewRequest(http.MethodPut, "/v1/me/domain/draft", strings.NewReader(validYAML)))
	mux.ServeHTTP(httptest.NewRecorder(),
		httptest.NewRequest(http.MethodPost, "/v1/me/domain/draft/activate", nil))

	// Second activate with an edited YAML: version = 2.
	tweaked := strings.ReplaceAll(validYAML, "TestEntity", "TestEntityV2")
	mux.ServeHTTP(httptest.NewRecorder(),
		httptest.NewRequest(http.MethodPut, "/v1/me/domain/draft", strings.NewReader(tweaked)))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/me/domain/draft/activate", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Version int `json:"version"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body.Version != 2 {
		t.Errorf("version = %d, want 2 on second activate", body.Version)
	}
}

// TestDraftRoutes_RejectAnonymousIdentity confirms the four
// routes inherit the auth boundary. This catches a future
// refactor that drops requireIdentity from one of them.
func TestDraftRoutes_RejectAnonymousIdentity(t *testing.T) {
	t.Parallel()
	src := domain.NewMemorySource()
	drafts := domain.NewMemoryDraftSource()
	h := handlers.NewDraftHandler(src, drafts, executors.DefaultRegistry(), &fakeResetter{}, nil)
	mux := http.NewServeMux()
	h.Register(mux)

	for _, tc := range []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/v1/me/domain/draft"},
		{http.MethodPut, "/v1/me/domain/draft"},
		{http.MethodDelete, "/v1/me/domain/draft"},
		{http.MethodPost, "/v1/me/domain/draft/activate"},
	} {
		t.Run(tc.method+"_"+tc.path, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader("{}"))
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", rec.Code)
			}
		})
	}
}
