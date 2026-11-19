package handlers_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kiff/cloud/api/internal/auth"
)

// Who reaches the draft routes: an agent's key may submit a draft
// but never confirm or discard one; a phone submits and confirms only
// with its signature.
func TestAgentDraftRoutesByCaller(t *testing.T) {
	t.Parallel()
	inner := http.NewServeMux()
	for _, p := range []string{"POST /v1/me/agent-drafts", "POST /v1/me/agent-drafts/{id}/confirm", "DELETE /v1/me/agent-drafts/{id}", "GET /v1/me/agent-drafts/{id}"} {
		inner.HandleFunc(p, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) })
	}
	policed := auth.DefaultRoutePolicy().Wrap(inner)
	call := func(id auth.Identity, method, path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		withFakeIdentity(policed, id).ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(`{}`)))
		return rec
	}
	agent := auth.Identity{Subject: "key-1", Tenant: "t1", Method: auth.MethodAPIKey, APIKey: true, AgentID: "setup-agent"}
	if rec := call(agent, http.MethodPost, "/v1/me/agent-drafts"); rec.Code != http.StatusTeapot {
		t.Errorf("agent submit: %d %s", rec.Code, rec.Body.String())
	}
	for _, c := range [][2]string{{http.MethodPost, "/v1/me/agent-drafts/d1/confirm"}, {http.MethodDelete, "/v1/me/agent-drafts/d1"}} {
		if rec := call(agent, c[0], c[1]); rec.Code != http.StatusForbidden {
			t.Errorf("agent %s %s: %d", c[0], c[1], rec.Code)
		}
	}
	phone := auth.Identity{Subject: "alice", Tenant: "t1", Method: auth.MethodDeveloper, Roles: []string{"tenant_owner"}, DeviceKey: []byte{0x30}, DeviceSessionID: "s"}
	for _, path := range []string{"/v1/me/agent-drafts", "/v1/me/agent-drafts/d1/confirm"} {
		if rec := call(phone, http.MethodPost, path); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "signature_required") {
			t.Errorf("unsigned phone POST %s: %d %s", path, rec.Code, rec.Body.String())
		}
	}
	if rec := call(phone, http.MethodGet, "/v1/me/agent-drafts/d1"); rec.Code != http.StatusTeapot {
		t.Errorf("phone reads a draft: %d", rec.Code)
	}
}
