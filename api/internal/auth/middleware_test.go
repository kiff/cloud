package auth

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kiff/kiff/pkg/kiff/actor"
)

// fixedClock returns the given time, useful for asserting Touch
// timestamps in tests.
func fixedClock(at time.Time) func() time.Time { return func() time.Time { return at.UTC() } }

// authedHandler is the downstream handler used by middleware
// tests. It writes back the Identity + actor.Actor it sees on the
// request context, so tests can assert that both were attached.
type authedHandler struct {
	called bool
	body   map[string]any
	id     Identity
	actor  actor.Actor
	hasErr error
}

func (h *authedHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.called = true
	id, err := IdentityFrom(r.Context())
	if err != nil {
		h.hasErr = err
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	a, err := ActorFrom(r.Context())
	if err != nil {
		h.hasErr = err
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	h.id = id
	h.actor = a
	h.body = map[string]any{
		"identity": id,
		"actor":    a,
	}
	_ = json.NewEncoder(w).Encode(h.body)
}

func newMiddlewareForTest(t *testing.T, provider Provider, store APIKeyStore, resolver ActorResolver, clock time.Time) *Middleware {
	t.Helper()
	return NewMiddleware(provider, store, resolver, WithClock(fixedClock(clock)))
}

// ─── Session-token path ──────────────────────────────────────────

func TestMiddleware_SessionToken_HappyPath(t *testing.T) {
	t.Parallel()

	resolver := NewMapResolver()
	a := actor.Actor{ID: "u-1", DisplayName: "Alice", Roles: []string{"admin"}}
	resolver.Set("user_alice", a, "tenant-acme")

	provider := NewStubProvider(resolver)
	mw := newMiddlewareForTest(t, provider, NewMemoryAPIKeyStore(), resolver, time.Now().UTC())

	downstream := &authedHandler{}
	srv := httptest.NewServer(mw.Wrap(downstream))
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL, nil)
	req.Header.Set("Authorization", "Bearer user_alice")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d, want 200", resp.StatusCode)
	}
	if !downstream.called {
		t.Fatalf("downstream handler was not called")
	}
	if downstream.id.Subject != "user_alice" || downstream.id.Tenant != "tenant-acme" {
		t.Errorf("identity: got %+v, want subject=user_alice tenant=tenant-acme", downstream.id)
	}
	if downstream.actor.ID != "u-1" {
		t.Errorf("actor.ID: got %q, want u-1", downstream.actor.ID)
	}
}

// TestMiddleware_SessionToken_AddsOwnerRoleAliases is a
// regression test. When the auth method hands the middleware
// a session identity for the tenant owner, the middleware adds
// the role aliases emitted by generated domains: `operator` for
// the guided-builder era and `tenant_owner` for Studio/guard
// domains. Without this, a Clerk solo user with provider role
// `admin` can sign in but still receives permission_denied on
// the first Studio proof action.
//
// The aliasing applies only to session-method paths. API-key
// paths carry their own roles from the key record.
func TestMiddleware_SessionToken_AddsOwnerRoleAliases(t *testing.T) {
	t.Parallel()

	resolver := NewMapResolver()
	// Note: no Roles on the registered actor — this is the
	// shape a fresh Clerk self-signup leaves.
	resolver.Set("user_self_signup", actor.Actor{ID: "u-self", DisplayName: "Self"}, "tenant-self")

	provider := NewStubProvider(resolver)
	mw := newMiddlewareForTest(t, provider, NewMemoryAPIKeyStore(), resolver, time.Now().UTC())

	downstream := &authedHandler{}
	srv := httptest.NewServer(mw.Wrap(downstream))
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL, nil)
	req.Header.Set("Authorization", "Bearer user_self_signup")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d, want 200", resp.StatusCode)
	}
	if !downstream.called {
		t.Fatalf("downstream handler was not called")
	}
	wantSelf := []string{"operator", "tenant_owner"}
	if !sameStrings(downstream.id.Roles, wantSelf) {
		t.Errorf("identity.Roles: got %v, want %v", downstream.id.Roles, wantSelf)
	}
	if !sameStrings(downstream.actor.Roles, wantSelf) {
		t.Errorf("actor.Roles: got %v, want %v", downstream.actor.Roles, wantSelf)
	}
	// Sanity: the existing role-from-resolver path still
	// preserves the original role and adds owner aliases. This
	// mirrors Clerk solo users, which arrive as admin before the
	// middleware appends generated-domain aliases.
	resolver.Set("user_admin", actor.Actor{ID: "u-adm", Roles: []string{"admin"}}, "tenant-adm")
	req2, _ := http.NewRequest("GET", srv.URL, nil)
	req2.Header.Set("Authorization", "Bearer user_admin")
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatalf("admin request: %v", err)
	}
	resp2.Body.Close()
	wantAdmin := []string{"admin", "operator", "tenant_owner"}
	if !sameStrings(downstream.id.Roles, wantAdmin) {
		t.Errorf("admin identity.Roles: got %v, want %v", downstream.id.Roles, wantAdmin)
	}
	if !sameStrings(downstream.actor.Roles, wantAdmin) {
		t.Errorf("admin actor.Roles: got %v, want %v", downstream.actor.Roles, wantAdmin)
	}
}

func sameStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func TestMiddleware_RejectsMissingHeader(t *testing.T) {
	t.Parallel()
	mw := newMiddlewareForTest(t, NewStubProvider(NewMapResolver()), NewMemoryAPIKeyStore(), NewMapResolver(), time.Now().UTC())
	srv := httptest.NewServer(mw.Wrap(&authedHandler{}))
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status: got %d, want 401", resp.StatusCode)
	}
	if got := resp.Header.Get("WWW-Authenticate"); !strings.HasPrefix(got, "Bearer") {
		t.Errorf("WWW-Authenticate: got %q, want starts with Bearer", got)
	}
}

func TestMiddleware_RejectsBadSessionToken(t *testing.T) {
	t.Parallel()
	mw := newMiddlewareForTest(t, NewStubProvider(NewMapResolver()), NewMemoryAPIKeyStore(), NewMapResolver(), time.Now().UTC())
	srv := httptest.NewServer(mw.Wrap(&authedHandler{}))
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL, nil)
	req.Header.Set("Authorization", "Bearer not-a-known-subject")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("got %d, want 401", resp.StatusCode)
	}
}

func TestMiddleware_RejectsResolverFailure(t *testing.T) {
	t.Parallel()
	// Provider says yes (subject is registered), resolver says no
	// because the Identity carries no tenant the resolver knows.
	resolver := NewMapResolver() // empty
	provider := NewStubProvider(NewMapResolver())
	provider.backing = NewMapResolver()
	provider.backing.Set("user_x", actor.Actor{ID: "x"}, "tenant-x")
	mw := newMiddlewareForTest(t, provider, NewMemoryAPIKeyStore(), resolver, time.Now().UTC())
	srv := httptest.NewServer(mw.Wrap(&authedHandler{}))
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL, nil)
	req.Header.Set("Authorization", "Bearer user_x")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("got %d, want 401 (resolver rejected)", resp.StatusCode)
	}
}

func TestMiddleware_ResolverBackendFailureReturnsServiceUnavailable(t *testing.T) {
	t.Parallel()
	providerBacking := NewMapResolver()
	providerBacking.Set("user_x", actor.Actor{ID: "x"}, "tenant-x")
	resolver := failingResolver{err: fmt.Errorf("%w: database offline", ErrResolverUnavailable)}
	mw := newMiddlewareForTest(t, NewStubProvider(providerBacking), NewMemoryAPIKeyStore(), resolver, time.Now().UTC())
	srv := httptest.NewServer(mw.Wrap(&authedHandler{}))
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL, nil)
	req.Header.Set("Authorization", "Bearer user_x")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("got %d, want 503", resp.StatusCode)
	}
	if got := resp.Header.Get("WWW-Authenticate"); got != "" {
		t.Fatalf("503 must not challenge authentication, got %q", got)
	}
}

type failingResolver struct{ err error }

func (f failingResolver) Resolve(context.Context, Identity) (actor.Actor, string, error) {
	return actor.Actor{}, "", f.err
}

// ─── API-key path ────────────────────────────────────────────────

