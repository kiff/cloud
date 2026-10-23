package auth

import (
	"context"
	"crypto/elliptic"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestDevSessionToken_RoundTrip(t *testing.T) {
	t.Parallel()
	gen, err := NewDevSessionToken()
	if err != nil {
		t.Fatalf("NewDevSessionToken: %v", err)
	}
	if !strings.HasPrefix(gen.Plaintext, DevSessionPrefix) {
		t.Errorf("plaintext missing prefix: %q", gen.Plaintext)
	}
	parsed, err := ParseDevSessionToken(gen.Plaintext)
	if err != nil {
		t.Fatalf("ParseDevSessionToken: %v", err)
	}
	if parsed.Hash != gen.Hash {
		t.Error("parsed hash != generated hash")
	}
	// Bearer prefix tolerated.
	if _, err := ParseDevSessionToken("Bearer " + gen.Plaintext); err != nil {
		t.Errorf("bearer-prefixed token should parse: %v", err)
	}
}

func TestParseDevSessionToken_Rejects(t *testing.T) {
	t.Parallel()
	bad := []string{"", "nope", "kiff_live_acme_" + strings.Repeat("a", 32), DevSessionPrefix + "short", DevSessionPrefix + strings.Repeat("A", 32)}
	for _, b := range bad {
		if _, err := ParseDevSessionToken(b); err == nil {
			t.Errorf("expected %q to be rejected", b)
		}
	}
}

func TestMemoryDeveloperSessionStore(t *testing.T) {
	t.Parallel()
	store := NewMemoryDeveloperSessionStore()
	ctx := context.Background()
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	gen, _ := NewDevSessionToken()
	sess := DeveloperSession{
		ID: "sess-1", Subject: "u1", Tenant: "t1", Label: "laptop",
		TokenHash: gen.Hash, CreatedAt: now, ExpiresAt: now.Add(24 * time.Hour),
	}
	if err := store.Insert(ctx, sess); err != nil {
		t.Fatalf("insert: %v", err)
	}
	// Active lookup.
	got, err := store.GetByHash(ctx, gen.Hash, now)
	if err != nil || got.ID != "sess-1" {
		t.Fatalf("get: %v (%+v)", err, got)
	}
	// Expired.
	if _, err := store.GetByHash(ctx, gen.Hash, now.Add(48*time.Hour)); err != ErrDevSessionExpired {
		t.Errorf("want ErrDevSessionExpired, got %v", err)
	}
	// Per-device revoke via RevokeByHash (the logout shape).
	if err := store.RevokeByHash(ctx, gen.Hash, now); err != nil {
		t.Fatalf("revoke by hash: %v", err)
	}
	if _, err := store.GetByHash(ctx, gen.Hash, now); err != ErrDevSessionRevoked {
		t.Errorf("want ErrDevSessionRevoked, got %v", err)
	}
	// Revoked sessions drop out of the active list.
	list, _ := store.ListActiveForSubject(ctx, "u1", now)
	if len(list) != 0 {
		t.Errorf("revoked session should not be listed active; got %d", len(list))
	}
}

func TestMemoryDeveloperSessionStore_RevokeAllForSubject(t *testing.T) {
	t.Parallel()
	store := NewMemoryDeveloperSessionStore()
	ctx := context.Background()
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	for _, id := range []string{"a", "b", "c"} {
		gen, _ := NewDevSessionToken()
		subj := "u1"
		if id == "c" {
			subj = "other" // different subject, must be untouched
		}
		_ = store.Insert(ctx, DeveloperSession{
			ID: id, Subject: subj, Tenant: "t1", TokenHash: gen.Hash,
			CreatedAt: now, ExpiresAt: now.Add(time.Hour),
		})
	}
	n, err := store.RevokeAllForSubject(ctx, "u1", now)
	if err != nil || n != 2 {
		t.Fatalf("RevokeAllForSubject = %d, %v; want 2", n, err)
	}
	if list, _ := store.ListActiveForSubject(ctx, "u1", now); len(list) != 0 {
		t.Errorf("u1 should have no active sessions; got %d", len(list))
	}
	if list, _ := store.ListActiveForSubject(ctx, "other", now); len(list) != 1 {
		t.Errorf("other subject must be untouched; got %d active", len(list))
	}
}

// TestMiddleware_DeveloperSession proves a minted developer-session
// token authenticates through the middleware, resolves to the record's
// tenant, and is widened to the owner role (tenant_owner) like a
// session — so it can drive Tier-2 operations.
func TestMiddleware_DeveloperSession(t *testing.T) {
	t.Parallel()
	store := NewMemoryDeveloperSessionStore()
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	gen, _ := NewDevSessionToken()
	if err := store.Insert(context.Background(), DeveloperSession{
		ID: "sess-1", Subject: "human-1", Tenant: "tenant-9",
		TokenHash: gen.Hash, CreatedAt: now, ExpiresAt: now.Add(24 * time.Hour),
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}

	resolver := NewMapResolver()
	mw := NewMiddleware(NewStubProvider(resolver), NewMemoryAPIKeyStore(), resolver,
		WithClock(fixedClock(now)),
		WithDeveloperSessions(store),
	)

	var got Identity
	next := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got, _ = IdentityFrom(r.Context())
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/me", nil)
	req.Header.Set("Authorization", "Bearer "+gen.Plaintext)
	mw.Wrap(next).ServeHTTP(rec, req)

	if rec.Code == http.StatusUnauthorized {
		t.Fatalf("developer session should authenticate; got 401")
	}
	if got.Tenant != "tenant-9" || got.Subject != "human-1" {
		t.Errorf("identity tenant/subject = %q/%q", got.Tenant, got.Subject)
	}
	if got.Method != MethodDeveloper {
		t.Errorf("method = %q, want developer", got.Method)
	}
	if !got.HasRole("tenant_owner") {
		t.Errorf("developer session must be owner-plane (tenant_owner); roles=%v", got.Roles)
	}
	if got.APIKey {
		t.Error("developer session must not be flagged APIKey")
	}
}

// TestMiddleware_DeveloperSessionDisabled confirms a kiff_dev_ token is
// rejected when no developer-session store is wired.
func TestMiddleware_DeveloperSessionDisabled(t *testing.T) {
	t.Parallel()
	resolver := NewMapResolver()
	mw := NewMiddleware(NewStubProvider(resolver), NewMemoryAPIKeyStore(), resolver)
	gen, _ := NewDevSessionToken()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/me", nil)
	req.Header.Set("Authorization", "Bearer "+gen.Plaintext)
	mw.Wrap(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})).ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("dev token with no store should 401; got %d", rec.Code)
	}
}

