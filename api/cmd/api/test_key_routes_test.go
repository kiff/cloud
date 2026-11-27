package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/agents"
	"github.com/kiff/cloud/api/internal/alert"
	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/domain"
	"github.com/kiff/cloud/api/internal/executors"
	"github.com/kiff/cloud/api/internal/guard"
	"github.com/kiff/cloud/api/internal/mandate"
	"github.com/kiff/cloud/api/internal/proofs"
	"github.com/kiff/cloud/api/internal/receipt"
	cloudruntime "github.com/kiff/cloud/api/internal/runtime"
	"github.com/kiff/cloud/api/internal/stream"
	"github.com/kiff/cloud/api/internal/support"
	"github.com/kiff/cloud/api/internal/tenant"
	"github.com/kiff/cloud/api/internal/usage"
)

// Every route a test key is denied is one the API registers, so a route
// renamed or removed fails here instead of quietly leaving the list.
func TestEveryDeniedTestKeyRouteIsARegisteredRoute(t *testing.T) {
	routes := registeredRoutes(t)
	for _, pattern := range auth.TestKeyDeniedRoutes {
		if !slices.Contains(routes, pattern) {
			t.Errorf("%s is in auth.TestKeyDeniedRoutes but no handler registers it", pattern)
		}
	}
}

// Through the real router, as main builds it with the proof test keys: a
// session holding the test phone's key on its own test tenant gets 403
// test_key_route on every denied route, and still reaches a read.
func TestATestKeyIsDeniedEveryListedRouteThroughTheRouter(t *testing.T) {
	ctx := context.Background()
	const testTenant = "01HZZZTESTTENANT0000000000"
	tenants := tenant.NewMemoryStore()
	if err := tenants.Create(ctx, tenant.Tenant{ID: testTenant, Slug: "proofs", DisplayName: "Proofs", Status: tenant.StatusActive,
		Metadata: map[string]any{tenant.MetadataKeyPlan: string(tenant.PlanInternal)}}); err != nil {
		t.Fatal(err)
	}
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(&k.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	testKeys, bad := proofs.ParseTestKeys([]string{auth.DeviceKeyFingerprint(der)})
	if len(bad) > 0 || !testKeys.Has(der) {
		t.Fatalf("the test key doesn't parse: %v", bad)
	}
	sessions := auth.NewMemoryDeveloperSessionStore()
	gen, _ := auth.NewDevSessionToken()
	now := time.Now()
	if err := sessions.Insert(ctx, auth.DeveloperSession{ID: "sess-test-phone", Subject: "proof-runner", Tenant: testTenant,
		TokenHash: gen.Hash, CreatedAt: now, ExpiresAt: now.Add(time.Hour), DevicePublicKey: der}); err != nil {
		t.Fatal(err)
	}
	resolver := auth.NewMapResolver()
	source := domain.NewMemorySource()
	h := newRouter(routerDeps{
		provider: auth.NewStubProvider(resolver), apiKeyStore: auth.NewMemoryAPIKeyStore(),
		walletNonceStore: auth.NewMemoryWalletNonceStore(), walletStore: auth.NewMemoryWalletStore(),
		receiptStore: receipt.NewMemoryStore(), tenantStore: tenants, domainSource: source,
		draftSource: domain.NewMemoryDraftSource(), controlStore: domain.NewMemoryControlStore(),
		supportStore: support.NewMemoryStore(), alertStore: alert.NewMemoryStore(), alertDeliveries: alert.NewMemoryDeliveryStore(),
		usageStore: usage.NewMemoryStore(), guardStore: guard.NewMemoryStore(), mandateStore: mandate.NewMemoryStore(),
		agentStore: agents.NewMemoryStore(), broker: stream.NewBroker(), resolver: resolver,
		tenantRuntime:   cloudruntime.New(cloudruntime.NewMemoryResolver(tenants, source, executors.DefaultRegistry())),
		devSessionStore: sessions, deviceStore: auth.NewMemoryDeviceStore(), passkeys: auth.NewMemoryPasskeyStore(),
		proofTestKeys: testKeys, proofTestTenants: []string{testTenant},
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	call := func(method, path string) (int, string) {
		req := httptest.NewRequest(method, path, strings.NewReader("{}"))
		req.Header.Set("Authorization", "Bearer "+gen.Plaintext)
		req.Header.Set("Content-Type", "application/json")
		rctx, cancel := context.WithTimeout(req.Context(), 300*time.Millisecond)
		defer cancel()
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req.WithContext(rctx))
		var body map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		why, _ := body["error"].(string)
		return rec.Code, why
	}
	param := regexp.MustCompile(`\{[^}]+\}`)
	for _, pattern := range auth.TestKeyDeniedRoutes {
		method, p, _ := strings.Cut(pattern, " ")
		path := param.ReplaceAllString(p, "x")
		if code, why := call(method, path); code != http.StatusForbidden || why != auth.TestKeyRouteCode {
			t.Errorf("%s %s with the test key: %d %q, want 403 %s", method, path, code, why, auth.TestKeyRouteCode)
		}
	}
	// A read isn't on the list: the test key is past this check.
	if code, why := call(http.MethodGet, "/v1/me"); why == auth.TestKeyRouteCode || why == auth.KeyRestrictedCode || code == http.StatusUnauthorized {
		t.Fatalf("GET /v1/me with the test key: %d %q", code, why)
	}
}
