package handlers_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/domain"
	"github.com/kiff/cloud/api/internal/executors"
	"github.com/kiff/cloud/api/internal/handlers"
	"github.com/kiff/cloud/api/internal/templates"
)

// stubResetter records the tenant ids passed to ResetTenant so
// tests can assert the cache was invalidated after a successful
// apply.
type stubResetter struct {
	calls atomic.Int32
	last  atomic.Value // string
}

func (s *stubResetter) ResetTenant(tenantID string) {
	s.calls.Add(1)
	s.last.Store(tenantID)
}

// newTemplatesRouter returns a fresh router exposing only the
// templates handler, behind a stub auth middleware that injects an
// Identity for every request. The stub keeps the test focused on
// the handler's contract; real auth is exercised in cmd/api tests.
func newTemplatesRouter(t *testing.T, source domain.Source, runtimes handlers.RuntimeResetter, tenantID string, opts ...handlers.TemplatesOption) http.Handler {
	t.Helper()
	registry := executors.DefaultRegistry()
	mux := http.NewServeMux()
	handlers.NewTemplatesHandler(source, registry, runtimes, opts...).Register(mux)

	// Wrap in a stub middleware that injects an Identity for the
	// configured tenantID. Empty tenantID simulates the
	// "no tenant on identity" path.
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := auth.Identity{
			Subject: "subj-1",
			Tenant:  tenantID,
			Method:  auth.MethodAPIKey,
			APIKey:  true,
			// Applying a template authors the active contract, so it
			// requires the management role. A plain runtime key does not
			// carry it; a key minted with tenant_owner (or the dashboard
			// session) does. The rig models the latter.
			Roles: []string{"tenant_owner"},
		}
		ctx := auth.WithIdentity(r.Context(), id)
		mux.ServeHTTP(w, r.WithContext(ctx))
	})
}

