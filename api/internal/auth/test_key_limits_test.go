package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/membership"
)

// serveKey sends method path with token through h and answers the status
// and the error code.
func serveKey(t *testing.T, h http.Handler, token, method, path string) (int, string) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var body map[string]string
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	return rec.Code, body["error"]
}

func splitRoute(t *testing.T, pattern string) (string, string) {
	t.Helper()
	method, path, ok := strings.Cut(pattern, " ")
	if !ok {
		t.Fatalf("pattern %q has no method", pattern)
	}
	return method, path
}

// shortRestriction keeps the test key to two test tenants: "tenant-test",
// a robot test user's, held to short sessions, and "tenant-ci", the CI
// phones', which isn't.
func (rig *testKeyRig) shortRestriction() *KeyRestriction {
	return NewKeyRestriction(func(der []byte) bool { return bytes.Equal(der, rig.key) }, []string{"tenant-test", "tenant-ci"}).
		WithShortSessions([]string{"tenant-test", " ", "tenant-not-a-test-tenant"})
}

// On a short-session test tenant, a session made with a test key lives two
// weeks, not the usual 90 days. On another test tenant (the CI's), and for
// any other key, a session keeps the lifetime it's given.
func TestATestKeysSessionLivesTwoWeeksOnAShortSessionTenant(t *testing.T) {
	rig := newTestKeyRig(t)
	k := rig.shortRestriction()
	const usual = 90 * 24 * time.Hour
	for name, keys := range map[string][][]byte{
		"device key":          {rig.key, nil},
		"approval key":        {rig.other, rig.key},
		"approval key, alone": {nil, rig.key},
	} {
		if got := k.SessionLifetime(usual, "tenant-test", keys...); got != TestKeySessionLifetime {
			t.Errorf("%s: %v, want %v", name, got, TestKeySessionLifetime)
		}
		if got := k.SessionLifetime(usual, "tenant-ci", keys...); got != usual {
			t.Errorf("%s on the CI's test tenant: %v, want %v", name, got, usual)
		}
	}
	if got := k.SessionLifetime(usual, "tenant-test", rig.other, nil); got != usual {
		t.Errorf("another key: %v", got)
	}
	if got := k.SessionLifetime(time.Hour, "tenant-test", rig.key); got != time.Hour {
		t.Errorf("a shorter lifetime stays: %v", got)
	}
	if TestKeySessionLifetime != 14*24*time.Hour {
		t.Fatalf("TestKeySessionLifetime = %v", TestKeySessionLifetime)
	}
	// With no short-session tenant named, no session is shortened.
	if got := rig.restriction().SessionLifetime(usual, "tenant-test", rig.key); got != usual {
		t.Errorf("no short-session tenants: %v", got)
	}
	var none *KeyRestriction
	if got := none.SessionLifetime(usual, "tenant-test", rig.key); got != usual {
		t.Errorf("no restriction: %v", got)
	}
}

