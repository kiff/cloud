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

// multiDomainSource is a fake domain.Source that holds an ordered
// list of domains for a single tenant — the shape MemorySource does
// not (it caps at one). It exists to exercise the multi-domain read
// projection at the handler level: list over
// many, and resolve {slug} across all of them.
type multiDomainSource struct {
	tenantID string
	domains  []domain.TenantDomain
}

func newMultiDomainSource(tenantID string, yamls ...string) *multiDomainSource {
	m := &multiDomainSource{tenantID: tenantID}
	for _, y := range yamls {
		id, slug := domain.DeriveStorageIdentity([]byte(y))
		m.domains = append(m.domains, domain.TenantDomain{
			TenantID: tenantID,
			YAML:     []byte(y),
			Version:  1,
			DomainID: id,
			Slug:     slug,
		})
	}
	return m
}

func (m *multiDomainSource) Load(_ context.Context, tenantID string) (domain.TenantDomain, error) {
	if tenantID != m.tenantID || len(m.domains) == 0 {
		return domain.TenantDomain{}, domain.ErrDomainNotFound
	}
	return m.domains[0], nil
}

func (m *multiDomainSource) LoadByDomain(_ context.Context, tenantID, domainID string) (domain.TenantDomain, error) {
	if tenantID != m.tenantID {
		return domain.TenantDomain{}, domain.ErrDomainNotFound
	}
	if domainID == "" && len(m.domains) > 0 {
		return m.domains[0], nil
	}
	for _, d := range m.domains {
		if d.DomainID == domainID {
			return d, nil
		}
	}
	return domain.TenantDomain{}, domain.ErrDomainNotFound
}

func (m *multiDomainSource) List(_ context.Context, tenantID string) ([]domain.TenantDomain, error) {
	if tenantID != m.tenantID {
		return []domain.TenantDomain{}, nil
	}
	return m.domains, nil
}

func (m *multiDomainSource) Save(_ context.Context, _ string, _ []byte) error { return nil }
func (m *multiDomainSource) Delete(_ context.Context, _ string) error         { return nil }

func (m *multiDomainSource) DeleteByDomain(_ context.Context, tenantID, domainID string) error {
	for i, d := range m.domains {
		if d.DomainID == domainID {
			m.domains = append(m.domains[:i:i], m.domains[i+1:]...)
			return nil
		}
	}
	return domain.ErrDomainNotFound
}

func (m *multiDomainSource) UpdateByDomain(_ context.Context, tenantID, domainID string, yaml []byte) error {
	for i := range m.domains {
		if m.domains[i].DomainID == domainID {
			m.domains[i].YAML = yaml
			m.domains[i].Version++
			return nil
		}
	}
	return domain.ErrDomainNotFound
}

func (m *multiDomainSource) Create(_ context.Context, tenantID string, yaml []byte) error {
	id, slug := domain.DeriveStorageIdentity(yaml)
	for _, d := range m.domains {
		if d.DomainID == id {
			return domain.ErrDomainExists
		}
	}
	m.domains = append(m.domains, domain.TenantDomain{
		TenantID: tenantID, YAML: yaml, Version: 1, DomainID: id, Slug: slug,
	})
	return nil
}

// secondValidYAML is a second domain that parses against the default
// registry, with a name distinct from validYAML's test-domain.
const secondValidYAML = `
domain: payments-domain
entity: Invoice
events:
  - RAISED
states:
  - OPEN
transitions:
  - on: RAISED
    from: ""
    to: OPEN
actions:
  - name: NOOP
    allowed_states: [OPEN]
    required_parameters: [opened_by]
    risk: low
    approval: never
    executor: cafe.start_shift
`

func newMultiDomainMux(t *testing.T, tenantID string, src domain.Source) *http.ServeMux {
	t.Helper()
	h := handlers.NewDomainsHandler(src, executors.DefaultRegistry())
	mux := http.NewServeMux()
	h.Register(mux)
	wrapped := http.NewServeMux()
	wrapped.Handle("/", withFakeIdentity(mux, auth.Identity{
		Tenant: tenantID, Subject: "test-subject", Roles: []string{"tenant_owner"},
	}))
	return wrapped
}

