package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// Invariant: a key bound to an agent cannot mint, grant or revoke keys, even when it holds the admin role.
// Category: authority
// Attack: mint an admin key bound to an agent, then use it to mint an unbound admin key and act as the account's owner
// Refused: key management requires the owner's or an admin's session or a key not bound to an agent
func TestAgentKeyCannotManageKeys(t *testing.T) {
	srv, store, _ := keysServer(t, "tenant-a")
	// A stored agent-bound key with admin can no longer be minted
	// (TestAgentKeyCannotHoldAccountRoles), so plant one as a key
	// minted before that refusal would be.
	created := plantKey(t, store, "tenant-a", "agent-a", []string{"admin"})
	agentAuth := "Bearer " + created.Plaintext

	if resp, body := doJSON(t, http.MethodPost, srv.URL+"/v1/keys", agentAuth,
		map[string]any{"label": "escape", "roles": []string{"admin"}}); resp.StatusCode != http.StatusForbidden {
		t.Errorf("agent key minted an unbound admin key: %d %s", resp.StatusCode, body)
	}
	if resp, body := doJSON(t, http.MethodPatch, srv.URL+"/v1/keys/"+created.ID, agentAuth,
		map[string]any{"add_roles": []string{"admin"}}); resp.StatusCode != http.StatusForbidden {
		t.Errorf("agent key granted a role: %d %s", resp.StatusCode, body)
	}
	if resp, body := doJSON(t, http.MethodDelete, srv.URL+"/v1/keys/"+created.ID, agentAuth, nil); resp.StatusCode != http.StatusForbidden {
		t.Errorf("agent key revoked a key: %d %s", resp.StatusCode, body)
	}
}

// Invariant: a key bound to an agent never holds the tenant_owner or admin role.
// Category: authority
// Attack: mint an agent's key with admin, or grant admin to it later, so domain permissions bound to admin reach the agent
// Refused: minting or granting an account role onto an agent's key is refused, and a stored one resolves without it
func TestAgentKeyCannotHoldAccountRoles(t *testing.T) {
	srv, store, ownerAuth := keysServer(t, "tenant-a")
	for _, role := range AccountRoles {
		if resp, body := doJSON(t, http.MethodPost, srv.URL+"/v1/keys", ownerAuth,
			map[string]any{"label": "agent", "roles": []string{role}, "agent_id": "agent-a"}); resp.StatusCode != http.StatusBadRequest {
			t.Errorf("minted an agent key with %s: %d %s", role, resp.StatusCode, body)
		}
	}

	resp, body := doJSON(t, http.MethodPost, srv.URL+"/v1/keys", ownerAuth,
		map[string]any{"label": "agent", "roles": []string{"gateway_agent"}, "agent_id": "agent-a"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("owner mints an agent key: %d %s", resp.StatusCode, body)
	}
	var agentKey struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(body, &agentKey); err != nil {
		t.Fatal(err)
	}
	for _, role := range AccountRoles {
		if resp, body := doJSON(t, http.MethodPatch, srv.URL+"/v1/keys/"+agentKey.ID, ownerAuth,
			map[string]any{"add_roles": []string{role}}); resp.StatusCode != http.StatusBadRequest {
			t.Errorf("granted %s to an agent key: %d %s", role, resp.StatusCode, body)
		}
	}
	if resp, body := doJSON(t, http.MethodPatch, srv.URL+"/v1/keys/"+agentKey.ID, ownerAuth,
		map[string]any{"add_roles": []string{"refunds.reviewer"}}); resp.StatusCode != http.StatusOK {
		t.Errorf("granting an ordinary role to an agent key: %d %s", resp.StatusCode, body)
	}
	if resp, body := doJSON(t, http.MethodPatch, srv.URL+"/v1/keys/no-such-key", ownerAuth,
		map[string]any{"add_roles": []string{"admin"}}); resp.StatusCode != http.StatusNotFound {
		t.Errorf("granting admin to an unknown key: %d %s", resp.StatusCode, body)
	}
	// An unbound key may still be given admin by an admin.
	resp, body = doJSON(t, http.MethodPost, srv.URL+"/v1/keys", ownerAuth, map[string]any{"label": "ops"})
	var unbound struct {
		ID string `json:"id"`
	}
	if resp.StatusCode != http.StatusCreated || json.Unmarshal(body, &unbound) != nil {
		t.Fatalf("mint unbound: %d %s", resp.StatusCode, body)
	}
	if resp, body := doJSON(t, http.MethodPatch, srv.URL+"/v1/keys/"+unbound.ID, ownerAuth,
		map[string]any{"add_roles": []string{"admin"}}); resp.StatusCode != http.StatusOK {
		t.Errorf("granting admin to an unbound key: %d %s", resp.StatusCode, body)
	}

	// A stored agent key that holds an account role anyway resolves
	// without it, and keeps its other roles.
	planted := plantKey(t, store, "tenant-a", "agent-b", []string{"admin", "tenant_owner", "refunds.reviewer"})
	mw := NewMiddleware(NewStubProvider(NewMapResolver()), store, NewMapResolver())
	var got Identity
	h := mw.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ = IdentityFrom(r.Context())
	}))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+planted.Plaintext)
	h.ServeHTTP(httptest.NewRecorder(), req)
	if got.AgentID != "agent-b" {
		t.Fatalf("planted key did not resolve: %+v", got)
	}
	if got.HasRole("admin") || got.HasRole("tenant_owner") || !got.HasRole("refunds.reviewer") {
		t.Errorf("agent key resolved with roles %v, want only refunds.reviewer", got.Roles)
	}

	// The keys list shows the roles it acts with, not the stored ones.
	resp, body = doJSON(t, http.MethodGet, srv.URL+"/v1/keys", ownerAuth, nil)
	var list struct {
		Keys []struct {
			ID    string   `json:"id"`
			Roles []string `json:"roles"`
		} `json:"keys"`
	}
	if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &list) != nil {
		t.Fatalf("list keys: %d %s", resp.StatusCode, body)
	}
	found := false
	for _, k := range list.Keys {
		if k.ID == planted.ID {
			found = true
			if len(k.Roles) != 1 || k.Roles[0] != "refunds.reviewer" {
				t.Errorf("keys list shows the agent key with roles %v, want only refunds.reviewer", k.Roles)
			}
		}
	}
	if !found {
		t.Errorf("planted key not listed: %s", body)
	}
}

