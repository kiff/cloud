package handlers_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/domain"
	"github.com/kiff/cloud/api/internal/executors"
	"github.com/kiff/cloud/api/internal/handlers"
	"github.com/kiff/cloud/api/internal/proposals"
)

// newDomainsTestRig wires a DomainsHandler against in-memory deps
// and returns the mux plus the stores so tests can seed them. The
// supplied tenant id is the one IdentityFrom returns. A nil reader
// exercises the "agents unavailable" path.
func newDomainsTestRig(t *testing.T, tenantID string, reader proposals.Reader) (*http.ServeMux, *domain.MemorySource, *domain.MemoryControlStore) {
	t.Helper()
	src := domain.NewMemorySource()
	controls := domain.NewMemoryControlStore()
	opts := []handlers.DomainsOption{
		handlers.WithDomainsControlStore(controls),
	}
	if reader != nil {
		opts = append(opts, handlers.WithDomainsDecisionsReader(reader))
	}
	h := handlers.NewDomainsHandler(src, executors.DefaultRegistry(), opts...)
	mux := http.NewServeMux()
	h.Register(mux)
	wrapped := http.NewServeMux()
	// The dashboard owner authenticates via session and carries
	// tenant_owner (auth.sessionOwnerRoles). Model that here so the
	// mutation routes (create/update/delete), which require the
	// management role, are exercised as the real caller would.
	wrapped.Handle("/", withFakeIdentity(mux, auth.Identity{
		Tenant: tenantID, Subject: "test-subject", Roles: []string{"tenant_owner"},
	}))
	return wrapped, src, controls
}

// seedActiveControl persists an active control for tenant on the
// test domain and returns its id, so decision rows can be scoped.
func seedActiveControl(t *testing.T, controls *domain.MemoryControlStore, tenantID, controlID string) string {
	t.Helper()
	err := controls.Create(context.Background(), domain.Control{
		ID:          controlID,
		TenantID:    tenantID,
		Domain:      "test-domain",
		Entity:      "TestEntity",
		Actions:     []string{"NOOP"},
		ActivatedAt: time.Now().UTC().Add(-time.Hour),
		ActivatedBy: "test-subject",
	})
	if err != nil {
		t.Fatalf("seed control: %v", err)
	}
	return controlID
}

func TestDomainsList_EmptyWhenNoDomain(t *testing.T) {
	t.Parallel()
	mux, _, _ := newDomainsTestRig(t, "t1", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/me/domains", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Domains []map[string]any `json:"domains"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Domains == nil {
		t.Fatal("domains must be an empty array, not null")
	}
	if len(body.Domains) != 0 {
		t.Fatalf("want 0 domains, got %d", len(body.Domains))
	}
}