func TestDomainsList_MultipleDomains(t *testing.T) {
	t.Parallel()
	src := newMultiDomainSource("t1", validYAML, secondValidYAML)
	mux := newMultiDomainMux(t, "t1", src)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/me/domains", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Domains []struct {
			Name string `json:"name"`
		} `json:"domains"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Domains) != 2 {
		t.Fatalf("want 2 domains, got %d: %s", len(body.Domains), rec.Body.String())
	}
	if body.Domains[0].Name != "test-domain" {
		t.Errorf("domains[0].name = %q, want test-domain (default first)", body.Domains[0].Name)
	}
	if body.Domains[1].Name != "payments-domain" {
		t.Errorf("domains[1].name = %q, want payments-domain", body.Domains[1].Name)
	}
}

func TestDomainsGet_ResolvesNonDefaultBySlug(t *testing.T) {
	t.Parallel()
	src := newMultiDomainSource("t1", validYAML, secondValidYAML)
	mux := newMultiDomainMux(t, "t1", src)

	// The second (non-default) domain is reachable by its slug.
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/me/domains/payments-domain", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Name   string `json:"name"`
		Entity string `json:"entity"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Name != "payments-domain" || body.Entity != "Invoice" {
		t.Errorf("got name=%q entity=%q, want payments-domain/Invoice", body.Name, body.Entity)
	}

	// The default domain is still reachable by its slug.
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/me/domains/test-domain", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("default get status = %d, body = %s", rec.Code, rec.Body.String())
	}

	// An unknown slug 404s.
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/me/domains/nope", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown slug status = %d, want 404", rec.Code)
	}
}

// newCreateRig wires a DomainsHandler over a real (multi-domain)
// MemorySource plus a resetter and control store, so create tests
// exercise the full path: validate → Source.Create → reset → control
// record → 201. Returns the mux and the deps tests assert against.
func newCreateRig(t *testing.T, tenantID string) (*http.ServeMux, *domain.MemorySource, *fakeResetter, *domain.MemoryControlStore) {
	t.Helper()
	src := domain.NewMemorySource()
	reset := &fakeResetter{}
	controls := domain.NewMemoryControlStore()
	h := handlers.NewDomainsHandler(src, executors.DefaultRegistry(),
		handlers.WithDomainsControlStore(controls),
		handlers.WithDomainsRuntimeResetter(reset),
	)
	mux := http.NewServeMux()
	h.Register(mux)
	wrapped := http.NewServeMux()
	wrapped.Handle("/", withFakeIdentity(mux, auth.Identity{
		Tenant: tenantID, Subject: "test-subject", Roles: []string{"tenant_owner"},
	}))
	return wrapped, src, reset, controls
}

