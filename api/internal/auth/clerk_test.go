package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/clerk/clerk-sdk-go/v2"
)

// fakeVerifier returns a fixed claims/error pair for any token.
// Tests construct ClerkSessions with NewClerkSessionsWithVerifier
// and a fakeVerifier so the test never touches Clerk's network.
type fakeVerifier struct {
	claims *clerk.SessionClaims
	err    error
}

func (f fakeVerifier) Verify(_ context.Context, _ string) (*clerk.SessionClaims, error) {
	return f.claims, f.err
}

func newClaims(subject string) *clerk.SessionClaims {
	c := &clerk.SessionClaims{}
	c.RegisteredClaims = clerk.RegisteredClaims{Subject: subject}
	return c
}

func TestClerkSessions_VerifyToken_HappyPath(t *testing.T) {
	t.Parallel()

	claims := newClaims("user_abc")
	claims.Claims.SessionID = "sess_xyz"
	claims.Claims.ActiveOrganizationID = "org_acme"
	claims.Claims.ActiveOrganizationSlug = "acme"
	claims.Claims.ActiveOrganizationRole = "admin"

	cs := NewClerkSessionsWithVerifier(fakeVerifier{claims: claims})

	id, err := cs.VerifyToken(context.Background(), "Bearer some.jwt.value")
	if err != nil {
		t.Fatalf("VerifyToken: %v", err)
	}
	if id.Subject != "user_abc" {
		t.Errorf("Subject: got %q, want user_abc", id.Subject)
	}
	if id.Method != MethodSession {
		t.Errorf("Method: got %q, want session", id.Method)
	}
	if id.Tenant != "" {
		t.Errorf("Tenant should be empty until the resolver fills it in; got %q", id.Tenant)
	}
	if id.Email != "" {
		t.Errorf("Email should stay empty until a backend lookup or custom JWT template adds it; got %q", id.Email)
	}
	want := map[string]string{
		"clerk_session_id": "sess_xyz",
		"clerk_org_id":     "org_acme",
		"clerk_org_slug":   "acme",
	}
	for k, v := range want {
		if got := id.Metadata[k]; got != v {
			t.Errorf("Metadata[%q]: got %q, want %q", k, got, v)
		}
	}
	if !id.HasRole("admin") {
		t.Errorf("expected admin role from ActiveOrganizationRole, got %v", id.Roles)
	}
}

func TestClerkSessions_VerifyToken_TrimsBearer(t *testing.T) {
	t.Parallel()
	cs := NewClerkSessionsWithVerifier(fakeVerifier{claims: newClaims("user_abc")})

	for _, raw := range []string{
		"value-only",
		"Bearer value-only",
		"  bearer value-only  ",
		"BEARER value-only",
	} {
		id, err := cs.VerifyToken(context.Background(), raw)
		if err != nil {
			t.Errorf("token %q: unexpected error %v", raw, err)
		}
		if id.Subject != "user_abc" {
			t.Errorf("token %q: subject mismatch %q", raw, id.Subject)
		}
	}
}

func TestClerkSessions_VerifyToken_RejectsEmpty(t *testing.T) {
	t.Parallel()
	cs := NewClerkSessionsWithVerifier(fakeVerifier{})

	for _, raw := range []string{"", "   ", "Bearer  "} {
		_, err := cs.VerifyToken(context.Background(), raw)
		if !errors.Is(err, ErrInvalidToken) {
			t.Errorf("token %q: expected ErrInvalidToken, got %v", raw, err)
		}
	}
}

func TestClerkSessions_VerifyToken_PropagatesVerifierError(t *testing.T) {
	t.Parallel()
	wantInner := errors.New("verifier said no")
	cs := NewClerkSessionsWithVerifier(fakeVerifier{err: wantInner})

	_, err := cs.VerifyToken(context.Background(), "good-shape")
	if !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("outer error should wrap ErrInvalidToken, got %v", err)
	}
	if !errors.Is(err, wantInner) {
		t.Fatalf("outer error should wrap the verifier's error, got %v", err)
	}
}

