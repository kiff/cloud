package auth

import (
	"bytes"
	"context"
	"crypto/elliptic"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/membership"
)

// testKeyRig: one restricted key (the proof runner's test phone), kept to
// the tenant "tenant-test"; developer sessions on it and on a customer's
// tenant, with the key as device key or as approval key alone.
type testKeyRig struct {
	key      []byte
	other    []byte
	sessions *MemoryDeveloperSessionStore
	tokens   map[string]string
}

func newTestKeyRig(t *testing.T) *testKeyRig {
	t.Helper()
	_, pub := testDeviceKey(t, elliptic.P256())
	key, _ := ParseDeviceKey(pub)
	_, pub2 := testDeviceKey(t, elliptic.P256())
	other, _ := ParseDeviceKey(pub2)
	rig := &testKeyRig{key: key, other: other, sessions: NewMemoryDeveloperSessionStore(), tokens: map[string]string{}}
	now := time.Now()
	add := func(name, tenant string, device, approval []byte) {
		gen, _ := NewDevSessionToken()
		if err := rig.sessions.Insert(context.Background(), DeveloperSession{ID: "sess-" + name, Subject: "human-" + tenant,
			Tenant: tenant, TokenHash: gen.Hash, CreatedAt: now, ExpiresAt: now.Add(time.Hour),
			DevicePublicKey: device, ApprovalPublicKey: approval}); err != nil {
			t.Fatal(err)
		}
		rig.tokens[name] = gen.Plaintext
	}
	add("device-on-customer", "tenant-customer", key, nil)
	add("approval-only-on-customer", "tenant-customer", nil, key)
	add("approval-on-customer", "tenant-customer", other, key)
	add("device-on-test", "tenant-test", key, nil)
	add("approval-on-test", "tenant-test", other, key)
	add("plain-phone-on-customer", "tenant-customer", other, nil)
	return rig
}

func (rig *testKeyRig) restriction() *KeyRestriction {
	return NewKeyRestriction(func(der []byte) bool { return bytes.Equal(der, rig.key) }, []string{"tenant-test", " "})
}

// The routes the review named, and a read: every one takes its keys from
// the identity the middleware hands on, so the one check covers them all.
var restrictedRoutes = []struct{ method, path string }{
	{http.MethodPost, "/v1/me/device-signature"},
	{http.MethodPost, "/v1/me/card-exceptions/ex-1/approve-once"},
	{http.MethodPost, "/v1/me/card-exceptions/ex-1/confirm"},
	{http.MethodPost, "/v1/me/window"},
	{http.MethodPost, "/v1/me/pairings/approve"},
	{http.MethodPost, "/v1/me/account/deletion"},
	{http.MethodGet, "/v1/me"},
}

func TestARestrictedKeyReachesNoRouteOutsideItsTenants(t *testing.T) {
	rig := newTestKeyRig(t)
	resolver := NewMapResolver()
	mw := NewMiddleware(NewStubProvider(resolver), NewMemoryAPIKeyStore(), resolver,
		WithDeveloperSessions(rig.sessions), WithKeyRestriction(rig.restriction()))
	reached := 0
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { reached++; w.WriteHeader(http.StatusOK) })
	serve := func(h http.Handler, token, method, path string) int {
		req := httptest.NewRequest(method, path, strings.NewReader(`{}`))
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code == http.StatusForbidden && !strings.Contains(rec.Body.String(), KeyRestrictedCode) {
			t.Errorf("%s %s: 403 without %s: %s", method, path, KeyRestrictedCode, rec.Body)
		}
		return rec.Code
	}
	for _, wrap := range []struct {
		name string
		h    http.Handler
	}{{"Wrap", mw.Wrap(next)}, {"WrapPerson", mw.WrapPerson(next)}} {
		for _, rt := range restrictedRoutes {
			for _, s := range []string{"device-on-customer", "approval-only-on-customer", "approval-on-customer"} {
				if code := serve(wrap.h, rig.tokens[s], rt.method, rt.path); code != http.StatusForbidden {
					t.Errorf("%s %s %s with %s: %d, want 403", wrap.name, rt.method, rt.path, s, code)
				}
			}
			for _, s := range []string{"device-on-test", "approval-on-test", "plain-phone-on-customer"} {
				if code := serve(wrap.h, rig.tokens[s], rt.method, rt.path); code != http.StatusOK {
					t.Errorf("%s %s %s with %s: %d, want 200", wrap.name, rt.method, rt.path, s, code)
				}
			}
		}
	}
	if want := 2 * len(restrictedRoutes) * 3; reached != want {
		t.Fatalf("handler reached %d times, want %d (only the allowed sessions)", reached, want)
	}

	// No restriction configured: the test key is any phone's key.
	open := NewMiddleware(NewStubProvider(resolver), NewMemoryAPIKeyStore(), resolver, WithDeveloperSessions(rig.sessions))
	if code := serve(open.Wrap(next), rig.tokens["device-on-customer"], http.MethodGet, "/v1/me"); code != http.StatusOK {
		t.Fatalf("without the list: %d", code)
	}
}