func TestDomainsCreate_AddsSecondDomain(t *testing.T) {
	t.Parallel()
	mux, src, reset, controls := newCreateRig(t, "t1")

	// The tenant's default domain already exists (created via activate).
	if err := src.Save(context.Background(), "t1", []byte(validYAML)); err != nil {
		t.Fatalf("seed default: %v", err)
	}

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/me/domains",
		strings.NewReader(secondValidYAML)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d, want 201; body = %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Domain string `json:"domain"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Domain != "payments-domain" {
		t.Errorf("created domain = %q, want payments-domain", body.Domain)
	}

	// Both domains now resolve through List.
	got, err := src.List(context.Background(), "t1")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("List after create: got %d, want 2", len(got))
	}
	if got[0].Slug != "test-domain" || got[1].Slug != "payments-domain" {
		t.Errorf("List order = [%q, %q], want [test-domain, payments-domain]", got[0].Slug, got[1].Slug)
	}

	// The resolver cache was reset so the new domain is picked up.
	if calls := reset.Calls(); len(calls) != 1 || calls[0] != "t1" {
		t.Errorf("ResetTenant calls = %v, want [t1]", calls)
	}

	// A control record was created for the new domain (best-effort
	// audit trail, same as activate).
	ctrls, err := controls.List(context.Background(), "t1")
	if err != nil {
		t.Fatalf("controls.List: %v", err)
	}
	foundPayments := false
	for _, c := range ctrls {
		if c.Domain == "payments-domain" {
			foundPayments = true
		}
	}
	if !foundPayments {
		t.Error("expected a control record for the created domain")
	}
}

func TestDomainsCreate_ConflictOnDuplicate(t *testing.T) {
	t.Parallel()
	mux, _, _, _ := newCreateRig(t, "t1")

	// First create succeeds.
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/me/domains",
		strings.NewReader(secondValidYAML)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("first create status = %d, want 201; body = %s", rec.Code, rec.Body.String())
	}

	// Re-creating the same domain is a 409, not an overwrite.
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/me/domains",
		strings.NewReader(secondValidYAML)))
	if rec.Code != http.StatusConflict {
		t.Fatalf("duplicate create status = %d, want 409; body = %s", rec.Code, rec.Body.String())
	}
}

func TestDomainsCreate_InvalidYAMLIs422(t *testing.T) {
	t.Parallel()
	mux, _, reset, _ := newCreateRig(t, "t1")

	// References an executor not in the registry → fails build.
	bad := strings.ReplaceAll(secondValidYAML, "cafe.start_shift", "tenant.unknown_executor")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/me/domains", strings.NewReader(bad)))
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid create status = %d, want 422; body = %s", rec.Code, rec.Body.String())
	}
	// Nothing was persisted, so the cache was never reset.
	if calls := reset.Calls(); len(calls) != 0 {
		t.Errorf("ResetTenant should not be called on a rejected create; got %v", calls)
	}
}

func TestDomainsCreate_EmptyBodyIs400(t *testing.T) {
	t.Parallel()
	mux, _, _, _ := newCreateRig(t, "t1")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/me/domains", strings.NewReader("   ")))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("empty create status = %d, want 400; body = %s", rec.Code, rec.Body.String())
	}
}

func TestDomainsDelete_RemovesNonDefault(t *testing.T) {
	t.Parallel()
	mux, src, reset, _ := newCreateRig(t, "t1")

	// Default domain plus a created non-default one.
	if err := src.Save(context.Background(), "t1", []byte(validYAML)); err != nil {
		t.Fatalf("seed default: %v", err)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/me/domains", strings.NewReader(secondValidYAML)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("seed second: status %d; body=%s", rec.Code, rec.Body.String())
	}

	// Delete the non-default domain by its slug.
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/v1/me/domains/payments-domain", nil))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d, want 204; body=%s", rec.Code, rec.Body.String())
	}

	// Only the default remains.
	got, err := src.List(context.Background(), "t1")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 || got[0].Slug != "test-domain" {
		t.Fatalf("after delete List = %+v, want [test-domain]", got)
	}
	// The runtime cache was reset (create + delete = 2 resets).
	if calls := reset.Calls(); len(calls) != 2 {
		t.Errorf("ResetTenant calls = %v, want 2 (create + delete)", calls)
	}
}

func TestDomainsDelete_DefaultIs409(t *testing.T) {
	t.Parallel()
	mux, src, _, _ := newCreateRig(t, "t1")
	if err := src.Save(context.Background(), "t1", []byte(validYAML)); err != nil {
		t.Fatalf("seed default: %v", err)
	}

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/v1/me/domains/test-domain", nil))
	if rec.Code != http.StatusConflict {
		t.Fatalf("delete default status = %d, want 409; body=%s", rec.Code, rec.Body.String())
	}
	// The default is still there.
	got, _ := src.List(context.Background(), "t1")
	if len(got) != 1 {
		t.Errorf("default must survive a 409 delete; List = %+v", got)
	}
}

func TestDomainsDelete_UnknownSlugIs404(t *testing.T) {
	t.Parallel()
	mux, src, _, _ := newCreateRig(t, "t1")
	if err := src.Save(context.Background(), "t1", []byte(validYAML)); err != nil {
		t.Fatalf("seed default: %v", err)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/v1/me/domains/nope", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("delete unknown status = %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
}

func TestDomainsCreate_MaxDomainsCapIs403(t *testing.T) {
	t.Parallel()
	src := domain.NewMemorySource()
	h := handlers.NewDomainsHandler(src, executors.DefaultRegistry(),
		// Cap of 1: the tenant's default domain already fills it.
		handlers.WithDomainsMaxResolver(func(context.Context, string) int { return 1 }),
	)
	mux := http.NewServeMux()
	h.Register(mux)
	wrapped := http.NewServeMux()
	wrapped.Handle("/", withFakeIdentity(mux, auth.Identity{
		Tenant: "t1", Subject: "test-subject", Roles: []string{"tenant_owner"},
	}))

	if err := src.Save(context.Background(), "t1", []byte(validYAML)); err != nil {
		t.Fatalf("seed default: %v", err)
	}

	rec := httptest.NewRecorder()
	wrapped.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/me/domains", strings.NewReader(secondValidYAML)))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("over-cap create status = %d, want 403; body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		MaxDomains int `json:"max_domains"`
		Count      int `json:"count"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.MaxDomains != 1 || body.Count != 1 {
		t.Errorf("over-cap body = %+v, want max_domains=1 count=1", body)
	}
	// Nothing was created.
	got, _ := src.List(context.Background(), "t1")
	if len(got) != 1 {
		t.Errorf("over-cap create must not add a domain; List = %d", len(got))
	}
}

