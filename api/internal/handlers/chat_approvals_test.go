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

// the cards-for-any-agent design: chat approvals default to on for a personal account
// and off otherwise; the owner's choice wins; an agent's key cannot
// change it, and /v1/me reports the value in effect.
func TestChatApprovals(t *testing.T) {
	store := tenant.NewMemoryStore()
	tnt := tenant.Tenant{ID: tenant.NewID(), Slug: "chat", Status: tenant.StatusActive}
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
	do := func(h http.Handler, method, path, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
		return rec
	}
	effective := func() string {
		b := do(owner, http.MethodGet, "/v1/me", "").Body.String()
		if strings.Contains(b, `"chat_approvals":true`) {
			return "on"
		}
		return "off"
	}

	if got := effective(); got != "off" {
		t.Errorf("unchosen account: %s, want off", got)
	}
	do(owner, http.MethodPut, "/v1/me/account-kind", `{"kind":"personal"}`)
	if got := effective(); got != "on" {
		t.Errorf("personal default: %s, want on", got)
	}
	do(owner, http.MethodPut, "/v1/me/account-kind", `{"kind":"company"}`)
	if got := effective(); got != "off" {
		t.Errorf("company default: %s, want off", got)
	}
	if rec := do(owner, http.MethodPut, "/v1/me/chat-approvals", `{"enabled":true}`); rec.Code != http.StatusOK {
		t.Fatalf("owner: %d %s", rec.Code, rec.Body.String())
	}
	if got := effective(); got != "on" {
		t.Errorf("company, owner turned it on: %s", got)
	}
	do(owner, http.MethodPut, "/v1/me/account-kind", `{"kind":"personal"}`)
	do(owner, http.MethodPut, "/v1/me/chat-approvals", `{"enabled":false}`)
	if got := effective(); got != "off" {
		t.Errorf("personal, owner turned it off: %s", got)
	}
	agent := as(auth.Identity{Subject: "k", AgentID: "refund-agent", Roles: []string{"admin"}})
	if rec := do(agent, http.MethodPut, "/v1/me/chat-approvals", `{"enabled":true}`); rec.Code != http.StatusForbidden {
		t.Errorf("agent key: %d", rec.Code)
	}
	for _, bad := range []string{`{}`, `{"enabled":"yes"}`, `{"enabled":true,"x":1}`} {
		if rec := do(owner, http.MethodPut, "/v1/me/chat-approvals", bad); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", bad, rec.Code)
		}
	}
}
