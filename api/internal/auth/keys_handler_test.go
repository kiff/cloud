package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kiff/kiff/pkg/kiff/actor"
)

// keysServer assembles a real HTTP test server with the
// middleware + keys handler wired through. Returns the server, the
// store (so tests can read it back), and a "Bearer …" token that
// authenticates as a tenant-admin.
func keysServer(t *testing.T, tenant string) (*httptest.Server, *MemoryAPIKeyStore, string) {
	return keysServerAs(t, tenant, "admin-subject", []string{"admin"})
}

// keysServerAs is the explicit-role variant. The caller picks the
// subject + roles the synthetic auth resolver attaches to the
// inbound request, so tests can drive the role-subset check from
// non-admin callers.
func keysServerAs(t *testing.T, tenant, subject string, roles []string) (*httptest.Server, *MemoryAPIKeyStore, string) {
	t.Helper()

	store := NewMemoryAPIKeyStore()
	resolver := NewMapResolver()
	resolver.Set(subject, actor.Actor{ID: subject + "-actor", Roles: append([]string(nil), roles...)}, tenant)

	provider := NewStubProvider(resolver)
	clk := time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC)
	mw := NewMiddleware(provider, store, resolver, WithClock(fixedClock(clk)))
	keys := NewKeysHandler(store, WithKeysClock(fixedClock(clk)))

	mux := http.NewServeMux()
	keys.Register(mux)

	srv := httptest.NewServer(mw.Wrap(mux))
	t.Cleanup(srv.Close)

	return srv, store, "Bearer " + subject
}

func doJSON(t *testing.T, method, url, auth string, body any) (*http.Response, []byte) {
	t.Helper()
	var bodyReader io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		bodyReader = bytes.NewReader(buf)
	}
	req, err := http.NewRequest(method, url, bodyReader)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return resp, raw
}

func TestKeysHandler_Create(t *testing.T) {
	t.Parallel()
	srv, store, token := keysServer(t, "tenant-acme")

	resp, raw := doJSON(t, "POST", srv.URL+"/v1/keys", token, map[string]any{
		"label": "loader",
		"roles": []string{"operator"},
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status: got %d, want 201; body=%s", resp.StatusCode, raw)
	}

	var got CreateResponse
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v body=%s", err, raw)
	}
	if !strings.HasPrefix(got.Plaintext, APIKeyPrefix+"tenant-acme_") {
		t.Errorf("plaintext should embed tenant: %q", got.Plaintext)
	}
	if got.Tenant != "tenant-acme" {
		t.Errorf("Tenant: got %q, want tenant-acme", got.Tenant)
	}
	if got.Subject != "admin-subject" {
		t.Errorf("Subject: got %q, want admin-subject", got.Subject)
	}
	if got.Label != "loader" {
		t.Errorf("Label: got %q, want loader", got.Label)
	}

	// Confirm the record landed in the store and is retrievable by hash.
	parsed, err := ParseAPIKey(got.Plaintext)
	if err != nil {
		t.Fatalf("ParseAPIKey: %v", err)
	}
	rec, err := store.GetByHash(context.Background(), parsed.Hash)
	if err != nil {
		t.Fatalf("GetByHash: %v", err)
	}
	if rec.ID != got.ID {
		t.Errorf("stored record id mismatch: got %q, want %q", rec.ID, got.ID)
	}
}