func TestMiddleware_APIKey_HappyPath(t *testing.T) {
	t.Parallel()

	gen, err := NewAPIKey("acme", time.Time{})
	if err != nil {
		t.Fatalf("NewAPIKey: %v", err)
	}
	rec := APIKeyRecord{
		ID:        "key-id-1",
		Tenant:    "acme",
		Subject:   "service-loader",
		Label:     "loader",
		Roles:     []string{"operator"},
		KeyHash:   gen.Hash,
		CreatedAt: time.Now().UTC(),
	}
	store := NewMemoryAPIKeyStore()
	if err := store.Insert(context.Background(), rec); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	resolver := NewMapResolver()
	resolver.Set("service-loader", actor.Actor{ID: "svc-loader", Roles: []string{"operator"}}, "acme")

	provider := NewStubProvider(NewMapResolver()) // never reached
	clk := time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC)
	mw := newMiddlewareForTest(t, provider, store, resolver, clk)

	downstream := &authedHandler{}
	srv := httptest.NewServer(mw.Wrap(downstream))
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL, nil)
	req.Header.Set("Authorization", "Bearer "+gen.Plaintext)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d, want 200", resp.StatusCode)
	}

	if downstream.id.Method != MethodAPIKey || !downstream.id.APIKey {
		t.Errorf("identity should be method=api_key APIKey=true; got %+v", downstream.id)
	}
	if downstream.id.Subject != "service-loader" {
		t.Errorf("identity.Subject: got %q, want service-loader", downstream.id.Subject)
	}
	if downstream.id.Tenant != "acme" {
		t.Errorf("identity.Tenant: got %q, want acme", downstream.id.Tenant)
	}
	if downstream.id.Metadata["api_key_id"] != "key-id-1" {
		t.Errorf("expected api_key_id metadata=key-id-1, got %v", downstream.id.Metadata)
	}

	// Touch should have stamped LastUsedAt at the fixed clock.
	got, err := store.GetByHash(context.Background(), gen.Hash)
	if err != nil {
		t.Fatalf("GetByHash: %v", err)
	}
	if got.LastUsedAt == nil || !got.LastUsedAt.Equal(clk) {
		t.Errorf("LastUsedAt: got %v, want %v", got.LastUsedAt, clk)
	}
}

func TestMiddleware_APIKey_RevokedKey_Rejected(t *testing.T) {
	t.Parallel()
	gen, _ := NewAPIKey("acme", time.Time{})
	rec := APIKeyRecord{
		ID:        "key-id-1",
		Tenant:    "acme",
		Subject:   "service-loader",
		KeyHash:   gen.Hash,
		CreatedAt: time.Now().UTC(),
	}
	store := NewMemoryAPIKeyStore()
	_ = store.Insert(context.Background(), rec)
	_ = store.Revoke(context.Background(), rec.ID, time.Now().UTC())

	mw := newMiddlewareForTest(t, NewStubProvider(NewMapResolver()), store, NewMapResolver(), time.Now().UTC())
	srv := httptest.NewServer(mw.Wrap(&authedHandler{}))
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL, nil)
	req.Header.Set("Authorization", "Bearer "+gen.Plaintext)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("got %d, want 401", resp.StatusCode)
	}
}

func TestMiddleware_APIKey_TenantMismatch_Rejected(t *testing.T) {
	t.Parallel()
	// Plaintext claims tenant=acme; stored record claims tenant=other.
	// This mismatch must be refused even though the hash matches.
	gen, _ := NewAPIKey("acme", time.Time{})
	store := NewMemoryAPIKeyStore()
	bogus := APIKeyRecord{
		ID:        "key-id-1",
		Tenant:    "other-tenant",
		Subject:   "service",
		KeyHash:   gen.Hash,
		CreatedAt: time.Now().UTC(),
	}
	if err := store.Insert(context.Background(), bogus); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	mw := newMiddlewareForTest(t, NewStubProvider(NewMapResolver()), store, NewMapResolver(), time.Now().UTC())
	srv := httptest.NewServer(mw.Wrap(&authedHandler{}))
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL, nil)
	req.Header.Set("Authorization", "Bearer "+gen.Plaintext)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("got %d, want 401 on tenant mismatch", resp.StatusCode)
	}
}

func TestMiddleware_APIKey_MalformedKey_Rejected(t *testing.T) {
	t.Parallel()
	mw := newMiddlewareForTest(t, NewStubProvider(NewMapResolver()), NewMemoryAPIKeyStore(), NewMapResolver(), time.Now().UTC())
	srv := httptest.NewServer(mw.Wrap(&authedHandler{}))
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL, nil)
	req.Header.Set("Authorization", "Bearer kiff_live_acme_short")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("got %d, want 401", resp.StatusCode)
	}
}