// A developer session bound to a phone's key puts that key, and
// the session's id, on the identity; one without a key puts neither.
func TestMiddleware_DeveloperSessionCarriesItsDeviceKey(t *testing.T) {
	t.Parallel()
	store := NewMemoryDeveloperSessionStore()
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	_, pub := testDeviceKey(t, elliptic.P256())
	der, _ := ParseDeviceKey(pub)
	phone, _ := NewDevSessionToken()
	cli, _ := NewDevSessionToken()
	for _, s := range []DeveloperSession{
		{ID: "sess-phone", Subject: "human-1", Tenant: "tenant-9", TokenHash: phone.Hash, CreatedAt: now, ExpiresAt: now.Add(time.Hour), DevicePublicKey: der},
		{ID: "sess-cli", Subject: "human-1", Tenant: "tenant-9", TokenHash: cli.Hash, CreatedAt: now, ExpiresAt: now.Add(time.Hour)},
	} {
		if err := store.Insert(context.Background(), s); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}
	resolver := NewMapResolver()
	mw := NewMiddleware(NewStubProvider(resolver), NewMemoryAPIKeyStore(), resolver, WithClock(fixedClock(now)), WithDeveloperSessions(store))
	identityFor := func(token string) Identity {
		var got Identity
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/v1/me", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		mw.Wrap(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { got, _ = IdentityFrom(r.Context()) })).ServeHTTP(rec, req)
		return got
	}
	if got := identityFor(phone.Plaintext); string(got.DeviceKey) != string(der) || got.DeviceSessionID != "sess-phone" {
		t.Errorf("phone identity key/session = %d bytes/%q", len(got.DeviceKey), got.DeviceSessionID)
	}
	if got := identityFor(cli.Plaintext); got.DeviceKey != nil || got.DeviceSessionID != "" {
		t.Errorf("CLI identity carries a device key: %+v", got)
	}
}