func TestDomainsList_OneDomain(t *testing.T) {
	t.Parallel()
	mux, src, controls := newDomainsTestRig(t, "t1", nil)
	if err := src.Save(context.Background(), "t1", []byte(validYAML)); err != nil {
		t.Fatalf("save: %v", err)
	}
	seedActiveControl(t, controls, "t1", "ctrl-1")

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/me/domains", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Domains []struct {
			Name        string     `json:"name"`
			Entity      string     `json:"entity"`
			Version     int        `json:"version"`
			Status      string     `json:"status"`
			ActionCount int        `json:"action_count"`
			ActivatedAt *time.Time `json:"activated_at"`
		} `json:"domains"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Domains) != 1 {
		t.Fatalf("want 1 domain, got %d", len(body.Domains))
	}
	d := body.Domains[0]
	if d.Name != "test-domain" || d.Entity != "TestEntity" {
		t.Errorf("name/entity = %q/%q", d.Name, d.Entity)
	}
	if d.Version != 1 {
		t.Errorf("version = %d, want 1", d.Version)
	}
	if d.ActionCount != 1 {
		t.Errorf("action_count = %d, want 1", d.ActionCount)
	}
	if d.Status != "active" {
		t.Errorf("status = %q, want active", d.Status)
	}
	if d.ActivatedAt == nil {
		t.Error("activated_at should be set from the active control")
	}
}

func TestDomainsGet_SingleDomainShape(t *testing.T) {
	t.Parallel()
	mux, src, controls := newDomainsTestRig(t, "t1", nil)
	if err := src.Save(context.Background(), "t1", []byte(validYAML)); err != nil {
		t.Fatalf("save: %v", err)
	}
	seedActiveControl(t, controls, "t1", "ctrl-1")

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/me/domains/test-domain", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Name    string `json:"name"`
		Actions []struct {
			Name          string   `json:"name"`
			AllowedStates []string `json:"allowed_states"`
			Executor      string   `json:"executor"`
		} `json:"actions"`
		Lifecycle struct {
			States      []string `json:"states"`
			Events      []string `json:"events"`
			Transitions []struct {
				On string `json:"on"`
				To string `json:"to"`
			} `json:"transitions"`
		} `json:"lifecycle"`
		Evidence struct {
			ControlID string `json:"control_id"`
		} `json:"evidence"`
		Agents struct {
			Status string `json:"status"`
		} `json:"agents"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Name != "test-domain" {
		t.Errorf("name = %q", body.Name)
	}
	if len(body.Actions) != 1 || body.Actions[0].Name != "NOOP" {
		t.Fatalf("actions = %+v", body.Actions)
	}
	if body.Actions[0].Executor != "cafe.start_shift" {
		t.Errorf("executor = %q", body.Actions[0].Executor)
	}
	if len(body.Lifecycle.States) != 1 || body.Lifecycle.States[0] != "NEW" {
		t.Errorf("states = %v", body.Lifecycle.States)
	}
	if len(body.Lifecycle.Transitions) != 1 || body.Lifecycle.Transitions[0].To != "NEW" {
		t.Errorf("transitions = %+v", body.Lifecycle.Transitions)
	}
	if body.Evidence.ControlID != "ctrl-1" {
		t.Errorf("evidence.control_id = %q, want ctrl-1", body.Evidence.ControlID)
	}
	// No decisions reader wired in this rig -> agents unavailable.
	if body.Agents.Status != "unavailable" {
		t.Errorf("agents.status = %q, want unavailable", body.Agents.Status)
	}
}

func TestDomainsGet_NotFoundWrongSlug(t *testing.T) {
	t.Parallel()
	mux, src, _ := newDomainsTestRig(t, "t1", nil)
	if err := src.Save(context.Background(), "t1", []byte(validYAML)); err != nil {
		t.Fatalf("save: %v", err)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/me/domains/other-domain", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body = %s", rec.Code, rec.Body.String())
	}
}

func TestDomainsGet_NotFoundNoDomain(t *testing.T) {
	t.Parallel()
	mux, _, _ := newDomainsTestRig(t, "t1", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/me/domains/test-domain", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body = %s", rec.Code, rec.Body.String())
	}
}

func TestDomains_Unauthorized(t *testing.T) {
	t.Parallel()
	// Rig with no identity middleware: register the handler bare.
	src := domain.NewMemorySource()
	h := handlers.NewDomainsHandler(src, executors.DefaultRegistry())
	mux := http.NewServeMux()
	h.Register(mux)

	for _, path := range []string{"/v1/me/domains", "/v1/me/domains/test-domain"} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s status = %d, want 401", path, rec.Code)
		}
	}
}

