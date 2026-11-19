package handlers_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/domain"
	"github.com/kiff/cloud/api/internal/executors"
	"github.com/kiff/cloud/api/internal/handlers"
)

// validYAML is a tiny domain that parses against the default
// executor registry. Tests reuse it whenever they need a YAML
// the cloud will accept.
const validYAML = `
domain: test-domain
entity: TestEntity
events:
  - CREATED
states:
  - NEW
transitions:
  - on: CREATED
    from: ""
    to: NEW
actions:
  - name: NOOP
    allowed_states: [NEW]
    required_parameters: [opened_by]
    risk: low
    approval: never
    executor: cafe.start_shift
`

// fakeResetter records ResetTenant calls so tests can assert the
// runtime cache invalidation contract.
type fakeResetter struct {
	mu    sync.Mutex
	calls []string
}

func (f *fakeResetter) ResetTenant(tenantID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, tenantID)
}

func (f *fakeResetter) Calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.calls))
	copy(out, f.calls)
	return out
}

// newDomainTestRig wires a DomainHandler against in-memory
// dependencies and returns the http.ServeMux so tests can drive
// it via httptest. The supplied tenant id is the one IdentityFrom
// will return; tests that need a different tenant override it.
func newDomainTestRig(t *testing.T, tenantID string) (*http.ServeMux, *domain.MemorySource, *fakeResetter) {
	t.Helper()
	src := domain.NewMemorySource()
	resetter := &fakeResetter{}
	h := handlers.NewDomainHandler(src, executors.DefaultRegistry(), resetter)
	mux := http.NewServeMux()
	h.Register(mux)
	// Wrap with a synthetic auth middleware so every request
	// carries the supplied identity. tenant_owner models the
	// dashboard session (auth.sessionOwnerRoles); the upload route
	// requires the management role.
	wrapped := http.NewServeMux()
	wrapped.Handle("/", withFakeIdentity(mux, auth.Identity{
		Tenant: tenantID, Subject: "test-subject", Roles: []string{"tenant_owner"},
	}))
	return wrapped, src, resetter
}

// withFakeIdentity stamps an Identity onto every inbound request
// before delegating, mimicking what the real auth middleware does.
func withFakeIdentity(next http.Handler, id auth.Identity) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := auth.WithIdentity(r.Context(), id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func TestDomainValidate_AcceptsValidYAML(t *testing.T) {
	t.Parallel()
	mux, _, resetter := newDomainTestRig(t, "tenant-A")
	req := httptest.NewRequest(http.MethodPost, "/v1/me/domain/validate", strings.NewReader(validYAML))
	req.Header.Set("Content-Type", "application/yaml")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if len(resetter.Calls()) != 0 {
		t.Fatalf("validate must not reset runtime cache; got calls=%v", resetter.Calls())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body["domain"] != "test-domain" {
		t.Errorf("domain = %v", body["domain"])
	}
}

func TestDomainValidate_RejectsUnknownExecutor(t *testing.T) {
	t.Parallel()
	mux, src, resetter := newDomainTestRig(t, "tenant-A")
	bad := strings.ReplaceAll(validYAML, "cafe.start_shift", "tenant.custom_code")
	req := httptest.NewRequest(http.MethodPost, "/v1/me/domain/validate", strings.NewReader(bad))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422; body = %s", rec.Code, rec.Body.String())
	}
	if _, err := src.Load(context.Background(), "tenant-A"); err == nil {
		t.Fatalf("validate must not persist; tenant-A unexpectedly has a domain")
	}
	if len(resetter.Calls()) != 0 {
		t.Fatalf("validate must not reset runtime cache; got calls=%v", resetter.Calls())
	}
}

func TestDomainUpload_PersistsAndResetsRuntime(t *testing.T) {
	t.Parallel()
	mux, src, resetter := newDomainTestRig(t, "tenant-A")
	req := httptest.NewRequest(http.MethodPut, "/v1/me/domain", strings.NewReader(validYAML))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	td, err := src.Load(context.Background(), "tenant-A")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if td.Version != 1 {
		t.Errorf("version = %d, want 1", td.Version)
	}
	calls := resetter.Calls()
	if len(calls) != 1 || calls[0] != "tenant-A" {
		t.Errorf("expected one ResetTenant(tenant-A); got %v", calls)
	}
}

func TestDomainUpload_InvalidDoesNotReplacePrevious(t *testing.T) {
	t.Parallel()
	mux, src, resetter := newDomainTestRig(t, "tenant-A")
	// First, upload a valid YAML.
	req := httptest.NewRequest(http.MethodPut, "/v1/me/domain", strings.NewReader(validYAML))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("first upload: status = %d", rec.Code)
	}
	if len(resetter.Calls()) != 1 {
		t.Fatalf("first upload should reset once; got %v", resetter.Calls())
	}
	// Now upload garbage. The previous YAML must remain.
	bad := strings.ReplaceAll(validYAML, "cafe.start_shift", "definitely.not.real")
	req2 := httptest.NewRequest(http.MethodPut, "/v1/me/domain", strings.NewReader(bad))
	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid upload: status = %d, want 422; body = %s", rec2.Code, rec2.Body.String())
	}
	if len(resetter.Calls()) != 1 {
		t.Fatalf("invalid upload must not reset runtime cache; got %v", resetter.Calls())
	}
	td, _ := src.Load(context.Background(), "tenant-A")
	if !strings.Contains(string(td.YAML), "cafe.start_shift") {
		t.Fatalf("invalid upload corrupted stored YAML: %s", td.YAML)
	}
}