// A session from a device signs with that device's key, and the
// device decides: once it's removed, its session is over at the next
// request, whatever the session says. A session naming a device that can't
// be checked, or one of another person's, is refused.
func TestMiddleware_DeveloperSessionFollowsItsDevice(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	sessions := NewMemoryDeveloperSessionStore()
	devices := NewMemoryDeviceStore()
	now := time.Date(2026, 10, 19, 9, 0, 0, 0, time.UTC)
	_, pub := testDeviceKey(t, elliptic.P256())
	der, _ := ParseDeviceKey(pub)
	ipad := ApprovalDevice{ID: NewDeviceID(), Tenant: "tenant-9", Subject: "human-1", Name: "tafs on iPad", Platform: "ipados", PublicKey: der, CreatedAt: now}
	theirs := ApprovalDevice{ID: NewDeviceID(), Tenant: "tenant-9", Subject: "human-2", Name: "theirs", Platform: "ios", PublicKey: der, CreatedAt: now}
	for _, d := range []ApprovalDevice{ipad, theirs} {
		if err := devices.Insert(ctx, d); err != nil {
			t.Fatal(err)
		}
	}
	mine, _ := NewDevSessionToken()
	borrowed, _ := NewDevSessionToken()
	for _, s := range []DeveloperSession{
		{ID: "sess-ipad", Subject: "human-1", Tenant: "tenant-9", Label: "old label", TokenHash: mine.Hash, CreatedAt: now, ExpiresAt: now.Add(time.Hour), DevicePublicKey: der, DeviceID: ipad.ID},
		{ID: "sess-borrowed", Subject: "human-1", Tenant: "tenant-9", TokenHash: borrowed.Hash, CreatedAt: now, ExpiresAt: now.Add(time.Hour), DevicePublicKey: der, DeviceID: theirs.ID},
	} {
		if err := sessions.Insert(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	resolver := NewMapResolver()
	serve := func(mw *Middleware, token string) (Identity, int) {
		var got Identity
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/v1/me", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		mw.Wrap(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { got, _ = IdentityFrom(r.Context()) })).ServeHTTP(rec, req)
		return got, rec.Code
	}
	mw := NewMiddleware(NewStubProvider(resolver), NewMemoryAPIKeyStore(), resolver, WithClock(fixedClock(now)),
		WithDeveloperSessions(sessions), WithDevices(devices))

	got, code := serve(mw, mine.Plaintext)
	if code != http.StatusOK || got.DeviceID != ipad.ID || string(got.DeviceKey) != string(der) || got.DeviceName != "tafs on iPad" || got.DeviceSessionID != "sess-ipad" {
		t.Fatalf("the iPad's identity = %+v (%d)", got, code)
	}
	if _, code := serve(mw, borrowed.Plaintext); code != http.StatusUnauthorized {
		t.Errorf("a session naming another person's device: %d", code)
	}
	noDevices := NewMiddleware(NewStubProvider(resolver), NewMemoryAPIKeyStore(), resolver, WithClock(fixedClock(now)), WithDeveloperSessions(sessions))
	if _, code := serve(noDevices, mine.Plaintext); code != http.StatusUnauthorized {
		t.Errorf("a device that can't be checked: %d", code)
	}
	if err := devices.Revoke(ctx, ipad.ID, now); err != nil {
		t.Fatal(err)
	}
	if _, code := serve(mw, mine.Plaintext); code != http.StatusUnauthorized {
		t.Errorf("the removed iPad's session still works: %d", code)
	}
}