// On a short-session test tenant, a session holding a test key is refused
// once it's two weeks old, whatever its own expiry says: one made before the
// limit, or given a test approval key later. On the CI's test tenant a test
// key's 30-day-old session still works, and another phone's isn't touched.
func TestATestKeysSessionEndsAfterTwoWeeksOnAShortSessionTenant(t *testing.T) {
	rig := newTestKeyRig(t)
	now := time.Now()
	add := func(name, tenant string, created time.Time, device, approval []byte) {
		gen, _ := NewDevSessionToken()
		if err := rig.sessions.Insert(context.Background(), DeveloperSession{ID: "sess-" + name, Subject: "human-" + tenant,
			Tenant: tenant, TokenHash: gen.Hash, CreatedAt: created, ExpiresAt: now.Add(80 * 24 * time.Hour),
			DevicePublicKey: device, ApprovalPublicKey: approval}); err != nil {
			t.Fatal(err)
		}
		rig.tokens[name] = gen.Plaintext
	}
	add("test-15-days", "tenant-test", now.Add(-15*24*time.Hour), rig.key, nil)
	add("test-approval-15-days", "tenant-test", now.Add(-15*24*time.Hour), nil, rig.key)
	add("test-13-days", "tenant-test", now.Add(-13*24*time.Hour), rig.key, nil)
	add("ci-30-days", "tenant-ci", now.Add(-30*24*time.Hour), rig.key, nil)
	add("ci-approval-30-days", "tenant-ci", now.Add(-30*24*time.Hour), nil, rig.key)
	add("other-60-days", "tenant-test", now.Add(-60*24*time.Hour), rig.other, nil)
	resolver := NewMapResolver()
	mw := NewMiddleware(NewStubProvider(resolver), NewMemoryAPIKeyStore(), resolver,
		WithDeveloperSessions(rig.sessions), WithKeyRestriction(rig.shortRestriction()), WithClock(func() time.Time { return now }))
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	for name, want := range map[string]int{
		"test-15-days": http.StatusUnauthorized, "test-approval-15-days": http.StatusUnauthorized,
		"test-13-days": http.StatusOK, "ci-30-days": http.StatusOK, "ci-approval-30-days": http.StatusOK, "other-60-days": http.StatusOK,
	} {
		if code, _ := serveKey(t, mw.Wrap(next), rig.tokens[name], http.MethodGet, "/v1/me"); code != want {
			t.Errorf("%s: %d, want %d", name, code, want)
		}
	}
	// With no short-session tenant named (as CI's account ran when this
	// broke it), a test key's 15-day-old session works.
	open := NewMiddleware(NewStubProvider(resolver), NewMemoryAPIKeyStore(), resolver,
		WithDeveloperSessions(rig.sessions), WithKeyRestriction(rig.restriction()), WithClock(func() time.Time { return now }))
	if code, _ := serveKey(t, open.Wrap(next), rig.tokens["test-15-days"], http.MethodGet, "/v1/me"); code != http.StatusOK {
		t.Errorf("no short-session tenants, a 15-day session: %d", code)
	}
}

// Even on its own test tenant, a test key can't delete the account, pay, or
// make or register a key: 403 test_key_route on each of those routes, by
// every path the middleware hands on an identity. Everything else it may
// still do, and another phone may do all of it.
func TestATestKeyIsDeniedTheAccountsDangerousRoutes(t *testing.T) {
	rig := newTestKeyRig(t)
	resolver := NewMapResolver()
	mw := NewMiddleware(NewStubProvider(resolver), NewMemoryAPIKeyStore(), resolver,
		WithDeveloperSessions(rig.sessions), WithKeyRestriction(rig.restriction()))
	reached := 0
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { reached++; w.WriteHeader(http.StatusOK) })
	for _, wrap := range []struct {
		name string
		h    http.Handler
	}{{"Wrap", mw.Wrap(next)}, {"WrapPerson", mw.WrapPerson(next)}} {
		for _, pattern := range TestKeyDeniedRoutes {
			method, path := splitRoute(t, pattern)
			for _, s := range []string{"device-on-test", "approval-on-test"} {
				if code, why := serveKey(t, wrap.h, rig.tokens[s], method, path); code != http.StatusForbidden || why != TestKeyRouteCode {
					t.Errorf("%s %s with %s: %d %q, want 403 %s", wrap.name, pattern, s, code, why, TestKeyRouteCode)
				}
			}
			if code, _ := serveKey(t, wrap.h, rig.tokens["plain-phone-on-customer"], method, path); code != http.StatusOK {
				t.Errorf("%s %s with another phone: %d", wrap.name, pattern, code)
			}
		}
		// A route not on the list: the test key goes on.
		for _, rt := range []struct{ method, path string }{{http.MethodGet, "/v1/me"}, {http.MethodGet, "/v1/keys"},
			{http.MethodPost, "/v1/me/agent-drafts"}, {http.MethodDelete, "/v1/keys/k1"}} {
			if code, _ := serveKey(t, wrap.h, rig.tokens["device-on-test"], rt.method, rt.path); code != http.StatusOK {
				t.Errorf("%s %s %s with the test key: %d", wrap.name, rt.method, rt.path, code)
			}
		}
	}
	if want := 2 * (len(TestKeyDeniedRoutes) + 4); reached != want {
		t.Fatalf("handler reached %d times, want %d", reached, want)
	}
	// On a customer's tenant it stays proof_test_key, as before.
	if code, why := serveKey(t, mw.Wrap(next), rig.tokens["device-on-customer"], http.MethodPost, "/v1/keys"); code != http.StatusForbidden || why != KeyRestrictedCode {
		t.Fatalf("off its tenant: %d %q", code, why)
	}
}

