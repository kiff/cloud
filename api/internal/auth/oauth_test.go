package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type fakeOAuth struct {
	grant OAuthGrant
	err   error
}

func (f fakeOAuth) ResolveAccessToken(context.Context, string) (OAuthGrant, error) {
	return f.grant, f.err
}

// An OAuth access token acts as its grant's agent key, on the gateway's
// routes only.
func TestMiddleware_OAuthToken(t *testing.T) {
	gen, _ := NewAPIKey("acme", time.Time{})
	store := NewMemoryAPIKeyStore()
	_ = store.Insert(context.Background(), APIKeyRecord{ID: "key-g", Tenant: "acme", Subject: "editor-1",
		Roles: []string{RoleGatewayAgent}, AgentID: "claude", KeyHash: gen.Hash, CreatedAt: time.Now()})
	clk := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)

	serve := func(res OAuthTokenResolver, method, path string) (int, Identity) {
		mw := NewMiddleware(NewStubProvider(NewMapResolver()), store, NewMapResolver(), WithClock(fixedClock(clk)), WithOAuthTokens(res))
		down := &authedHandler{}
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(method, path, nil)
		req.Header.Set("Authorization", "Bearer "+OAuthAccessTokenPrefix+"abc")
		mw.Wrap(down).ServeHTTP(rec, req)
		return rec.Code, down.id
	}
	ok := fakeOAuth{grant: OAuthGrant{GrantID: "kg_1", KeyHash: gen.Hash}}

	for _, route := range [][2]string{{"POST", "/v1/proposals/decide"}, {"POST", "/v1/events/raw"}, {"GET", "/v1/me"}, {"GET", "/v1/me/card"}} {
		code, id := serve(ok, route[0], route[1])
		if code != http.StatusOK {
			t.Fatalf("%v: %d", route, code)
		}
		if id.AgentID != "claude" || id.Tenant != "acme" || !id.APIKey ||
			id.Metadata["api_key_id"] != "key-g" || id.Metadata["oauth_grant_id"] != "kg_1" {
			t.Fatalf("%v: identity %+v", route, id)
		}
	}
	for _, route := range [][2]string{{"POST", "/v1/keys"}, {"GET", "/v1/me/agents"}, {"POST", "/v1/oauth/consent"},
		{"POST", "/v1/mandates"}, {"GET", "/v1/account/export"}} {
		if code, _ := serve(ok, route[0], route[1]); code != http.StatusForbidden {
			t.Fatalf("%v reached with an OAuth token: %d", route, code)
		}
	}
	if code, _ := serve(fakeOAuth{err: errors.New("expired")}, "POST", "/v1/proposals/decide"); code != http.StatusUnauthorized {
		t.Fatalf("expired token: %d", code)
	}
	// Without the option, OAuth tokens are refused.
	mw := NewMiddleware(NewStubProvider(NewMapResolver()), store, NewMapResolver())
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/proposals/decide", nil)
	req.Header.Set("Authorization", "Bearer "+OAuthAccessTokenPrefix+"abc")
	mw.Wrap(&authedHandler{}).ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("oauth disabled: %d", rec.Code)
	}
	// A grant whose key is not an agent-bound gateway key is refused.
	acct, _ := NewAPIKey("acme", time.Time{})
	_ = store.Insert(context.Background(), APIKeyRecord{ID: "key-admin", Tenant: "acme", Subject: "owner",
		Roles: []string{"admin"}, KeyHash: acct.Hash, CreatedAt: time.Now()})
	if code, _ := serve(fakeOAuth{grant: OAuthGrant{GrantID: "kg_2", KeyHash: acct.Hash}}, "GET", "/v1/me"); code != http.StatusUnauthorized {
		t.Fatalf("account key behind a grant: %d", code)
	}
}

// Invariant: an agent's gateway key pasted into a client reaches the same
// routes as an OAuth token for that agent, and no others: not the
// account's Cards, held work, event stream, agents or export.
// Category: authority
// Attack: use an agent's pasted gateway key to read the rest of the account through GET /v1/me/* routes
// Refused: the middleware applies the gateway route list to any agent-bound gateway_agent key, not only to OAuth tokens
func TestPastedGatewayKeyReachesOnlyGatewayRoutes(t *testing.T) {
	store := NewMemoryAPIKeyStore()
	mint := func(id string, roles []string, agent string) string {
		gen, _ := NewAPIKey("acme", time.Time{})
		_ = store.Insert(context.Background(), APIKeyRecord{ID: id, Tenant: "acme", Subject: "editor-1",
			Roles: roles, AgentID: agent, KeyHash: gen.Hash, CreatedAt: time.Now()})
		return gen.Plaintext
	}
	gateway := mint("key-gw", []string{RoleGatewayAgent}, "claude")
	// No creation path makes one, but a gateway key with no agent is
	// still only a gateway key.
	unbound := mint("key-gw-unbound", []string{RoleGatewayAgent}, "")
	runtime := mint("key-rt", []string{"ops_agent"}, "claude")
	mw := NewMiddleware(NewStubProvider(NewMapResolver()), store, NewMapResolver())
	serve := func(key, method, path string) int {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(method, path, nil)
		req.Header.Set("Authorization", "Bearer "+key)
		mw.Wrap(&authedHandler{}).ServeHTTP(rec, req)
		return rec.Code
	}
	for _, route := range oauthRoutes {
		method, path, _ := strings.Cut(route, " ")
		if code := serve(gateway, method, path); code != http.StatusOK {
			t.Errorf("gateway key on %s: %d", route, code)
		}
	}
	for _, route := range [][2]string{{"GET", "/v1/me/mandates"}, {"GET", "/v1/me/mandates/card-b/statement"},
		{"GET", "/v1/me/card-exceptions"}, {"GET", "/v1/me/attention"}, {"GET", "/v1/me/stream"},
		{"GET", "/v1/me/agents"}, {"GET", "/v1/me/export"}, {"GET", "/v1/me/controls"}, {"POST", "/v1/keys"}} {
		if code := serve(gateway, route[0], route[1]); code != http.StatusForbidden {
			t.Errorf("gateway key reached %v: %d", route, code)
		}
		if code := serve(unbound, route[0], route[1]); code != http.StatusForbidden {
			t.Errorf("unbound gateway key reached %v: %d", route, code)
		}
	}
	// An agent's runtime key is not a gateway credential; its handlers
	// scope what it reads (TestAgentKeyReadsOnlyCardsItsAgentHolds).
	if code := serve(runtime, "GET", "/v1/me/mandates"); code != http.StatusOK {
		t.Errorf("runtime key on /v1/me/mandates: %d", code)
	}
}
