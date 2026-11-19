package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/receipt"
	"github.com/kiff/cloud/api/internal/tenant"
)

// Invariant: a key bound to an agent cannot author a domain, export or delete the account, or choose what it is for, even when it holds the admin role.
// Category: authority
// Attack: use an admin key bound to an agent to rewrite the contract it is governed by, download every receipt, or delete the account
// Refused: these act for the account's people and require the owner's or an admin's session or a key not bound to an agent
func TestAgentKeyCannotActForTheAccount(t *testing.T) {
	store := tenant.NewMemoryStore()
	tnt := tenant.Tenant{ID: tenant.NewID(), Slug: "agent-key", Status: tenant.StatusActive}
	if err := store.Create(context.Background(), tnt); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	NewAccountHandler(store, auth.NewMemoryAPIKeyStore(), receipt.NewMemoryStore()).Register(mux)
	NewMeHandler(store).Register(mux)

	agentAdmin := auth.Identity{Subject: "k", Tenant: tnt.ID, Method: auth.MethodAPIKey, APIKey: true,
		Roles: []string{"admin"}, AgentID: "agent-a"}
	for name, c := range map[string]struct{ method, path string }{
		"export":       {http.MethodGet, "/v1/me/export"},
		"delete":       {http.MethodPost, "/v1/me/delete"},
		"account kind": {http.MethodPut, "/v1/me/account-kind"},
	} {
		var body any
		if name == "account kind" {
			body = map[string]string{"kind": "personal"}
		}
		if rr := do(t, mux, agentAdmin, c.method, c.path, body); rr.Code != http.StatusForbidden {
			t.Errorf("%s: %d %s", name, rr.Code, rr.Body.String())
		}
	}
	if got, err := store.Get(context.Background(), tnt.ID); err != nil || got.Status != tenant.StatusActive {
		t.Fatalf("the account changed: %+v %v", got, err)
	}
	if rec := httptest.NewRecorder(); requireManagement(rec, agentAdmin) || rec.Code != http.StatusForbidden {
		t.Errorf("agent key may author a domain: %d", rec.Code)
	}

	admin := agentAdmin
	admin.AgentID = ""
	if rr := do(t, mux, admin, http.MethodGet, "/v1/me/export", nil); rr.Code != http.StatusOK {
		t.Errorf("unbound admin export: %d %s", rr.Code, rr.Body.String())
	}
	if rr := do(t, mux, runtimeKey(), http.MethodGet, "/v1/me/export", nil); rr.Code != http.StatusForbidden {
		t.Errorf("runtime key export: %d", rr.Code)
	}
}