// The membership path (production's) refuses them too.
func TestATestKeyIsDeniedTheDangerousRoutesOnTheMembershipPath(t *testing.T) {
	rig := newTestKeyRig(t)
	f := newMemberFixture(t)
	f.invite("tenant-test", "owner-t", "human-tenant-test", membership.RoleAdmin)
	f.mw = NewMiddleware(NewStubProvider(f.resolver), f.keys, f.resolver, WithMemberships(f.svc),
		WithDeveloperSessions(rig.sessions), WithKeyRestriction(rig.restriction()))
	h := f.mw.Wrap(&authedHandler{})
	for _, pattern := range TestKeyDeniedRoutes {
		method, path := splitRoute(t, pattern)
		if code, why := serveKey(t, h, rig.tokens["approval-on-test"], method, path); code != http.StatusForbidden || why != TestKeyRouteCode {
			t.Errorf("%s: %d %q", pattern, code, why)
		}
	}
	if code, _, body := f.do(rig.tokens["approval-on-test"], ""); code != http.StatusOK {
		t.Fatalf("GET /v1/me: %d %v", code, body)
	}
}

// While an operator has a test tenant suspended, nothing on it reaches any
// route, reads included: its test keys, and a session without one. Once it's
// unsuspended they're back. A suspension that can't be read refuses. A real
// tenant's phone is untouched here, suspended or not.
func TestASuspendedTestTenantsKeysReachNoRoute(t *testing.T) {
	rig := newTestKeyRig(t)
	// Anything else on the test tenant: a session with no test key.
	gen, _ := NewDevSessionToken()
	if err := rig.sessions.Insert(context.Background(), DeveloperSession{ID: "sess-plain-on-test", Subject: "human-tenant-test",
		Tenant: "tenant-test", TokenHash: gen.Hash, CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	rig.tokens["plain-on-test"] = gen.Plaintext
	suspended := map[string]bool{"tenant-test": true, "tenant-customer": true}
	var readErr error
	k := rig.restriction().WithSuspendedCheck(func(_ context.Context, tenant string) (bool, error) {
		return suspended[tenant], readErr
	})
	resolver := NewMapResolver()
	mw := NewMiddleware(NewStubProvider(resolver), NewMemoryAPIKeyStore(), resolver,
		WithDeveloperSessions(rig.sessions), WithKeyRestriction(k))
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	routes := append([]struct{ method, path string }{{http.MethodGet, "/v1/me"}, {http.MethodGet, "/v1/me/agents"},
		{http.MethodPost, "/v1/me/agent-drafts"}, {http.MethodPost, "/v1/proposals"}}, restrictedRoutes...)
	for _, h := range []http.Handler{mw.Wrap(next), mw.WrapPerson(next)} {
		for _, rt := range routes {
			for _, s := range []string{"device-on-test", "approval-on-test", "plain-on-test"} {
				if code, why := serveKey(t, h, rig.tokens[s], rt.method, rt.path); code != http.StatusForbidden || why != TenantSuspendedCode {
					t.Errorf("suspended, %s %s with %s: %d %q", rt.method, rt.path, s, code, why)
				}
			}
			// A suspended tenant that isn't a test tenant isn't asked here.
			if code, _ := serveKey(t, h, rig.tokens["plain-phone-on-customer"], rt.method, rt.path); code != http.StatusOK {
				t.Errorf("a suspended real tenant's phone, %s %s: %d", rt.method, rt.path, code)
			}
		}
	}
	suspended["tenant-test"] = false
	if code, _ := serveKey(t, mw.Wrap(next), rig.tokens["device-on-test"], http.MethodGet, "/v1/me"); code != http.StatusOK {
		t.Fatalf("unsuspended: %d", code)
	}
	readErr = errors.New("the tenant store is down")
	if code, why := serveKey(t, mw.Wrap(next), rig.tokens["device-on-test"], http.MethodGet, "/v1/me"); code != http.StatusForbidden || why != TenantSuspendedCode {
		t.Fatalf("a suspension that can't be read: %d %q", code, why)
	}
}