func TestDomainsCreate_UnlimitedResolverAllows(t *testing.T) {
	t.Parallel()
	src := domain.NewMemorySource()
	h := handlers.NewDomainsHandler(src, executors.DefaultRegistry(),
		handlers.WithDomainsMaxResolver(func(context.Context, string) int { return 0 }), // 0 = unlimited
	)
	mux := http.NewServeMux()
	h.Register(mux)
	wrapped := http.NewServeMux()
	wrapped.Handle("/", withFakeIdentity(mux, auth.Identity{
		Tenant: "t1", Subject: "test-subject", Roles: []string{"tenant_owner"},
	}))

	if err := src.Save(context.Background(), "t1", []byte(validYAML)); err != nil {
		t.Fatalf("seed default: %v", err)
	}
	rec := httptest.NewRecorder()
	wrapped.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/me/domains", strings.NewReader(secondValidYAML)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("unlimited create status = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}
}

// secondValidYAMLEdited is secondValidYAML re-authored (an extra
// event/state/transition) but with the SAME domain name, so a
// per-domain update keeps the identity (slug) fixed.
const secondValidYAMLEdited = `
domain: payments-domain
entity: Invoice
events:
  - RAISED
  - PAID
states:
  - OPEN
  - CLOSED
transitions:
  - on: RAISED
    from: ""
    to: OPEN
  - on: PAID
    from: OPEN
    to: CLOSED
actions:
  - name: NOOP
    allowed_states: [OPEN]
    required_parameters: [opened_by]
    risk: low
    approval: never
    executor: cafe.start_shift
`

