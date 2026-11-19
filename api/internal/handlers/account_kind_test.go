package handlers_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/handlers"
	"github.com/kiff/cloud/api/internal/tenant"
)

// the cards-for-any-agent design: the account's owner chooses "for me" or "for a company";
// /v1/me reports it, empty until chosen. An agent's key, even with
// admin, and a runtime key cannot choose.
func TestAccountKind(t *testing.T) {
	store := tenant.NewMemoryStore()
	tnt := tenant.Tenant{ID: tenant.NewID(), Slug: "kind", Status: tenant.StatusActive}
	if err := store.Create(context.Background(), tnt); err != nil {
		t.Fatal(err)
	}
	inner := http.NewServeMux()
	handlers.NewMeHandler(store).Register(inner)
	as := func(id auth.Identity) http.Handler {
		id.Tenant = tnt.ID
		return withFakeIdentity(inner, id)
	}
	owner := as(auth.Identity{Subject: "alice", Roles: []string{"tenant_owner"}})
	do := func(h http.Handler, method, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, map[string]string{http.MethodGet: "/v1/me", http.MethodPut: "/v1/me/account-kind"}[method], strings.NewReader(body))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	if body := do(owner, http.MethodGet, "").Body.String(); strings.Contains(body, "account_kind") {
		t.Fatalf("an unchosen account reports a kind: %s", body)
	}
	for name, h := range map[string]http.Handler{
		"agent key with admin": as(auth.Identity{Subject: "k", AgentID: "refund-agent", Roles: []string{"admin"}}),
		"runtime key":          as(auth.Identity{Subject: "k", Roles: []string{"ops_agent"}}),
	} {
		if rec := do(h, http.MethodPut, `{"kind":"personal"}`); rec.Code != http.StatusForbidden {
			t.Errorf("%s: %d", name, rec.Code)
		}
	}
	for _, bad := range []string{`{"kind":"family"}`, `{"kind":"personal","x":1}`, `nope`} {
		if rec := do(owner, http.MethodPut, bad); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", bad, rec.Code)
		}
	}
	if rec := do(owner, http.MethodPut, `{"kind":"personal"}`); rec.Code != http.StatusOK {
		t.Fatalf("owner: %d %s", rec.Code, rec.Body.String())
	}
	if body := do(owner, http.MethodGet, "").Body.String(); !strings.Contains(body, `"account_kind":"personal"`) {
		t.Fatalf("after choosing: %s", body)
	}
	admin := as(auth.Identity{Subject: "bob", Roles: []string{"admin"}})
	if rec := do(admin, http.MethodPut, `{"kind":"company"}`); rec.Code != http.StatusOK {
		t.Fatalf("admin: %d", rec.Code)
	}
	if body := do(owner, http.MethodGet, "").Body.String(); !strings.Contains(body, `"account_kind":"company"`) {
		t.Fatalf("after changing: %s", body)
	}
}