// The membership path (the production one) checks the account the session
// resolves to.
func TestARestrictedKeyIsRefusedOnTheMembershipPath(t *testing.T) {
	rig := newTestKeyRig(t)
	f := newMemberFixture(t)
	f.invite("tenant-customer", "owner-c", "human-tenant-customer", membership.RoleAdmin)
	f.invite("tenant-test", "owner-t", "human-tenant-test", membership.RoleAdmin)
	f.mw = NewMiddleware(NewStubProvider(f.resolver), f.keys, f.resolver, WithMemberships(f.svc),
		WithDeveloperSessions(rig.sessions), WithKeyRestriction(rig.restriction()))
	if code, _, body := f.do(rig.tokens["approval-only-on-customer"], ""); code != http.StatusForbidden {
		t.Fatalf("approval-only on a customer's tenant: %d %v", code, body)
	}
	if code, id, body := f.do(rig.tokens["approval-on-test"], ""); code != http.StatusOK || id.Tenant != "tenant-test" {
		t.Fatalf("on the test tenant: %d %+v %v", code, id, body)
	}
}

func TestKeyRestrictionAllows(t *testing.T) {
	bg := context.Background()
	var none *KeyRestriction
	if !none.Allows(bg, "any", []byte("k")) || NewKeyRestriction(nil, []string{"t"}) != nil {
		t.Fatal("a nil restriction restricted")
	}
	k := NewKeyRestriction(func(der []byte) bool { return string(der) == "test" }, []string{"t"})
	switch {
	case k.Allows(bg, "c", []byte("test")), k.Allows(bg, "", []byte("test")), k.Allows(bg, "c", nil, []byte("test")):
		t.Fatal("the test key allowed off its tenant")
	case !k.Allows(bg, "t", []byte("test")), !k.Allows(bg, "c", []byte("other")), !k.Allows(bg, "c"):
		t.Fatal("refused what it should allow")
	}
}

// The use check is asked at each use of a restricted key on a listed tenant,
// and only then: a key that isn't restricted, or a tenant that isn't
// listed, never reaches it.
func TestKeyRestrictionUseCheck(t *testing.T) {
	bg := context.Background()
	still := map[string]bool{"t": true}
	var asked []string
	k := NewKeyRestriction(func(der []byte) bool { return string(der) == "test" }, []string{"t", "u"}).
		WithUseCheck(func(_ context.Context, tenant string) bool { asked = append(asked, tenant); return still[tenant] })
	switch {
	case !k.Allows(bg, "t", []byte("test")):
		t.Fatal("refused on a tenant that still passes")
	case k.Allows(bg, "u", []byte("test")), k.Allows(bg, "u", []byte("other"), []byte("test")):
		t.Fatal("allowed on a tenant that no longer passes")
	case !k.Allows(bg, "u", []byte("other")), !k.Allows(bg, "c", []byte("other")):
		t.Fatal("a key that isn't restricted was refused")
	case k.Allows(bg, "c", []byte("test")):
		t.Fatal("allowed off the list")
	}
	if want := []string{"t", "u", "u"}; strings.Join(asked, ",") != strings.Join(want, ",") {
		t.Fatalf("asked %v, want %v", asked, want)
	}
	still["t"] = false
	if k.Allows(bg, "t", []byte("test")) {
		t.Fatal("a tenant that stops passing after boot kept its test key")
	}
	var none *KeyRestriction
	if none.WithUseCheck(func(context.Context, string) bool { return false }) != nil {
		t.Fatal("WithUseCheck on nil made a restriction")
	}
}

// Through the middleware: the test tenant's session works until the use
// check stops passing, then every route refuses it with proof_test_key.
func TestARestrictedKeyIsRefusedOnceItsTenantStopsPassing(t *testing.T) {
	rig := newTestKeyRig(t)
	passes := true
	resolver := NewMapResolver()
	mw := NewMiddleware(NewStubProvider(resolver), NewMemoryAPIKeyStore(), resolver,
		WithDeveloperSessions(rig.sessions), WithKeyRestriction(rig.restriction().
			WithUseCheck(func(_ context.Context, tenant string) bool { return tenant == "tenant-test" && passes })))
	h := mw.Wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	serve := func(token string) (int, string) {
		req := httptest.NewRequest(http.MethodPost, "/v1/me/card-exceptions/ex-1/approve-once", strings.NewReader(`{}`))
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}
	if code, body := serve(rig.tokens["approval-on-test"]); code != http.StatusOK {
		t.Fatalf("before: %d %s", code, body)
	}
	passes = false
	for _, s := range []string{"approval-on-test", "device-on-test"} {
		if code, body := serve(rig.tokens[s]); code != http.StatusForbidden || !strings.Contains(body, KeyRestrictedCode) {
			t.Fatalf("%s after: %d %s", s, code, body)
		}
	}
	if code, body := serve(rig.tokens["plain-phone-on-customer"]); code != http.StatusOK {
		t.Fatalf("a plain phone: %d %s", code, body)
	}
}