func TestMiddleware_APIKey_NoResolverEntry_StillSucceeds(t *testing.T) {
	t.Parallel()

	// This bug was found in use: an operator-minted API key
	// (kiff-tenant mint-key) has a tenant in the record but no
	// MapResolver entry — by design, since the API-key path is
	// for callers without a session. The middleware must let the
	// request through and synthesize an actor.Actor from the key
	// record itself rather than 401-ing.
	gen, _ := NewAPIKey("acme", time.Time{})
	rec := APIKeyRecord{
		ID:        "key-id-1",
		Tenant:    "acme",
		Subject:   "service-bootstrap",
		Label:     "bootstrap",
		Roles:     []string{"admin"},
		KeyHash:   gen.Hash,
		CreatedAt: time.Now().UTC(),
	}
	store := NewMemoryAPIKeyStore()
	if err := store.Insert(context.Background(), rec); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	// MapResolver is intentionally empty: no entry for service-bootstrap.
	resolver := NewMapResolver()
	provider := NewStubProvider(NewMapResolver())
	mw := newMiddlewareForTest(t, provider, store, resolver, time.Now().UTC())

	downstream := &authedHandler{}
	srv := httptest.NewServer(mw.Wrap(downstream))
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL, nil)
	req.Header.Set("Authorization", "Bearer "+gen.Plaintext)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("got %d, want 200 (API key with no resolver entry must succeed)", resp.StatusCode)
	}
	if downstream.id.Tenant != "acme" {
		t.Errorf("Identity.Tenant: got %q, want %q (taken from API-key record)", downstream.id.Tenant, "acme")
	}
	if downstream.actor.ID != "actor-service-bootstrap" {
		t.Errorf("Actor.ID: got %q, want synthesized actor-<subject>", downstream.actor.ID)
	}
	if downstream.actor.Type != actor.TypeService {
		t.Errorf("Actor.Type: got %q, want service", downstream.actor.Type)
	}
}

// ─── Wallet enrichment ───────────────────────────────

// errWalletStore is a WalletStore stub that returns the configured
// link / error pairs. Used to exercise the not-linked, linked, and
// transient-error branches of the middleware enrichment hook.
type errWalletStore struct {
	link WalletLink
	err  error
	hits int
}

func (s *errWalletStore) Upsert(_ context.Context, _ string, _ WalletLink) error { return nil }
func (s *errWalletStore) Get(_ context.Context, _ string) (WalletLink, error) {
	s.hits++
	return s.link, s.err
}
func (s *errWalletStore) Delete(_ context.Context, _ string) error { return nil }

func TestMiddleware_WalletEnrichment_Linked(t *testing.T) {
	t.Parallel()

	resolver := NewMapResolver()
	resolver.Set("user_alice", actor.Actor{ID: "u-1", DisplayName: "Alice"}, "tenant-acme")

	link := WalletLink{
		Address:  "0x" + strings.Repeat("a", 40),
		ChainID:  84532, // Base Sepolia
		LinkedAt: time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC),
	}
	wallets := &errWalletStore{link: link}

	provider := NewStubProvider(resolver)
	mw := NewMiddleware(provider, NewMemoryAPIKeyStore(), resolver,
		WithClock(fixedClock(time.Now().UTC())),
		WithWallets(wallets),
	)

	downstream := &authedHandler{}
	srv := httptest.NewServer(mw.Wrap(downstream))
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL, nil)
	req.Header.Set("Authorization", "Bearer user_alice")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d, want 200", resp.StatusCode)
	}
	if wallets.hits != 1 {
		t.Errorf("wallet store was called %d times, want exactly 1", wallets.hits)
	}
	if downstream.id.Wallet == nil {
		t.Fatalf("Identity.Wallet is nil; want enriched")
	}
	if downstream.id.Wallet.Address != link.Address {
		t.Errorf("Identity.Wallet.Address: got %q, want %q", downstream.id.Wallet.Address, link.Address)
	}
	// Resolver attaches the wallet onto Actor.DisplayName via
	// annotateDisplay; assert the format so audit code that reads
	// Actor.DisplayName picks it up.
	if !strings.Contains(downstream.actor.DisplayName, "(0xaaaa…aaaa)") {
		t.Errorf("Actor.DisplayName missing wallet annotation: got %q", downstream.actor.DisplayName)
	}
}