func TestKeysHandler_Create_RejectsInvalidRole(t *testing.T) {
	t.Parallel()
	srv, _, token := keysServer(t, "tenant-acme")

	resp, raw := doJSON(t, "POST", srv.URL+"/v1/keys", token, map[string]any{
		"roles": []string{"Bad Role"},
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status: got %d, want 400; body=%s", resp.StatusCode, raw)
	}
}

func TestKeysHandler_Create_Unauthenticated(t *testing.T) {
	t.Parallel()
	srv, _, _ := keysServer(t, "tenant-acme")
	resp, _ := doJSON(t, "POST", srv.URL+"/v1/keys", "", nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status: got %d, want 401", resp.StatusCode)
	}
}

func TestKeysHandler_List(t *testing.T) {
	t.Parallel()
	srv, _, token := keysServer(t, "tenant-acme")

	// Create two.
	for _, label := range []string{"first", "second"} {
		resp, _ := doJSON(t, "POST", srv.URL+"/v1/keys", token, map[string]any{"label": label})
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("create %q failed: %d", label, resp.StatusCode)
		}
	}

	resp, raw := doJSON(t, "GET", srv.URL+"/v1/keys", token, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list status: got %d, want 200; body=%s", resp.StatusCode, raw)
	}
	var body struct {
		Keys []ListItem `json:"keys"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("unmarshal: %v body=%s", err, raw)
	}
	if len(body.Keys) != 2 {
		t.Fatalf("expected 2 keys, got %d", len(body.Keys))
	}
	for _, it := range body.Keys {
		if it.Subject != "admin-subject" {
			t.Errorf("Subject: got %q, want admin-subject", it.Subject)
		}
	}
}

func TestKeysHandler_Revoke(t *testing.T) {
	t.Parallel()
	srv, store, token := keysServer(t, "tenant-acme")

	resp, raw := doJSON(t, "POST", srv.URL+"/v1/keys", token, map[string]any{"label": "x"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create failed: %d body=%s", resp.StatusCode, raw)
	}
	var created CreateResponse
	_ = json.Unmarshal(raw, &created)

	resp, raw = doJSON(t, "DELETE", srv.URL+"/v1/keys/"+created.ID, token, nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("revoke status: got %d, want 204; body=%s", resp.StatusCode, raw)
	}

	parsed, _ := ParseAPIKey(created.Plaintext)
	_, err := store.GetByHash(context.Background(), parsed.Hash)
	if !errors.Is(err, ErrAPIKeyRevoked) {
		t.Fatalf("after revoke, GetByHash should return ErrAPIKeyRevoked; got %v", err)
	}
}

func TestKeysHandler_Revoke_NotFound_OutsideTenant(t *testing.T) {
	t.Parallel()
	srv, _, token := keysServer(t, "tenant-acme")

	resp, _ := doJSON(t, "DELETE", srv.URL+"/v1/keys/some-other-tenant-id", token, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status: got %d, want 404", resp.StatusCode)
	}
}

func TestKeysHandler_Revoke_Idempotent(t *testing.T) {
	t.Parallel()
	srv, _, token := keysServer(t, "tenant-acme")

	resp, raw := doJSON(t, "POST", srv.URL+"/v1/keys", token, map[string]any{})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create failed: %d body=%s", resp.StatusCode, raw)
	}
	var created CreateResponse
	_ = json.Unmarshal(raw, &created)

	resp1, _ := doJSON(t, "DELETE", srv.URL+"/v1/keys/"+created.ID, token, nil)
	resp2, _ := doJSON(t, "DELETE", srv.URL+"/v1/keys/"+created.ID, token, nil)
	if resp1.StatusCode != http.StatusNoContent {
		t.Fatalf("first revoke: got %d, want 204", resp1.StatusCode)
	}
	// Once revoked, ListActive no longer surfaces the key, so the
	// handler returns 404 on the second attempt — which is the
	// right caller-facing answer ("no active key with that id").
	if resp2.StatusCode != http.StatusNotFound {
		t.Fatalf("second revoke: got %d, want 404", resp2.StatusCode)
	}
}

// Invariant: One tenant's API keys are never visible to another. Key
// listing is scoped at the storage boundary, not filtered after the
// fact.
// Category: isolation
// Attack: Authenticate as tenant A and list API keys, hoping tenant B's
// appear
// Refused: Listing is scoped at the storage boundary, so tenant B's keys
// are never in the result to be filtered out
func TestKeysHandler_Tenants_Are_Isolated(t *testing.T) {
	t.Parallel()
	// Two tenants behind two servers — but sharing a store — so we
	// can verify the list endpoint filters by tenant.
	store := NewMemoryAPIKeyStore()
	resolver := NewMapResolver()
	resolver.Set("admin-a", actor.Actor{ID: "admin-a"}, "tenant-a")
	resolver.Set("admin-b", actor.Actor{ID: "admin-b"}, "tenant-b")
	provider := NewStubProvider(resolver)
	clk := time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC)
	mw := NewMiddleware(provider, store, resolver, WithClock(fixedClock(clk)))
	keys := NewKeysHandler(store, WithKeysClock(fixedClock(clk)))
	mux := http.NewServeMux()
	keys.Register(mux)
	srv := httptest.NewServer(mw.Wrap(mux))
	defer srv.Close()

	// admin-a creates a key in tenant-a.
	respA, _ := doJSON(t, "POST", srv.URL+"/v1/keys", "Bearer admin-a", map[string]any{"label": "a"})
	if respA.StatusCode != http.StatusCreated {
		t.Fatalf("create as admin-a: got %d", respA.StatusCode)
	}

	// admin-b lists keys — must see zero, because the key belongs to tenant-a.
	resp, raw := doJSON(t, "GET", srv.URL+"/v1/keys", "Bearer admin-b", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list as admin-b: got %d", resp.StatusCode)
	}
	var body struct {
		Keys []ListItem `json:"keys"`
	}
	_ = json.Unmarshal(raw, &body)
	if len(body.Keys) != 0 {
		t.Fatalf("admin-b should see zero keys; got %d", len(body.Keys))
	}
}

func TestValidRole(t *testing.T) {
	t.Parallel()
	good := []string{"operator", "admin", "ci.bot", "ops:human", "loader-1", "x_y_z"}
	for _, role := range good {
		if !validRole(role) {
			t.Errorf("expected %q to be valid", role)
		}
	}
	bad := []string{"", "Admin", "spaces here", "weird!", strings.Repeat("a", 65), "tab\t"}
	for _, role := range bad {
		if validRole(role) {
			t.Errorf("expected %q to be invalid", role)
		}
	}
}

// TestKeysHandler_Create_RoleSubsetRule is the regression test
// for a role-escalation problem found in review. The
// proposal-gate trust boundary depends on Identity.Roles being
// non-escalating across key minting. Three sub-tests cover the
// matrix:
//
//   - admin caller: can mint a key with any role (operator, etc.)
//   - non-admin caller minting same role: ok (subset of self)
//   - non-admin caller trying to mint a higher role: 403
func TestKeysHandler_Create_RoleSubsetRule(t *testing.T) {
	t.Parallel()

	t.Run("admin can mint operator", func(t *testing.T) {
		srv, _, token := keysServerAs(t, "tenant-acme", "admin-subject", []string{"admin"})
		resp, raw := doJSON(t, "POST", srv.URL+"/v1/keys", token, map[string]any{
			"label": "loader", "roles": []string{"operator"},
		})
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("status: got %d, want 201; body=%s", resp.StatusCode, raw)
		}
	})

	t.Run("operator can mint operator (subset of self)", func(t *testing.T) {
		srv, _, token := keysServerAs(t, "tenant-acme", "ops-subject", []string{"operator"})
		resp, raw := doJSON(t, "POST", srv.URL+"/v1/keys", token, map[string]any{
			"label": "loader", "roles": []string{"operator"},
		})
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("status: got %d, want 201; body=%s", resp.StatusCode, raw)
		}
	})

	t.Run("operator cannot mint admin", func(t *testing.T) {
		srv, _, token := keysServerAs(t, "tenant-acme", "ops-subject", []string{"operator"})
		resp, raw := doJSON(t, "POST", srv.URL+"/v1/keys", token, map[string]any{
			"label": "escalation", "roles": []string{"admin"},
		})
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("status: got %d, want 403; body=%s", resp.StatusCode, raw)
		}
	})

	t.Run("operator cannot mint a role the operator doesn't hold", func(t *testing.T) {
		srv, _, token := keysServerAs(t, "tenant-acme", "ops-subject", []string{"operator"})
		resp, raw := doJSON(t, "POST", srv.URL+"/v1/keys", token, map[string]any{
			"label": "escalation", "roles": []string{"billing_admin"},
		})
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("status: got %d, want 403; body=%s", resp.StatusCode, raw)
		}
	})

	t.Run("no-role caller can mint a no-role key", func(t *testing.T) {
		srv, _, token := keysServerAs(t, "tenant-acme", "anon-subject", nil)
		resp, raw := doJSON(t, "POST", srv.URL+"/v1/keys", token, map[string]any{
			"label": "ingester",
		})
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("status: got %d, want 201; body=%s", resp.StatusCode, raw)
		}
	})
}

// TestIsKeyManagement pins the gate logic: only tenant_owner or admin
// may manage keys; a plain runtime role cannot.
func TestIsKeyManagement(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		roles []string
		want  bool
	}{
		{"tenant_owner", []string{"tenant_owner"}, true},
		{"admin", []string{"admin"}, true},
		{"both", []string{"admin", "tenant_owner"}, true},
		{"runtime ops_agent", []string{"ops_agent"}, false},
		{"empty", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id := Identity{Subject: "s", Tenant: "t", Roles: tc.roles}
			if got := isKeyManagement(id); got != tc.want {
				t.Errorf("isKeyManagement(%v) = %v, want %v", tc.roles, got, tc.want)
			}
		})
	}
}

// TestKeysHandler_RuntimeKeyCannotManage proves the end-to-end gate: a
// plain runtime API key (role ops_agent, no management role — the
// agent's decide credential) is refused on both mint and revoke with
// 403. The middleware resolves an API key to an api_key-method
// identity and does NOT widen it with tenant_owner (sessionOwnerRoles
// is session-only), so the runtime key genuinely lacks the management
// role. This is the "a leaked runtime key can't manufacture sibling
// keys" guarantee.
//
// Invariant: A runtime key cannot manage keys. Credentials issued for
// calling the decision API cannot mint, rotate, or revoke other
// credentials.
// Category: authority
// Attack: Use a runtime key — the credential agents call the decision API
// with — to mint a second key
// Refused: Key management requires a management identity. The runtime key
// is refused
func TestKeysHandler_RuntimeKeyCannotManage(t *testing.T) {
	t.Parallel()
	srv, store, _ := keysServer(t, "tenant-acme")

	gen, err := NewAPIKey("tenant-acme", time.Time{})
	if err != nil {
		t.Fatalf("NewAPIKey: %v", err)
	}
	if err := store.Insert(context.Background(), APIKeyRecord{
		ID:        "runtime-key-1",
		Tenant:    "tenant-acme",
		Subject:   "svc-agent",
		Label:     "runtime",
		Roles:     []string{"ops_agent"},
		KeyHash:   gen.Hash,
		CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	runtimeAuth := "Bearer " + gen.Plaintext

	resp, raw := doJSON(t, "POST", srv.URL+"/v1/keys", runtimeAuth, map[string]any{"label": "sibling"})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("mint with runtime key: got %d, want 403; body=%s", resp.StatusCode, raw)
	}
	resp, raw = doJSON(t, "DELETE", srv.URL+"/v1/keys/some-id", runtimeAuth, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("revoke with runtime key: got %d, want 403; body=%s", resp.StatusCode, raw)
	}
}

// A key minted for a card's agent is bound to it: the binding is
// stored, echoed once on create, and shown on the list so the owner
// can see which agent each key speaks for.
func TestKeysHandler_Create_BindsAgent(t *testing.T) {
	t.Parallel()
	srv, store, token := keysServer(t, "tenant-acme")

	resp, raw := doJSON(t, "POST", srv.URL+"/v1/keys", token, map[string]any{
		"label":    "Agent key for card refund-agent-refunds",
		"roles":    []string{"support"},
		"agent_id": "  refund-agent ",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status: got %d, want 201; body=%s", resp.StatusCode, raw)
	}
	var got CreateResponse
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.AgentID != "refund-agent" {
		t.Errorf("AgentID: got %q, want refund-agent (trimmed)", got.AgentID)
	}
	parsed, err := ParseAPIKey(got.Plaintext)
	if err != nil {
		t.Fatalf("ParseAPIKey: %v", err)
	}
	rec, err := store.GetByHash(context.Background(), parsed.Hash)
	if err != nil {
		t.Fatalf("GetByHash: %v", err)
	}
	if rec.AgentID != "refund-agent" || !rec.IsAgentBound() {
		t.Errorf("stored AgentID: got %q, want refund-agent", rec.AgentID)
	}

	resp, raw = doJSON(t, "GET", srv.URL+"/v1/keys", token, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list status: got %d; body=%s", resp.StatusCode, raw)
	}
	if !strings.Contains(string(raw), `"agent_id":"refund-agent"`) {
		t.Errorf("list does not show the binding: %s", raw)
	}
}

func TestKeysHandler_Create_RejectsInvalidAgentID(t *testing.T) {
	t.Parallel()
	srv, _, token := keysServer(t, "tenant-acme")

	for _, bad := range []string{strings.Repeat("a", 201), "agent\nname"} {
		resp, raw := doJSON(t, "POST", srv.URL+"/v1/keys", token, map[string]any{"agent_id": bad})
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("agent_id %q: status %d, want 400; body=%s", bad, resp.StatusCode, raw)
		}
	}
}

// A key can be bound to one environment, trimmed, stored, listed.
func TestKeysHandler_Create_BindsEnvironment(t *testing.T) {
	t.Parallel()
	srv, store, token := keysServer(t, "tenant-acme")

	resp, raw := doJSON(t, "POST", srv.URL+"/v1/keys", token, map[string]any{
		"label": "prod agent", "agent_id": "refund-agent", "environment": " production ",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status: got %d, want 201; body=%s", resp.StatusCode, raw)
	}
	var got CreateResponse
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Environment != "production" {
		t.Errorf("Environment: got %q, want production (trimmed)", got.Environment)
	}
	parsed, err := ParseAPIKey(got.Plaintext)
	if err != nil {
		t.Fatalf("ParseAPIKey: %v", err)
	}
	rec, err := store.GetByHash(context.Background(), parsed.Hash)
	if err != nil {
		t.Fatalf("GetByHash: %v", err)
	}
	if rec.Environment != "production" || !rec.IsEnvironmentBound() {
		t.Errorf("stored Environment: got %q, want production", rec.Environment)
	}
	_, raw = doJSON(t, "GET", srv.URL+"/v1/keys", token, nil)
	if !strings.Contains(string(raw), `"environment":"production"`) {
		t.Errorf("list does not show the binding: %s", raw)
	}
	for _, bad := range []string{strings.Repeat("e", 201), "prod\nuction"} {
		if resp, raw := doJSON(t, "POST", srv.URL+"/v1/keys", token, map[string]any{"environment": bad}); resp.StatusCode != http.StatusBadRequest {
			t.Errorf("environment %q: status %d, want 400; body=%s", bad, resp.StatusCode, raw)
		}
	}
}

// Roles can be added to an existing key, so one key carries an
// agent from observe to enforce. The key keeps its id, agent and
// environment, repeats are ignored, and the new role authenticates.
func TestKeysHandler_Update_AddsRoles(t *testing.T) {
	t.Parallel()
	srv, store, token := keysServer(t, "tenant-acme")
	gen, err := NewAPIKey("tenant-acme", time.Time{})
	if err != nil {
		t.Fatalf("NewAPIKey: %v", err)
	}
	if err := store.Insert(context.Background(), APIKeyRecord{
		ID: "agent-key", Tenant: "tenant-acme", Subject: "owner", Label: "Agent key for refund-agent",
		AgentID: "refund-agent", Environment: "production", KeyHash: gen.Hash, CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	resp, raw := doJSON(t, "PATCH", srv.URL+"/v1/keys/agent-key", token, map[string]any{"add_roles": []string{"support", "support"}})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d, want 200; body=%s", resp.StatusCode, raw)
	}
	var got ListItem
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.ID != "agent-key" || got.AgentID != "refund-agent" || got.Environment != "production" ||
		len(got.Roles) != 1 || got.Roles[0] != "support" {
		t.Errorf("updated key = %+v, want the same key with role support", got)
	}
	resp, raw = doJSON(t, "PATCH", srv.URL+"/v1/keys/agent-key", token, map[string]any{"add_roles": []string{"billing", "support"}})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("second add: %d %s", resp.StatusCode, raw)
	}
	rec, err := store.GetByHash(context.Background(), gen.Hash)
	if err != nil {
		t.Fatalf("GetByHash: %v", err)
	}
	if strings.Join(rec.Roles, ",") != "support,billing" {
		t.Errorf("stored roles = %v, want [support billing] in the order added", rec.Roles)
	}
}

// A runtime key cannot grant roles, a non-admin cannot
// grant a role it does not hold, and only the tenant's active keys can
// be changed.
func TestKeysHandler_Update_Refusals(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	srv, store, token := keysServer(t, "tenant-acme")
	insert := func(id, tenant string, revoked bool) string {
		t.Helper()
		gen, err := NewAPIKey(tenant, time.Time{})
		if err != nil {
			t.Fatalf("NewAPIKey: %v", err)
		}
		if err := store.Insert(ctx, APIKeyRecord{
			ID: id, Tenant: tenant, Subject: "svc", Roles: []string{"ops_agent"}, KeyHash: gen.Hash, CreatedAt: time.Now().UTC(),
		}); err != nil {
			t.Fatalf("Insert %s: %v", id, err)
		}
		if revoked {
			if err := store.Revoke(ctx, id, time.Now()); err != nil {
				t.Fatalf("Revoke: %v", err)
			}
		}
		return "Bearer " + gen.Plaintext
	}
	runtimeAuth := insert("runtime-key", "tenant-acme", false)
	insert("other-tenant-key", "tenant-other", false)
	insert("revoked-key", "tenant-acme", true)

	for _, c := range []struct {
		name, auth, id string
		body           any
		want           int
	}{
		{"runtime key grants itself admin", runtimeAuth, "runtime-key", map[string]any{"add_roles": []string{"admin"}}, http.StatusForbidden},
		{"runtime key grants its own role", runtimeAuth, "runtime-key", map[string]any{"add_roles": []string{"ops_agent"}}, http.StatusForbidden},
		{"another tenant's key", token, "other-tenant-key", map[string]any{"add_roles": []string{"support"}}, http.StatusNotFound},
		{"revoked key", token, "revoked-key", map[string]any{"add_roles": []string{"support"}}, http.StatusNotFound},
		{"unknown key", token, "nope", map[string]any{"add_roles": []string{"support"}}, http.StatusNotFound},
		{"nothing to add", token, "runtime-key", map[string]any{"add_roles": []string{}}, http.StatusBadRequest},
		{"invalid role", token, "runtime-key", map[string]any{"add_roles": []string{"Admin!"}}, http.StatusBadRequest},
	} {
		if resp, raw := doJSON(t, "PATCH", srv.URL+"/v1/keys/"+c.id, c.auth, c.body); resp.StatusCode != c.want {
			t.Errorf("%s: status %d, want %d; body=%s", c.name, resp.StatusCode, c.want, raw)
		}
	}
	if rec, _ := store.GetByHash(ctx, mustHash(t, runtimeAuth)); strings.Join(rec.Roles, ",") != "ops_agent" {
		t.Errorf("a refused grant changed the key: roles %v", rec.Roles)
	}

	// A tenant_owner without the support role cannot grant it.
	ownerSrv, ownerStore, ownerToken := keysServerAs(t, "tenant-acme", "owner", []string{"tenant_owner"})
	gen, _ := NewAPIKey("tenant-acme", time.Time{})
	if err := ownerStore.Insert(ctx, APIKeyRecord{ID: "k", Tenant: "tenant-acme", Subject: "owner", KeyHash: gen.Hash, CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if resp, raw := doJSON(t, "PATCH", ownerSrv.URL+"/v1/keys/k", ownerToken, map[string]any{"add_roles": []string{"support"}}); resp.StatusCode != http.StatusForbidden {
		t.Errorf("non-admin granting a role it lacks: status %d, want 403; body=%s", resp.StatusCode, raw)
	}
}

func mustHash(t *testing.T, bearer string) [APIKeyHashSize]byte {
	t.Helper()
	parsed, err := ParseAPIKey(strings.TrimPrefix(bearer, "Bearer "))
	if err != nil {
		t.Fatalf("ParseAPIKey: %v", err)
	}
	return parsed.Hash
}

// A phone's session mints only a taf's own key, bound to an agent; an
// account key from a phone is refused, even signed.
func TestKeysHandler_PhoneMintsOnlyAgentKeys(t *testing.T) {
	t.Parallel()
	keys := NewKeysHandler(NewMemoryAPIKeyStore(), WithKeysClock(fixedClock(time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC))))
	mux := http.NewServeMux()
	keys.Register(mux)
	phone := Identity{Subject: "owner-1", Tenant: "tenant-acme", Method: MethodDeveloper, Roles: []string{"admin"}, DeviceKey: []byte{0x30}, DeviceSessionID: "sess-phone"}
	mint := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/v1/keys", strings.NewReader(body))
		req = req.WithContext(WithIdentity(req.Context(), phone))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}
	if rec := mint(`{"label":"admin","roles":["admin"]}`); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "from a phone") {
		t.Errorf("a phone minted an account key: %d %s", rec.Code, rec.Body.String())
	}
	if rec := mint(`{"label":"rae","roles":["gateway_agent"],"agent_id":"rae"}`); rec.Code != http.StatusCreated {
		t.Errorf("a phone could not mint its taf's key: %d %s", rec.Code, rec.Body.String())
	}
}

// A phone's session revokes an agent's key of its tenant, and no
// other: an account key and the connector key stay the dashboard's. An
// editor's phone revokes nothing, as in KIFF.
func TestKeysHandler_PhoneRevokesOnlyAgentKeys(t *testing.T) {
	t.Parallel()
	store := NewMemoryAPIKeyStore()
	keys := NewKeysHandler(store, WithKeysClock(fixedClock(time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC))))
	mux := http.NewServeMux()
	keys.Register(mux)
	phone := Identity{Subject: "owner-1", Tenant: "tenant-acme", Method: MethodDeveloper, Roles: []string{"admin", "tenant_owner"},
		DeviceKey: []byte{0x30}, DeviceSessionID: "sess-phone"}
	editor := phone
	editor.Roles = []string{"editor"}
	var n byte
	insert := func(tenant, id, agent string, roles ...string) {
		t.Helper()
		n++
		if err := store.Insert(context.Background(), APIKeyRecord{ID: id, Tenant: tenant, Subject: "owner-1", AgentID: agent, Roles: roles,
			KeyHash: [APIKeyHashSize]byte{n}, CreatedAt: time.Date(2026, 10, 18, 11, 0, 0, 0, time.UTC)}); err != nil {
			t.Fatal(err)
		}
	}
	insert("tenant-acme", "key-rae", "rae", RoleGatewayAgent)
	insert("tenant-acme", "key-setup", "setup-1")
	insert("tenant-acme", "key-account", "", "admin")
	insert("tenant-acme", "key-runtime", "")
	insert("tenant-acme", "key-planque", "", RoleConnector)
	insert("tenant-other", "key-theirs", "bob", RoleGatewayAgent)
	revoke := func(id Identity, key string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodDelete, "/v1/keys/"+key, nil)
		req = req.WithContext(WithIdentity(req.Context(), id))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}

	for key, code := range map[string]int{
		"key-account": http.StatusForbidden,
		"key-runtime": http.StatusForbidden,
		"key-planque": http.StatusForbidden,
		"key-theirs":  http.StatusNotFound,
	} {
		if rec := revoke(phone, key); rec.Code != code {
			t.Errorf("%s from a phone: %d %s, want %d", key, rec.Code, rec.Body.String(), code)
		}
	}
	if rec := revoke(editor, "key-rae"); rec.Code != http.StatusForbidden {
		t.Errorf("an editor's phone revoked a key: %d %s", rec.Code, rec.Body.String())
	}
	for _, key := range []string{"key-rae", "key-setup"} {
		if rec := revoke(phone, key); rec.Code != http.StatusNoContent {
			t.Errorf("%s from a phone: %d %s, want 204", key, rec.Code, rec.Body.String())
		}
	}
	left, _ := store.ListActive(context.Background(), "tenant-acme")
	var ids []string
	for _, rec := range left {
		ids = append(ids, rec.ID)
	}
	slices.Sort(ids)
	if !slices.Equal(ids, []string{"key-account", "key-planque", "key-runtime"}) {
		t.Fatalf("active after the phone's revokes: %v", ids)
	}
	if theirs, _ := store.ListActive(context.Background(), "tenant-other"); len(theirs) != 1 {
		t.Fatalf("another tenant's key was revoked: %+v", theirs)
	}
}