func TestClerkSessions_VerifyToken_RejectsClaimsMissingSubject(t *testing.T) {
	t.Parallel()
	cs := NewClerkSessionsWithVerifier(fakeVerifier{claims: &clerk.SessionClaims{}})

	_, err := cs.VerifyToken(context.Background(), "good-shape")
	if !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("expected ErrInvalidToken, got %v", err)
	}
}

func TestClerkSessions_VerifyToken_RejectsNilClaims(t *testing.T) {
	t.Parallel()
	cs := NewClerkSessionsWithVerifier(fakeVerifier{}) // claims is nil and err is nil

	_, err := cs.VerifyToken(context.Background(), "good-shape")
	if !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("expected ErrInvalidToken, got %v", err)
	}
}

func TestClerkSessions_OmitsMetadataWhenNoOrgClaims(t *testing.T) {
	t.Parallel()
	cs := NewClerkSessionsWithVerifier(fakeVerifier{claims: newClaims("user_abc")})

	id, err := cs.VerifyToken(context.Background(), "good-shape")
	if err != nil {
		t.Fatalf("VerifyToken: %v", err)
	}
	if id.Metadata != nil {
		t.Errorf("Metadata should be nil when no org claims are present, got %v", id.Metadata)
	}
	// Solo users (no org role) get a default "admin" role so they
	// can run Playground demos without permission-denied errors.
	if len(id.Roles) != 1 || id.Roles[0] != "admin" {
		t.Errorf("Roles should be [\"admin\"] for solo users, got %v", id.Roles)
	}
}

func TestClerkSessions_NewWithVerifier_NilFallsBackToReal(t *testing.T) {
	t.Parallel()
	cs := NewClerkSessionsWithVerifier(nil)
	if cs.verifier == nil {
		t.Fatalf("expected the constructor to substitute a real verifier when nil is passed")
	}
	// The fall-back is the production verifier; we don't call .Verify
	// on it because that would dial Clerk's JWKS endpoint.
	if _, ok := cs.verifier.(*clerkVerifier); !ok {
		t.Errorf("expected fall-back verifier to be the real clerkVerifier, got %T", cs.verifier)
	}
}

func TestClerkVerifier_CachesSigningKeysByKid(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	fetches := map[string]int{}
	v := newClerkVerifier()
	v.now = func() time.Time { return now }
	v.fetch = func(_ context.Context, kid string) (*clerk.JSONWebKey, error) {
		fetches[kid]++
		return &clerk.JSONWebKey{KeyID: kid}, nil
	}
	ctx := context.Background()

	for range 3 {
		if _, err := v.key(ctx, "kid-a"); err != nil {
			t.Fatal(err)
		}
	}
	if fetches["kid-a"] != 1 {
		t.Fatalf("repeat lookups within the TTL fetched %d times, want 1", fetches["kid-a"])
	}

	if _, err := v.key(ctx, "kid-b"); err != nil {
		t.Fatal(err)
	}
	if fetches["kid-b"] != 1 {
		t.Fatalf("a rotated-in kid must be fetched on first sight, got %d fetches", fetches["kid-b"])
	}

	now = now.Add(jwksTTL)
	if _, err := v.key(ctx, "kid-a"); err != nil {
		t.Fatal(err)
	}
	if fetches["kid-a"] != 2 {
		t.Fatalf("a key at the TTL must be refetched, got %d fetches", fetches["kid-a"])
	}
}

func TestClerkVerifier_FetchErrorIsNotCached(t *testing.T) {
	t.Parallel()
	calls := 0
	v := newClerkVerifier()
	v.fetch = func(context.Context, string) (*clerk.JSONWebKey, error) {
		calls++
		if calls == 1 {
			return nil, errors.New("clerk unavailable")
		}
		return &clerk.JSONWebKey{KeyID: "kid-a"}, nil
	}
	if _, err := v.key(context.Background(), "kid-a"); err == nil {
		t.Fatal("want the fetch error surfaced")
	}
	if _, err := v.key(context.Background(), "kid-a"); err != nil {
		t.Fatalf("a failed fetch must not poison the cache: %v", err)
	}
}