func TestMiddleware_WalletEnrichment_NotLinked(t *testing.T) {
	t.Parallel()

	resolver := NewMapResolver()
	resolver.Set("user_alice", actor.Actor{ID: "u-1", DisplayName: "Alice"}, "tenant-acme")

	wallets := &errWalletStore{err: ErrWalletNotLinked}

	mw := NewMiddleware(NewStubProvider(resolver), NewMemoryAPIKeyStore(), resolver,
		WithWallets(wallets),
	)
	downstream := &authedHandler{}
	srv := httptest.NewServer(mw.Wrap(downstream))
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL, nil)
	req.Header.Set("Authorization", "Bearer user_alice")
	resp, _ := http.DefaultClient.Do(req)
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d, want 200", resp.StatusCode)
	}
	if downstream.id.Wallet != nil {
		t.Errorf("Identity.Wallet should be nil for unlinked subject; got %+v", *downstream.id.Wallet)
	}
	// Display name unchanged when no wallet is present.
	if downstream.actor.DisplayName != "Alice" {
		t.Errorf("Actor.DisplayName: got %q, want %q", downstream.actor.DisplayName, "Alice")
	}
}

func TestMiddleware_WalletEnrichment_TransientErrorSucceeds(t *testing.T) {
	t.Parallel()

	resolver := NewMapResolver()
	resolver.Set("user_alice", actor.Actor{ID: "u-1", DisplayName: "Alice"}, "tenant-acme")

	wallets := &errWalletStore{err: errors.New("connection refused")}

	mw := NewMiddleware(NewStubProvider(resolver), NewMemoryAPIKeyStore(), resolver,
		WithWallets(wallets),
	)
	downstream := &authedHandler{}
	srv := httptest.NewServer(mw.Wrap(downstream))
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL, nil)
	req.Header.Set("Authorization", "Bearer user_alice")
	resp, _ := http.DefaultClient.Do(req)
	resp.Body.Close()

	// The auth check already passed; a transient wallet-store
	// failure must not turn the request into a 401. It's
	// decoration, not authentication.
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d, want 200 (transient wallet error must not fail the request)", resp.StatusCode)
	}
	if downstream.id.Wallet != nil {
		t.Errorf("Identity.Wallet should be nil after transient error; got %+v", *downstream.id.Wallet)
	}
}

func TestMiddleware_WalletEnrichment_OptOut(t *testing.T) {
	t.Parallel()

	// No WithWallets option: middleware should not touch the store
	// at all and Identity.Wallet stays nil.
	resolver := NewMapResolver()
	resolver.Set("user_alice", actor.Actor{ID: "u-1", DisplayName: "Alice"}, "tenant-acme")

	mw := NewMiddleware(NewStubProvider(resolver), NewMemoryAPIKeyStore(), resolver)
	downstream := &authedHandler{}
	srv := httptest.NewServer(mw.Wrap(downstream))
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL, nil)
	req.Header.Set("Authorization", "Bearer user_alice")
	resp, _ := http.DefaultClient.Do(req)
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d, want 200", resp.StatusCode)
	}
	if downstream.id.Wallet != nil {
		t.Errorf("Identity.Wallet should be nil when WithWallets is not configured; got %+v", *downstream.id.Wallet)
	}
}

// BenchmarkMiddleware_WalletEnrichment_HappyPath measures the
// middleware overhead with a successful wallet lookup against the
// in-memory store. The acceptance criterion is that the
// added cost stays under the same ~5 ms target as the rest of the
// middleware. Memory-store hits are µs; the benchmark exists so
// future regressions (or a Postgres-backed store with no cache)
// are visible.
func BenchmarkMiddleware_WalletEnrichment_HappyPath(b *testing.B) {
	resolver := NewMapResolver()
	resolver.Set("user_a", actor.Actor{ID: "u-a", DisplayName: "A"}, "tenant-a")

	wallets := NewMemoryWalletStore()
	_ = wallets.Upsert(context.Background(), "user_a", WalletLink{
		Address:  "0x" + strings.Repeat("a", 40),
		ChainID:  84532,
		LinkedAt: time.Now().UTC(),
	})

	provider := NewStubProvider(resolver)
	mw := NewMiddleware(provider, NewMemoryAPIKeyStore(), resolver, WithWallets(wallets))
	handler := mw.Wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer user_a")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			b.Fatalf("unexpected status %d", rr.Code)
		}
	}
}

// ─── Latency benchmark ───────────────────────────────────────────

// BenchmarkMiddleware_HappyPath asserts the middleware adds well
// under 5 ms per request on the stub path. The acceptance criterion
// in issue #3 is "at most ~5 ms latency on the happy path" — this
// records the measurement so future regressions are visible.
func BenchmarkMiddleware_HappyPath(b *testing.B) {
	resolver := NewMapResolver()
	resolver.Set("user_a", actor.Actor{ID: "u-a"}, "tenant-a")
	provider := NewStubProvider(resolver)
	mw := NewMiddleware(provider, NewMemoryAPIKeyStore(), resolver)
	handler := mw.Wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer user_a")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			b.Fatalf("unexpected status %d", rr.Code)
		}
	}
}