func TestTemplates_Apply_RecordsControlActivation(t *testing.T) {
	t.Parallel()
	const tenantID = "ten-1"
	source := domain.NewMemorySource()
	controls := domain.NewMemoryControlStore()
	router := newTemplatesRouter(t, source, &stubResetter{}, tenantID,
		handlers.WithTemplatesControlStore(controls))

	req := httptest.NewRequest(http.MethodPost, "/v1/me/domain/template",
		strings.NewReader(`{"slug":"card-refund"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%q)", rec.Code, rec.Body.String())
	}

	got, err := controls.List(context.Background(), tenantID)
	if err != nil {
		t.Fatalf("controls.List: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("controls = %d, want one activation", len(got))
	}
	ctrl := got[0]
	if ctrl.Domain != "card-refund" || ctrl.Entity != "Order" || ctrl.Status != "active" {
		t.Fatalf("control = %+v, want active card-refund Order", ctrl)
	}
	if ctrl.ActivatedBy != "subj-1" {
		t.Errorf("activated_by = %q, want subj-1", ctrl.ActivatedBy)
	}
	if len(ctrl.Actions) != 2 || ctrl.Actions[0] != "REFUND_ORDER" || ctrl.Actions[1] != "ISSUE_CREDIT" {
		t.Errorf("actions = %v, want [REFUND_ORDER ISSUE_CREDIT]", ctrl.Actions)
	}
}

func TestTemplates_List_ReturnsCatalog(t *testing.T) {
	t.Parallel()
	source := domain.NewMemorySource()
	router := newTemplatesRouter(t, source, &stubResetter{}, "ten-1")

	req := httptest.NewRequest(http.MethodGet, "/v1/templates", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%q)", rec.Code, rec.Body.String())
	}
	var got struct {
		Templates []templates.Template `json:"templates"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Templates) != 11 {
		t.Fatalf("templates count = %d, want 11", len(got.Templates))
	}
	for _, tmpl := range got.Templates {
		if tmpl.Slug == "" || tmpl.Name == "" || tmpl.Description == "" {
			t.Errorf("template %+v missing required field", tmpl)
		}
		if len(tmpl.SampleEvent) == 0 {
			t.Errorf("template %s missing sample_event", tmpl.Slug)
		}
		// YAML body is intentionally omitted from the wire
		// shape (json:"-" tag); confirm it didn't leak.
		if strings.Contains(rec.Body.String(), "domain:") {
			t.Errorf("YAML body leaked into list response")
		}
	}
}

func TestTemplates_List_RequiresAuth(t *testing.T) {
	t.Parallel()
	source := domain.NewMemorySource()
	registry := executors.DefaultRegistry()
	mux := http.NewServeMux()
	handlers.NewTemplatesHandler(source, registry, nil).Register(mux)

	req := httptest.NewRequest(http.MethodGet, "/v1/templates", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestTemplates_Apply_PersistsYAMLAndResetsRuntime(t *testing.T) {
	t.Parallel()
	const tenantID = "ten-1"
	source := domain.NewMemorySource()
	resetter := &stubResetter{}
	router := newTemplatesRouter(t, source, resetter, tenantID)

	body := `{"slug":"refund-agno"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/me/domain/template", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%q)", rec.Code, rec.Body.String())
	}
	var got struct {
		Slug    string `json:"slug"`
		Tenant  string `json:"tenant"`
		Version int    `json:"version"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Slug != "refund-agno" {
		t.Errorf("slug = %q, want refund-agno", got.Slug)
	}
	if got.Tenant != tenantID {
		t.Errorf("tenant = %q, want %q", got.Tenant, tenantID)
	}
	if got.Version != 1 {
		t.Errorf("version = %d, want 1 (first apply)", got.Version)
	}

	// The YAML must be in the source.
	td, err := source.Load(context.Background(), tenantID)
	if err != nil {
		t.Fatalf("source.Load: %v", err)
	}
	if !strings.Contains(string(td.YAML), "domain: refund-agno") {
		t.Fatalf("source did not record refund-agno YAML; got first 80 bytes: %q", string(td.YAML[:80]))
	}

	// The runtime must have been reset for the right tenant.
	if got := resetter.calls.Load(); got != 1 {
		t.Fatalf("ResetTenant calls = %d, want 1", got)
	}
	if got := resetter.last.Load().(string); got != tenantID {
		t.Fatalf("ResetTenant arg = %q, want %q", got, tenantID)
	}
}

func TestTemplates_Apply_IsIdempotent(t *testing.T) {
	t.Parallel()
	const tenantID = "ten-1"
	source := domain.NewMemorySource()
	router := newTemplatesRouter(t, source, &stubResetter{}, tenantID)

	body := `{"slug":"cafe-ops"}`
	for i := 1; i <= 3; i++ {
		req := httptest.NewRequest(http.MethodPost, "/v1/me/domain/template", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("apply %d: status = %d (body=%q)", i, rec.Code, rec.Body.String())
		}
	}
	td, err := source.Load(context.Background(), tenantID)
	if err != nil {
		t.Fatalf("source.Load: %v", err)
	}
	if td.Version != 3 {
		t.Fatalf("version after 3 applies = %d, want 3", td.Version)
	}
}

func TestTemplates_Apply_SwitchesDomain(t *testing.T) {
	t.Parallel()
	const tenantID = "ten-1"
	source := domain.NewMemorySource()
	router := newTemplatesRouter(t, source, &stubResetter{}, tenantID)

	// Apply cafe-ops first.
	postSlug := func(slug string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/v1/me/domain/template",
			strings.NewReader(`{"slug":"`+slug+`"}`))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}
	if rec := postSlug("cafe-ops"); rec.Code != http.StatusOK {
		t.Fatalf("apply cafe-ops: %d (body=%q)", rec.Code, rec.Body.String())
	}
	// Then switch to refund-agno.
	if rec := postSlug("refund-agno"); rec.Code != http.StatusOK {
		t.Fatalf("apply refund-agno: %d (body=%q)", rec.Code, rec.Body.String())
	}
	td, err := source.Load(context.Background(), tenantID)
	if err != nil {
		t.Fatalf("source.Load: %v", err)
	}
	if !strings.Contains(string(td.YAML), "domain: refund-agno") {
		t.Fatalf("after switch, YAML is not refund-agno; got: %q", string(td.YAML[:80]))
	}
}

func TestTemplates_Apply_RejectsUnknownSlug(t *testing.T) {
	t.Parallel()
	source := domain.NewMemorySource()
	router := newTemplatesRouter(t, source, &stubResetter{}, "ten-1")

	body := `{"slug":"does-not-exist"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/me/domain/template", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestTemplates_Apply_RejectsEmptySlug(t *testing.T) {
	t.Parallel()
	source := domain.NewMemorySource()
	router := newTemplatesRouter(t, source, &stubResetter{}, "ten-1")

	body := `{"slug":""}`
	req := httptest.NewRequest(http.MethodPost, "/v1/me/domain/template", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestTemplates_Apply_RejectsMalformedJSON(t *testing.T) {
	t.Parallel()
	source := domain.NewMemorySource()
	router := newTemplatesRouter(t, source, &stubResetter{}, "ten-1")

	req := httptest.NewRequest(http.MethodPost, "/v1/me/domain/template", strings.NewReader("not json"))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body=%q)", rec.Code, rec.Body.String())
	}
}

func TestTemplates_Apply_RequiresTenant(t *testing.T) {
	t.Parallel()
	source := domain.NewMemorySource()
	// Empty tenant on identity.
	router := newTemplatesRouter(t, source, &stubResetter{}, "")

	body := `{"slug":"cafe-ops"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/me/domain/template", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (body=%q)", rec.Code, rec.Body.String())
	}
}

func TestTemplates_Apply_LowercasesSlug(t *testing.T) {
	t.Parallel()
	source := domain.NewMemorySource()
	router := newTemplatesRouter(t, source, &stubResetter{}, "ten-1")

	body := `{"slug":"  Cafe-Ops "}`
	req := httptest.NewRequest(http.MethodPost, "/v1/me/domain/template", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%q)", rec.Code, rec.Body.String())
	}
}