func TestDomainsUpdate_InPlaceBumpsVersion(t *testing.T) {
	t.Parallel()
	mux, src, reset, controls := newCreateRig(t, "t1")

	// Default + a created non-default domain.
	if err := src.Save(context.Background(), "t1", []byte(validYAML)); err != nil {
		t.Fatalf("seed default: %v", err)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/me/domains", strings.NewReader(secondValidYAML)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("seed second: %d; %s", rec.Code, rec.Body.String())
	}

	// Re-author the second domain in place (same name).
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/v1/me/domains/payments-domain", strings.NewReader(secondValidYAMLEdited)))
	if rec.Code != http.StatusOK {
		t.Fatalf("update status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	// Version bumped; identity preserved.
	td, err := src.LoadByDomain(context.Background(), "t1", "payments-domain")
	if err != nil {
		t.Fatalf("LoadByDomain: %v", err)
	}
	if td.Version != 2 {
		t.Errorf("version = %d, want 2 after update", td.Version)
	}
	if td.Slug != "payments-domain" {
		t.Errorf("slug = %q, want payments-domain (unchanged)", td.Slug)
	}
	if !strings.Contains(string(td.YAML), "PAID") {
		t.Errorf("stored yaml was not updated to the edited contract")
	}
	// Runtime cache reset on create + update.
	if calls := reset.Calls(); len(calls) != 2 {
		t.Errorf("ResetTenant calls = %v, want 2 (create + update)", calls)
	}
	// The re-activation control supersedes only payments-domain's
	// prior control (domain-scoped lineage): v2 active.
	ctrls, _ := controls.List(context.Background(), "t1")
	var paymentsActiveVersion int
	for _, c := range ctrls {
		if c.Domain == "payments-domain" && c.Status == "active" {
			paymentsActiveVersion = c.Version
		}
	}
	if paymentsActiveVersion != 2 {
		t.Errorf("payments-domain active control version = %d, want 2", paymentsActiveVersion)
	}
}

func TestDomainsUpdate_NameChangeIs409(t *testing.T) {
	t.Parallel()
	mux, src, _, _ := newCreateRig(t, "t1")
	if err := src.Save(context.Background(), "t1", []byte(validYAML)); err != nil {
		t.Fatalf("seed default: %v", err)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/me/domains", strings.NewReader(secondValidYAML)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("seed second: %d", rec.Code)
	}

	// PUT a different-named domain to payments-domain's route → rename,
	// refused here.
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/v1/me/domains/payments-domain", strings.NewReader(validYAML)))
	if rec.Code != http.StatusConflict {
		t.Fatalf("name-change update status = %d, want 409; body=%s", rec.Code, rec.Body.String())
	}
}

func TestDomainsUpdate_UnknownSlugIs404(t *testing.T) {
	t.Parallel()
	mux, src, _, _ := newCreateRig(t, "t1")
	if err := src.Save(context.Background(), "t1", []byte(validYAML)); err != nil {
		t.Fatalf("seed default: %v", err)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/v1/me/domains/nope", strings.NewReader(secondValidYAML)))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("update unknown status = %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
}

func TestDomainsUpdate_InvalidYAMLIs422(t *testing.T) {
	t.Parallel()
	mux, src, _, _ := newCreateRig(t, "t1")
	if err := src.Save(context.Background(), "t1", []byte(validYAML)); err != nil {
		t.Fatalf("seed default: %v", err)
	}
	bad := strings.ReplaceAll(validYAML, "cafe.start_shift", "tenant.unknown_executor")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/v1/me/domains/test-domain", strings.NewReader(bad)))
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid update status = %d, want 422; body=%s", rec.Code, rec.Body.String())
	}
}

func TestDomainsYAML_ReturnsStoredYAML(t *testing.T) {
	t.Parallel()
	src := newMultiDomainSource("t1", validYAML, secondValidYAML)
	mux := newMultiDomainMux(t, "t1", src)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/me/domains/payments-domain/yaml", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("yaml read status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "domain: payments-domain") {
		t.Errorf("yaml read body did not return the stored contract; got %s", rec.Body.String())
	}

	// Unknown slug → 404.
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/me/domains/nope/yaml", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown yaml read status = %d, want 404", rec.Code)
	}
}

