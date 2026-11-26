package handlers

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/tenant"
)

// An operator suspending a test tenant (POST /v1/ops/tenants/{id}/suspend)
// stops everything on it on every route, reads included, not only new
// proposals: its test device's session and its agents' keys. Unsuspending
// brings them back. On any other account the
// middleware lets a suspended tenant's sessions through as before: there,
// only new proposals and the gateway trial are refused.
func TestSuspendingATestTenantStopsEverythingOnItOnEveryRoute(t *testing.T) {
	ctx := context.Background()
	mux, tenants, _, _, testID := opsControlFixture(t)
	realID := tenant.NewID()
	if err := tenants.Create(ctx, tenant.Tenant{ID: realID, Slug: "real", DisplayName: "Real", Status: tenant.StatusActive}); err != nil {
		t.Fatal(err)
	}
	der := func() []byte {
		k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		b, err := x509.MarshalPKIXPublicKey(&k.PublicKey)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	testKey, phone := der(), der()
	sessions := auth.NewMemoryDeveloperSessionStore()
	now := time.Now()
	token := func(id, tenantID string, key []byte) string {
		gen, _ := auth.NewDevSessionToken()
		if err := sessions.Insert(ctx, auth.DeveloperSession{ID: id, Subject: "human-" + id, Tenant: tenantID, TokenHash: gen.Hash,
			CreatedAt: now, ExpiresAt: now.Add(time.Hour), DevicePublicKey: key}); err != nil {
			t.Fatal(err)
		}
		return gen.Plaintext
	}
	testPhone, realPhone := token("s-test", testID, testKey), token("s-real", realID, phone)
	// An agent's key on the test tenant.
	apiKeys := auth.NewMemoryAPIKeyStore()
	agentKey, err := auth.NewAPIKey(testID, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := apiKeys.Insert(ctx, auth.APIKeyRecord{ID: "k-agent", Tenant: testID, Subject: "agent", AgentID: "agt-1",
		KeyHash: agentKey.Hash, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	// As main wires it.
	k := auth.NewKeyRestriction(func(b []byte) bool { return bytes.Equal(b, testKey) }, []string{testID}).
		WithSuspendedCheck(func(ctx context.Context, id string) (bool, error) { return tenant.IsSuspended(ctx, tenants, id) })
	resolver := auth.NewMapResolver()
	mw := auth.NewMiddleware(auth.NewStubProvider(resolver), apiKeys, resolver,
		auth.WithDeveloperSessions(sessions), auth.WithKeyRestriction(k))
	app := mw.Wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	routes := []struct{ method, path string }{
		{http.MethodGet, "/v1/me"}, {http.MethodGet, "/v1/me/agents"}, {http.MethodGet, "/v1/me/usage/credits"},
		{http.MethodPost, "/v1/me/agent-drafts"}, {http.MethodPost, "/v1/proposals"}, {http.MethodPost, "/v1/me/device-signature"},
	}
	call := func(tok, method, path string) (int, string) {
		req := httptest.NewRequest(method, path, strings.NewReader(`{}`))
		req.Header.Set("Authorization", "Bearer "+tok)
		rec := httptest.NewRecorder()
		app.ServeHTTP(rec, req)
		var body map[string]string
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		return rec.Code, body["error"]
	}
	suspend := func(id, action string) {
		t.Helper()
		if rec := doOpsPost(mux, "/v1/ops/tenants/"+id+"/"+action, opsSubject, `{"reason":"test pilot"}`); rec.Code != http.StatusOK {
			t.Fatalf("%s %s: %d %s", action, id, rec.Code, rec.Body)
		}
	}
	for _, rt := range routes {
		for name, tok := range map[string]string{"the test phone": testPhone, "an agent's key": agentKey.Plaintext} {
			if code, _ := call(tok, rt.method, rt.path); code != http.StatusOK {
				t.Fatalf("before the suspension, %s %s with %s: %d", rt.method, rt.path, name, code)
			}
		}
	}
	suspend(testID, "suspend")
	for _, rt := range routes {
		for name, tok := range map[string]string{"the test phone": testPhone, "an agent's key": agentKey.Plaintext} {
			if code, why := call(tok, rt.method, rt.path); code != http.StatusForbidden || why != auth.TenantSuspendedCode {
				t.Errorf("suspended test tenant, %s %s with %s: %d %q", rt.method, rt.path, name, code, why)
			}
		}
	}
	suspend(realID, "suspend")
	for _, rt := range routes {
		if code, _ := call(realPhone, rt.method, rt.path); code != http.StatusOK {
			t.Errorf("a suspended real tenant at the middleware, %s %s: %d (unchanged: only its handlers refuse)", rt.method, rt.path, code)
		}
	}
	suspend(testID, "unsuspend")
	for name, tok := range map[string]string{"the test phone": testPhone, "an agent's key": agentKey.Plaintext} {
		if code, _ := call(tok, http.MethodGet, "/v1/me"); code != http.StatusOK {
			t.Fatalf("unsuspended, %s: %d", name, code)
		}
	}
}