func TestDomainUpload_TenantScoped(t *testing.T) {
	t.Parallel()
	// tenant-A's request must never touch tenant-B's row, even
	// when the YAML body claims a different domain. The handler
	// always uses the tenant id from the auth context.
	srcA, _ := newSharedSource()
	mux := newSharedMux(srcA, "tenant-A")
	req := httptest.NewRequest(http.MethodPut, "/v1/me/domain", strings.NewReader(validYAML))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}

	if _, err := srcA.Load(context.Background(), "tenant-A"); err != nil {
		t.Fatalf("tenant-A: Load: %v", err)
	}
	if _, err := srcA.Load(context.Background(), "tenant-B"); err == nil {
		t.Fatalf("tenant-B unexpectedly has a domain row")
	}
}

func TestDomainGet_ReturnsNotFound_WhenNoDomain(t *testing.T) {
	t.Parallel()
	mux, _, _ := newDomainTestRig(t, "tenant-A")
	req := httptest.NewRequest(http.MethodGet, "/v1/me/domain", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body = %s", rec.Code, rec.Body.String())
	}
}

func TestDomainGet_ReturnsActiveSummary(t *testing.T) {
	t.Parallel()
	mux, src, _ := newDomainTestRig(t, "tenant-A")
	if err := src.Save(context.Background(), "tenant-A", []byte(validYAML)); err != nil {
		t.Fatalf("seed: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/v1/me/domain", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body["domain"] != "test-domain" {
		t.Errorf("domain = %v", body["domain"])
	}
	if body["version"].(float64) != 1 {
		t.Errorf("version = %v", body["version"])
	}
	actions, ok := body["actions"].([]any)
	if !ok || len(actions) != 1 {
		t.Fatalf("actions = %#v, want one action", body["actions"])
	}
	action, ok := actions[0].(map[string]any)
	if !ok {
		t.Fatalf("actions[0] = %#v, want object", actions[0])
	}
	required, ok := action["required_parameters"].([]any)
	if !ok || len(required) != 1 || required[0] != "opened_by" {
		t.Fatalf("required_parameters = %#v, want [opened_by]", action["required_parameters"])
	}
}

func TestDomainValidate_RequiresAuth(t *testing.T) {
	t.Parallel()
	src := domain.NewMemorySource()
	h := handlers.NewDomainHandler(src, executors.DefaultRegistry(), nil)
	mux := http.NewServeMux()
	h.Register(mux)
	// No auth wrapper — the handler must reject 401.
	req := httptest.NewRequest(http.MethodPost, "/v1/me/domain/validate", strings.NewReader(validYAML))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestDomainUpload_RejectsEmptyBody(t *testing.T) {
	t.Parallel()
	mux, _, _ := newDomainTestRig(t, "tenant-A")
	req := httptest.NewRequest(http.MethodPut, "/v1/me/domain", strings.NewReader("   \n  "))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestDomainUpload_RejectsTooLargeBody(t *testing.T) {
	t.Parallel()
	src := domain.NewMemorySource()
	h := handlers.NewDomainHandler(src, executors.DefaultRegistry(), nil, handlers.WithDomainMaxBytes(64))
	mux := http.NewServeMux()
	h.Register(mux)
	wrapped := http.NewServeMux()
	wrapped.Handle("/", withFakeIdentity(mux, auth.Identity{
		Tenant: "tenant-A", Subject: "test", Roles: []string{"tenant_owner"},
	}))
	req := httptest.NewRequest(http.MethodPut, "/v1/me/domain", strings.NewReader(validYAML))
	rec := httptest.NewRecorder()
	wrapped.ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413; body = %s", rec.Code, rec.Body.String())
	}
}

// ─── helpers ──────────────────────────────────────────────────

// newSharedSource is a small helper for the tenant-isolation
// test. Returns a fresh memory source plus an unused second
// reference so the test reads as "two views, one source".
func newSharedSource() (*domain.MemorySource, *domain.MemorySource) {
	s := domain.NewMemorySource()
	return s, s
}

// newSharedMux builds a handler against the given source +
// identity. Does not reset runtime cache.
func newSharedMux(src *domain.MemorySource, tenantID string) http.Handler {
	h := handlers.NewDomainHandler(src, executors.DefaultRegistry(), nil)
	mux := http.NewServeMux()
	h.Register(mux)
	wrapped := http.NewServeMux()
	wrapped.Handle("/", withFakeIdentity(mux, auth.Identity{
		Tenant: tenantID, Subject: "test", Roles: []string{"tenant_owner"},
	}))
	return wrapped
}

// the trusted-run-context design: validate is where an author checks that what they wrote took
// effect. An action's declared run-context dependency has to come back,
// or a domain that reads as governed on this surface is not — the exact
// defect the control exists to prevent, reproduced in its own API.
func TestDomainValidate_EchoesDeclaredRunContext(t *testing.T) {
	t.Parallel()
	mux, _, _ := newDomainTestRig(t, "tenant-rc")

	withRule := strings.Replace(validYAML,
		"    executor: cafe.start_shift",
		"    executor: cafe.start_shift\n    run_context:\n      on_untrusted_input: approval", 1)

	req := httptest.NewRequest(http.MethodPost, "/v1/me/domain/validate", strings.NewReader(withRule))
	req.Header.Set("Content-Type", "application/yaml")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var body struct {
		Actions []struct {
			Name       string `json:"name"`
			RunContext *struct {
				OnUntrustedInput string `json:"on_untrusted_input"`
			} `json:"run_context"`
		} `json:"actions"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	var found bool
	for _, a := range body.Actions {
		if a.RunContext == nil {
			continue
		}
		found = true
		if a.RunContext.OnUntrustedInput != "approval" {
			t.Errorf("action %s: on_untrusted_input = %q, want approval", a.Name, a.RunContext.OnUntrustedInput)
		}
	}
	if !found {
		t.Errorf("no action echoed its run_context; an author cannot confirm the rule registered: %s",
			rec.Body.String())
	}
}

// An action declaring no rule omits the field, so a domain written
// before the trusted-run-context design validates to byte-identical output.
func TestDomainValidate_OmitsRunContextWhenUndeclared(t *testing.T) {
	t.Parallel()
	mux, _, _ := newDomainTestRig(t, "tenant-rc2")

	req := httptest.NewRequest(http.MethodPost, "/v1/me/domain/validate", strings.NewReader(validYAML))
	req.Header.Set("Content-Type", "application/yaml")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if strings.Contains(rec.Body.String(), "run_context") {
		t.Errorf("run_context appeared for a domain that declares none: %s", rec.Body.String())
	}
}