// plantKey stores a key directly, bypassing the mint route's checks.
func plantKey(t *testing.T, store *MemoryAPIKeyStore, tenant, agentID string, roles []string) struct{ ID, Plaintext string } {
	t.Helper()
	now := time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC)
	gen, err := NewAPIKey(tenant, now)
	if err != nil {
		t.Fatal(err)
	}
	id := "planted-" + agentID
	if err := store.Insert(context.Background(), APIKeyRecord{
		ID: id, Tenant: tenant, Subject: "admin-subject", Label: "planted",
		Roles: roles, AgentID: agentID, KeyHash: gen.Hash, CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	return struct{ ID, Plaintext string }{id, gen.Plaintext}
}

func TestManagesAccount(t *testing.T) {
	for name, c := range map[string]struct {
		id   Identity
		want bool
	}{
		"owner session":         {Identity{Roles: []string{"tenant_owner"}}, true},
		"admin key":             {Identity{Roles: []string{"admin"}, APIKey: true}, true},
		"runtime key":           {Identity{Roles: []string{"ops_agent"}, APIKey: true}, false},
		"agent key with admin":  {Identity{Roles: []string{"admin"}, APIKey: true, AgentID: "agent-a"}, false},
		"agent key, owner role": {Identity{Roles: []string{"tenant_owner"}, APIKey: true, AgentID: "agent-a"}, false},
	} {
		if got := c.id.ManagesAccount(); got != c.want {
			t.Errorf("%s: %v, want %v", name, got, c.want)
		}
	}
}

// Invariant: a gateway key is always bound to an agent. Neither creating a
// key nor adding a role to one yields a gateway_agent key without an agent.
// Category: authority
// Attack: mint a gateway_agent key with no agent_id, or add gateway_agent to an unbound key
// Refused: key creation and role grants require an agent binding for gateway_agent
func TestGatewayKeyMustBeBoundToAnAgent(t *testing.T) {
	srv, _, ownerAuth := keysServer(t, "tenant-a")
	if resp, body := doJSON(t, http.MethodPost, srv.URL+"/v1/keys", ownerAuth,
		map[string]any{"label": "gw", "roles": []string{RoleGatewayAgent}}); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("minted an unbound gateway key: %d %s", resp.StatusCode, body)
	}
	resp, body := doJSON(t, http.MethodPost, srv.URL+"/v1/keys", ownerAuth,
		map[string]any{"label": "reviewer", "roles": []string{"refunds.reviewer"}})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("mint unbound key: %d %s", resp.StatusCode, body)
	}
	var key struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(body, &key); err != nil {
		t.Fatal(err)
	}
	if resp, body := doJSON(t, http.MethodPatch, srv.URL+"/v1/keys/"+key.ID, ownerAuth,
		map[string]any{"add_roles": []string{RoleGatewayAgent}}); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("added gateway_agent to an unbound key: %d %s", resp.StatusCode, body)
	}
}