func TestDomainsGet_ObservedAgents(t *testing.T) {
	t.Parallel()
	reader := proposals.NewMemoryReader()
	mux, src, controls := newDomainsTestRig(t, "t1", reader)
	if err := src.Save(context.Background(), "t1", []byte(validYAML)); err != nil {
		t.Fatalf("save: %v", err)
	}
	seedActiveControl(t, controls, "t1", "ctrl-1")

	now := time.Now().UTC()
	// Two decisions from the same api key (should dedupe to one agent
	// with 2 operations), one from a different actor, and one scoped
	// to a DIFFERENT control (must be excluded).
	reader.Append("t1", proposals.DecisionRow{
		ProposalID: "p1", ControlID: "ctrl-1", PrincipalAPIKeyID: "key-A",
		Outcome: "allowed", CreatedAt: now.Add(-2 * time.Hour),
	})
	reader.Append("t1", proposals.DecisionRow{
		ProposalID: "p2", ControlID: "ctrl-1", PrincipalAPIKeyID: "key-A",
		Outcome: "blocked", CreatedAt: now.Add(-1 * time.Hour),
	})
	reader.Append("t1", proposals.DecisionRow{
		ProposalID: "p3", ControlID: "ctrl-1", ActorID: "actor-Z",
		Outcome: "allowed", CreatedAt: now.Add(-30 * time.Minute),
	})
	reader.Append("t1", proposals.DecisionRow{
		ProposalID: "p4", ControlID: "ctrl-OTHER", PrincipalAPIKeyID: "key-Q",
		Outcome: "allowed", CreatedAt: now.Add(-10 * time.Minute),
	})

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/me/domains/test-domain", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Agents struct {
			Status string `json:"status"`
			Window *struct {
				From time.Time `json:"from"`
				To   time.Time `json:"to"`
			} `json:"window"`
			Agents []struct {
				ID         string `json:"id"`
				Kind       string `json:"kind"`
				Operations int    `json:"operations"`
			} `json:"agents"`
		} `json:"agents"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Agents.Status != "observed" {
		t.Fatalf("agents.status = %q, want observed", body.Agents.Status)
	}
	if body.Agents.Window == nil {
		t.Fatal("observed agents must carry a window")
	}
	if len(body.Agents.Agents) != 2 {
		t.Fatalf("want 2 distinct agents (key-A, actor-Z), got %d: %+v", len(body.Agents.Agents), body.Agents.Agents)
	}
	// key-A is newest-seen at -30m? No: actor-Z is newest. Order is
	// newest-seen first, so actor-Z leads, key-A follows.
	var keyA *struct {
		ID         string
		Kind       string
		Operations int
	}
	for _, a := range body.Agents.Agents {
		if a.ID == "key-A" {
			keyA = &struct {
				ID         string
				Kind       string
				Operations int
			}{a.ID, a.Kind, a.Operations}
		}
		if a.ID == "key-Q" {
			t.Errorf("agent from a different control leaked in: %+v", a)
		}
	}
	if keyA == nil {
		t.Fatal("key-A agent missing")
	}
	if keyA.Kind != "api_key" {
		t.Errorf("key-A kind = %q, want api_key", keyA.Kind)
	}
	if keyA.Operations != 2 {
		t.Errorf("key-A operations = %d, want 2", keyA.Operations)
	}
}

// newDomainsRigWithIdentity wires a DomainsHandler behind a synthetic
// auth middleware that stamps the supplied identity onto every
// request, so a test can exercise the management-role gate with a
// non-owner caller (e.g. a plain runtime API key).
func newDomainsRigWithIdentity(t *testing.T, id auth.Identity) (*http.ServeMux, *domain.MemorySource) {
	t.Helper()
	src := domain.NewMemorySource()
	controls := domain.NewMemoryControlStore()
	h := handlers.NewDomainsHandler(src, executors.DefaultRegistry(),
		handlers.WithDomainsControlStore(controls))
	mux := http.NewServeMux()
	h.Register(mux)
	wrapped := http.NewServeMux()
	wrapped.Handle("/", withFakeIdentity(mux, id))
	return wrapped, src
}

// TestDomainsMutations_RequireManagementRole asserts that a caller
// without the tenant_owner role — the shape of a plain runtime API
// key (the agent's decide credential) — is refused on every domain
// mutation with 403, even though it authenticates to a valid tenant.
// This is the credential-separation guarantee: a runtime key
// proposes actions against a domain, it cannot rewrite the domain
// that governs it.
func TestDomainsMutations_RequireManagementRole(t *testing.T) {
	t.Parallel()
	runtimeKey := auth.Identity{
		Tenant:  "t1",
		Subject: "kiff_live_t1_runtime",
		Method:  auth.MethodAPIKey,
		APIKey:  true,
		Roles:   []string{"ops_agent"}, // a runtime role, not management
	}
	mux, src := newDomainsRigWithIdentity(t, runtimeKey)
	// Seed a domain so update/delete resolve a real target before the
	// role gate would even matter — the gate must fire regardless.
	if err := src.Save(context.Background(), "t1", []byte(validYAML)); err != nil {
		t.Fatalf("seed: %v", err)
	}

	cases := []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{"create", http.MethodPost, "/v1/me/domains", validYAML},
		{"update", http.MethodPut, "/v1/me/domains/test-domain", validYAML},
		{"delete", http.MethodDelete, "/v1/me/domains/test-domain", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var body io.Reader
			if tc.body != "" {
				body = strings.NewReader(tc.body)
			}
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, body))
			if rec.Code != http.StatusForbidden {
				t.Fatalf("%s %s: status = %d, want 403; body = %s",
					tc.method, tc.path, rec.Code, rec.Body.String())
			}
		})
	}
}

// TestDomainsCreate_OwnerSucceeds confirms the tenant_owner (the
// dashboard session / a key minted with the management role) passes
// the gate and creates a domain.
func TestDomainsCreate_OwnerSucceeds(t *testing.T) {
	t.Parallel()
	// The default rig already carries tenant_owner.
	mux, _, _ := newDomainsTestRig(t, "t1", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/me/domains", strings.NewReader(validYAML)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body = %s", rec.Code, rec.Body.String())
	}
}

// TestDomainsCreate_AdminSucceeds confirms the "admin" role — the
// bootstrap/superuser role a fresh tenant's first key carries (and the
// role solo Clerk users and the Playground demo key hold) — also passes
// the authoring gate. Admin is strictly more privileged than
// tenant_owner, so refusing it was the bug behind the Playground's 403
// on every template apply.
func TestDomainsCreate_AdminSucceeds(t *testing.T) {
	t.Parallel()
	adminKey := auth.Identity{
		Tenant:  "t1",
		Subject: "kiff_live_t1_admin",
		Method:  auth.MethodAPIKey,
		APIKey:  true,
		Roles:   []string{"admin"},
	}
	mux, _ := newDomainsRigWithIdentity(t, adminKey)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/me/domains", strings.NewReader(validYAML)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("admin should pass the authoring gate: status = %d, want 201; body = %s", rec.Code, rec.Body.String())
	}
}

// the agent-as-unit design: verified decisions (a key bound to the actor) group under
// the agent, with the keys they came through as via_keys. An actor an
// unbound key merely claimed is not shown as that agent: the row stays
// under the key that made it.
func TestDomainsGet_ObservedAgentsGroupByVerifiedAgent(t *testing.T) {
	t.Parallel()
	reader := proposals.NewMemoryReader()
	mux, src, controls := newDomainsTestRig(t, "t1", reader)
	if err := src.Save(context.Background(), "t1", []byte(validYAML)); err != nil {
		t.Fatalf("save: %v", err)
	}
	seedActiveControl(t, controls, "t1", "ctrl-1")
	now := time.Now().UTC()
	for i, key := range []string{"key-B", "key-C", "key-B"} {
		reader.Append("t1", proposals.DecisionRow{
			ProposalID: "p" + string(rune('a'+i)), ControlID: "ctrl-1", ActorID: "refund-agent", ActorVerified: true,
			PrincipalAPIKeyID: key, Outcome: "allowed", CreatedAt: now.Add(-time.Duration(i+1) * time.Minute),
		})
	}
	// An unbound key claiming the same name on an action with no card.
	reader.Append("t1", proposals.DecisionRow{
		ProposalID: "claim", ControlID: "ctrl-1", ActorID: "refund-agent",
		PrincipalAPIKeyID: "key-unbound", Outcome: "allowed", CreatedAt: now.Add(-10 * time.Minute),
	})

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/me/domains/test-domain", nil))
	var body struct {
		Agents struct {
			Agents []domainAgentViewForTest `json:"agents"`
		} `json:"agents"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v; %s", err, rec.Body.String())
	}
	byID := map[string]domainAgentViewForTest{}
	for _, a := range body.Agents.Agents {
		byID[a.ID] = a
	}
	agent := byID["refund-agent"]
	if agent.Kind != "agent" || agent.Operations != 3 {
		t.Errorf("verified agent = %+v, want refund-agent (agent) with 3 operations", agent)
	}
	if len(agent.ViaKeys) != 2 || agent.ViaKeys[0] != "key-B" || agent.ViaKeys[1] != "key-C" {
		t.Errorf("via_keys = %v, want [key-B key-C] (not the unbound key)", agent.ViaKeys)
	}
	if claim := byID["key-unbound"]; claim.Kind != "api_key" || claim.Operations != 1 {
		t.Errorf("the unbound key's claim = %+v, want its own api_key entry", claim)
	}
}

type domainAgentViewForTest struct {
	ID         string   `json:"id"`
	Kind       string   `json:"kind"`
	Operations int      `json:"operations"`
	ViaKeys    []string `json:"via_keys"`
}