// ─── Context helpers ─────────────────────────────────────────────

func TestIdentityAndActorContext(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	if _, err := IdentityFrom(ctx); !errors.Is(err, ErrNoIdentity) {
		t.Errorf("expected ErrNoIdentity, got %v", err)
	}
	if _, err := ActorFrom(ctx); !errors.Is(err, ErrNoActor) {
		t.Errorf("expected ErrNoActor, got %v", err)
	}

	id := Identity{Subject: "u", Method: MethodSession, Tenant: "t"}
	a := actor.Actor{ID: "u", DisplayName: "U"}

	ctx = WithIdentity(ctx, id)
	ctx = WithActor(ctx, a)

	gotID, err := IdentityFrom(ctx)
	if err != nil || gotID.Subject != "u" {
		t.Errorf("IdentityFrom: %+v err=%v", gotID, err)
	}
	gotA, err := ActorFrom(ctx)
	if err != nil || gotA.ID != "u" {
		t.Errorf("ActorFrom: %+v err=%v", gotA, err)
	}

	// Must variants do not panic when present.
	_ = MustIdentity(ctx)
	_ = MustActor(ctx)
}

// hash helper used to silence the unused-import warnings if the
// rest of the file changes; Go's import block carries crypto/sha256
// already through APIKey types but we want the test file to be
// self-contained.
var _ = sha256.New

// A key bound to an agent carries the binding onto the identity, where
// the decide path matches it against the proposal's actor_id. It comes
// from the stored record only; an unbound key yields no binding.
func TestMiddleware_APIKey_CarriesAgentBinding(t *testing.T) {
	t.Parallel()

	for _, agentID := range []string{"refund-agent", ""} {
		gen, err := NewAPIKey("acme", time.Time{})
		if err != nil {
			t.Fatalf("NewAPIKey: %v", err)
		}
		store := NewMemoryAPIKeyStore()
		if err := store.Insert(context.Background(), APIKeyRecord{
			ID: "key-" + agentID, Tenant: "acme", Subject: "owner", Roles: []string{"support"},
			AgentID: agentID, KeyHash: gen.Hash, CreatedAt: time.Now().UTC(),
		}); err != nil {
			t.Fatalf("Insert: %v", err)
		}
		mw := newMiddlewareForTest(t, NewStubProvider(NewMapResolver()), store, NewMapResolver(),
			time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC))
		downstream := &authedHandler{}
		srv := httptest.NewServer(mw.Wrap(downstream))

		req, _ := http.NewRequest("GET", srv.URL, nil)
		req.Header.Set("Authorization", "Bearer "+gen.Plaintext)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("request: %v", err)
		}
		resp.Body.Close()
		srv.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status: got %d, want 200", resp.StatusCode)
		}
		if downstream.id.AgentID != agentID {
			t.Errorf("identity.AgentID: got %q, want %q", downstream.id.AgentID, agentID)
		}
	}
}

// A key bound to an environment carries it onto the identity, from
// the stored record only; an unbound key yields none.
func TestMiddleware_APIKey_CarriesEnvironmentBinding(t *testing.T) {
	t.Parallel()

	for _, env := range []string{"production", ""} {
		gen, err := NewAPIKey("acme", time.Time{})
		if err != nil {
			t.Fatalf("NewAPIKey: %v", err)
		}
		store := NewMemoryAPIKeyStore()
		if err := store.Insert(context.Background(), APIKeyRecord{
			ID: "key-" + env, Tenant: "acme", Subject: "owner", Roles: []string{"support"},
			Environment: env, KeyHash: gen.Hash, CreatedAt: time.Now().UTC(),
		}); err != nil {
			t.Fatalf("Insert: %v", err)
		}
		mw := newMiddlewareForTest(t, NewStubProvider(NewMapResolver()), store, NewMapResolver(),
			time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC))
		downstream := &authedHandler{}
		srv := httptest.NewServer(mw.Wrap(downstream))

		req, _ := http.NewRequest("GET", srv.URL, nil)
		req.Header.Set("Authorization", "Bearer "+gen.Plaintext)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("request: %v", err)
		}
		resp.Body.Close()
		srv.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status: got %d, want 200", resp.StatusCode)
		}
		if downstream.id.Environment != env {
			t.Errorf("identity.Environment: got %q, want %q", downstream.id.Environment, env)
		}
	}
}