// typedParamsYAML declares one summable parameter and one that is not,
// which is the distinction the mandate form has to make before it can
// offer a quantity to count.
const typedParamsYAML = `
domain: typed-domain
entity: Order
events:
  - RAISED
states:
  - OPEN
transitions:
  - on: RAISED
    from: ""
    to: OPEN
actions:
  - name: NOOP
    allowed_states: [OPEN]
    parameters:
      - {name: amount_minor, type: int, min: 1}
      - {name: reason, type: string, max_length: 200}
    risk: low
    approval: never
    executor: cafe.start_shift
`

// A mandate may sum a parameter only where every granted action
// declares it numeric, and that rule is enforced at issue time by the
// API. A surface that lets someone pick a quantity therefore needs the
// declared types, or it can only offer guesses and relay refusals.
func TestDomainDetailCarriesDeclaredParameterTypes(t *testing.T) {
	t.Parallel()
	src := newMultiDomainSource("t1", typedParamsYAML)
	mux := newMultiDomainMux(t, "t1", src)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/me/domains/typed-domain", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Actions []struct {
			Name       string `json:"name"`
			Parameters []struct {
				Name string `json:"name"`
				Type string `json:"type"`
			} `json:"parameters"`
		} `json:"actions"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Actions) != 1 {
		t.Fatalf("want 1 action, got %d: %s", len(body.Actions), rec.Body.String())
	}
	got := map[string]string{}
	for _, p := range body.Actions[0].Parameters {
		got[p.Name] = p.Type
	}
	if got["amount_minor"] != "int" {
		t.Errorf("amount_minor type = %q, want int — without it nothing can tell a summable parameter from a label", got["amount_minor"])
	}
	if got["reason"] != "string" {
		t.Errorf("reason type = %q, want string", got["reason"])
	}
}

// A domain that declares no typed parameters must not grow a
// "parameters": [] into its projection: the same projection is the
// proof pack's action list, whose bytes are pinned.
func TestDomainDetailOmitsParametersWhenNoneAreDeclared(t *testing.T) {
	t.Parallel()
	src := newMultiDomainSource("t1", secondValidYAML)
	mux := newMultiDomainMux(t, "t1", src)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/me/domains/payments-domain", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), `"parameters"`) {
		t.Errorf("untyped domain grew a parameters key: %s", rec.Body.String())
	}
}

// recordingSource notes whether a create went through Save (the
// tenant's default domain) or Create (an additional one).
type recordingSource struct {
	*domain.MemorySource
	mu    sync.Mutex
	calls []string
}

func (s *recordingSource) Save(ctx context.Context, tenantID string, yaml []byte) error {
	s.mu.Lock()
	s.calls = append(s.calls, "save")
	s.mu.Unlock()
	return s.MemorySource.Save(ctx, tenantID, yaml)
}

func (s *recordingSource) Create(ctx context.Context, tenantID string, yaml []byte) error {
	s.mu.Lock()
	s.calls = append(s.calls, "create")
	s.mu.Unlock()
	return s.MemorySource.Create(ctx, tenantID, yaml)
}

func newTemplateCreateRig(t *testing.T) (*http.ServeMux, *recordingSource) {
	t.Helper()
	src := &recordingSource{MemorySource: domain.NewMemorySource()}
	h := handlers.NewDomainsHandler(src, executors.DefaultRegistry(),
		handlers.WithDomainsControlStore(domain.NewMemoryControlStore()))
	mux := http.NewServeMux()
	h.Register(mux)
	wrapped := http.NewServeMux()
	wrapped.Handle("/", withFakeIdentity(mux, auth.Identity{
		Tenant: "t1", Subject: "test-subject", Roles: []string{"tenant_owner"},
	}))
	return wrapped, src
}

func TestDomainsCreate_FromTemplate(t *testing.T) {
	t.Parallel()
	mux, src := newTemplateCreateRig(t)
	post := func(target string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, target, nil))
		return rec
	}

	// A tenant's first domain must be its default, or the default-domain
	// paths have nothing to resolve.
	rec := post("/v1/me/domains?template=card-refund")
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"card-refund"`) {
		t.Fatalf("first template create = %d %s, want 201 card-refund", rec.Code, rec.Body.String())
	}
	// A later one is added alongside, never replacing the first.
	if rec := post("/v1/me/domains?template=retention-credit"); rec.Code != http.StatusCreated {
		t.Fatalf("second template create = %d %s, want 201", rec.Code, rec.Body.String())
	}
	if got := strings.Join(src.calls, ","); got != "save,create" {
		t.Errorf("persist calls = %s, want save,create", got)
	}
	got, _ := src.List(context.Background(), "t1")
	if len(got) != 2 || got[0].Slug != "card-refund" || got[1].Slug != "retention-credit" {
		t.Errorf("domains after two template creates = %+v", got)
	}

	if rec := post("/v1/me/domains?template=card-refund"); rec.Code != http.StatusConflict {
		t.Errorf("repeat template create = %d, want 409", rec.Code)
	}
	if rec := post("/v1/me/domains?template=no-such-template"); rec.Code != http.StatusNotFound {
		t.Errorf("unknown template = %d, want 404", rec.Code)
	}
}

func TestDomainsGet_SaysWhichActionsRequireACard(t *testing.T) {
	t.Parallel()
	mux, _ := newTemplateCreateRig(t)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/me/domains?template=card-refund", nil))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/me/domains/card-refund", nil))
	var body struct {
		Actions []struct {
			Name              string `json:"name"`
			RequiresAuthority bool   `json:"requires_authority"`
		} `json:"actions"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v; %s", err, rec.Body.String())
	}
	got := map[string]bool{}
	for _, a := range body.Actions {
		got[a.Name] = a.RequiresAuthority
	}
	if !got["REFUND_ORDER"] || got["ISSUE_CREDIT"] {
		t.Errorf("requires_authority = %v, want REFUND_ORDER only", got)
	}
}

// Saving a domain in place keeps its governance posture. It used to
// create the new control in observe, so re-saving an enforced domain
// silently made its refusals advisory. A domain's first activation still
// starts in observe; a control with no explicit posture stays unset.
func TestDomainsUpdate_KeepsThePosture(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	mux, src, _, controls := newCreateRig(t, "t1")
	if err := src.Save(ctx, "t1", []byte(validYAML)); err != nil {
		t.Fatalf("seed default: %v", err)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/me/domains", strings.NewReader(secondValidYAML)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d: %s", rec.Code, rec.Body.String())
	}
	active := func() domain.Control {
		t.Helper()
		list, err := controls.List(ctx, "t1")
		if err != nil {
			t.Fatalf("controls.List: %v", err)
		}
		for _, c := range list {
			if c.Domain == "payments-domain" && c.Status == "active" {
				return c
			}
		}
		t.Fatal("no active control for payments-domain")
		return domain.Control{}
	}
	save := func() {
		t.Helper()
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/v1/me/domains/payments-domain", strings.NewReader(secondValidYAML)))
		if rec.Code != http.StatusOK {
			t.Fatalf("update = %d: %s", rec.Code, rec.Body.String())
		}
	}

	first := active()
	if first.Mode != domain.ControlModeObserve {
		t.Fatalf("first activation mode = %q, want observe", first.Mode)
	}
	if err := controls.SetMode(ctx, "t1", first.ID, domain.ControlModeEnforce); err != nil {
		t.Fatalf("SetMode: %v", err)
	}
	save()
	if got := active(); got.ID == first.ID || got.Mode != domain.ControlModeEnforce {
		t.Fatalf("after an in-place save: control %s mode %q, want a new control in enforce", got.ID, got.Mode)
	}

	if err := controls.SetMode(ctx, "t1", active().ID, ""); err != nil {
		t.Fatalf("SetMode unset: %v", err)
	}
	save()
	if got := active(); got.Mode != "" {
		t.Errorf("a control following the workspace default must keep following it, got %q", got.Mode)
	}
}
