package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/clerk/clerk-sdk-go/v2"
	"github.com/clerk/clerk-sdk-go/v2/jwt"
)

// Verifier is the surface ClerkSessions depends on. The production
// adapter wraps jwt.Verify from clerk-sdk-go/v2; tests pass a
// fakeVerifier and never touch the real SDK.
//
// Keeping the interface here (rather than reaching for a generic
// JWT verifier) means the cloud's auth package owns the contract,
// and a future provider swap (different JWT, different IdP) just
// needs another Verifier implementation.
type Verifier interface {
	Verify(ctx context.Context, token string) (*clerk.SessionClaims, error)
}

// jwksTTL bounds how long a fetched signing key is trusted before it is
// fetched again. A rotated-in key is picked up immediately regardless
// (an unknown kid is a cache miss); the TTL only limits how long a key
// Clerk has since withdrawn keeps verifying.
const jwksTTL = time.Hour

// clerkVerifier adapts jwt.Verify into the Verifier interface and caches
// Clerk's signing keys by kid. Without the cache, jwt.Verify fetches the
// whole JWKS from Clerk's Backend API on every call (clerk-sdk-go v2
// only caches inside its HTTP middleware, which this API doesn't use),
// adding an internet round trip to every authenticated request.
type clerkVerifier struct {
	mu    sync.Mutex
	keys  map[string]cachedJWK
	fetch func(ctx context.Context, kid string) (*clerk.JSONWebKey, error)
	now   func() time.Time
}

type cachedJWK struct {
	jwk       *clerk.JSONWebKey
	fetchedAt time.Time
}

func newClerkVerifier() *clerkVerifier {
	return &clerkVerifier{
		keys: map[string]cachedJWK{},
		fetch: func(ctx context.Context, kid string) (*clerk.JSONWebKey, error) {
			return jwt.GetJSONWebKey(ctx, &jwt.GetJSONWebKeyParams{KeyID: kid})
		},
		now: time.Now,
	}
}

// Verify validates the token against the cached signing key for its kid,
// fetching the JWKS only on a miss or once the cached key is older than
// jwksTTL.
func (v *clerkVerifier) Verify(ctx context.Context, token string) (*clerk.SessionClaims, error) {
	unverified, err := jwt.Decode(ctx, &jwt.DecodeParams{Token: token})
	if err != nil {
		return nil, err
	}
	jwk, err := v.key(ctx, unverified.KeyID)
	if err != nil {
		return nil, err
	}
	return jwt.Verify(ctx, &jwt.VerifyParams{Token: token, JWK: jwk})
}

func (v *clerkVerifier) key(ctx context.Context, kid string) (*clerk.JSONWebKey, error) {
	v.mu.Lock()
	c, ok := v.keys[kid]
	v.mu.Unlock()
	if ok && v.now().Sub(c.fetchedAt) < jwksTTL {
		return c.jwk, nil
	}
	jwk, err := v.fetch(ctx, kid)
	if err != nil {
		return nil, err
	}
	v.mu.Lock()
	v.keys[kid] = cachedJWK{jwk: jwk, fetchedAt: v.now()}
	v.mu.Unlock()
	return jwk, nil
}

// ClerkSessions verifies user-session JWTs issued by Clerk and
// produces an Identity for downstream code. It does not look up
// tenant or actor records; that mapping happens in the
// ActorResolver passed to the HTTP middleware (slice 3).
//
// The Identity returned by VerifyToken carries:
//
//   - Subject: the JWT's standard `sub` claim (Clerk user id).
//   - Method: MethodSession.
//   - Metadata: Clerk's organization id and role when present
//     (org-aware tokens). Empty otherwise.
//
// Email is intentionally left empty. Clerk does not put email on
// the JWT by default; surfacing it requires either a Backend API
// round-trip or a custom JWT template. Both are deferred — the
// auth package's Identity already supports Email="" and downstream
// code does not depend on it for v0.1.
type ClerkSessions struct {
	verifier Verifier
}

// NewClerkSessions returns a ClerkSessions that uses the default
// Clerk SDK verifier. The SDK uses a package-level secret key set
// via clerk.SetKey; the API binary calls SetKey at startup with
// the value of CLERK_SECRET_KEY. Callers
// that construct ClerkSessions outside the binary need to do the
// same before VerifyToken hits the JWKS endpoint.
//
// Tests construct ClerkSessions directly with a fake Verifier
// instead of calling this; see clerk_test.go.
func NewClerkSessions() *ClerkSessions {
	return &ClerkSessions{verifier: newClerkVerifier()}
}

// NewClerkSessionsWithVerifier is the test-friendly constructor.
// Production code should call NewClerkSessions; this exists so
// tests can inject a fake verifier without exposing the unexported
// field.
func NewClerkSessionsWithVerifier(v Verifier) *ClerkSessions {
	if v == nil {
		v = newClerkVerifier()
	}
	return &ClerkSessions{verifier: v}
}

// ErrInvalidToken is returned when the verifier rejects a token.
// HTTP middleware translates this to 401. Distinct from
// ErrUnknownIdentity (post-verification, identity not registered).
var ErrInvalidToken = errors.New("auth: invalid session token")

// VerifyToken verifies the JWT and produces an Identity. The
// Identity's Tenant is intentionally empty; tenant resolution is
// the next step in the request pipeline.
//
// Whitespace and a leading "Bearer " prefix are trimmed so callers
// can pass the raw value of the Authorization header without
// pre-processing.
func (c *ClerkSessions) VerifyToken(ctx context.Context, token string) (Identity, error) {
	token = trimBearer(strings.TrimSpace(token))
	if token == "" {
		return Identity{}, fmt.Errorf("%w: empty token", ErrInvalidToken)
	}
	claims, err := c.verifier.Verify(ctx, token)
	if err != nil {
		return Identity{}, fmt.Errorf("%w: %w", ErrInvalidToken, err)
	}
	if claims == nil || claims.Subject == "" {
		return Identity{}, fmt.Errorf("%w: claims missing subject", ErrInvalidToken)
	}

	id := Identity{
		Subject: claims.Subject,
		Method:  MethodSession,
	}
	if metadata := orgMetadata(claims); len(metadata) > 0 {
		id.Metadata = metadata
	}
	if role := strings.TrimSpace(claims.ActiveOrganizationRole); role != "" {
		id.Roles = append(id.Roles, role)
	}
	// Solo users (no organization) get a default "admin" role so
	// they can run Playground demos and execute actions without
	// hitting permission-denied errors. Matches the stub mode
	// behavior where every dev-user gets ["admin"]. Organization
	// users inherit their org role instead.
	if len(id.Roles) == 0 {
		id.Roles = []string{"admin"}
	}
	id.FactorAge = factorAge(token, claims.FactorVerificationAge)
	return id, nil
}

// factorAge is how long ago the session's sign-in factor was verified,
// from Clerk's fva claim ([first factor, second factor] in minutes, -1
// for never). The SDK decodes an absent claim as [0, 0], which would
// read as "just verified", so the claim's presence is checked on the
// (already verified) payload; absent or never-verified answers nil and
// step-up falls back to the session's creation time.
func factorAge(token string, fva [2]int64) *time.Duration {
	if !tokenHasClaim(token, "fva") {
		return nil
	}
	best := int64(-1)
	for _, v := range fva {
		if v >= 0 && (best < 0 || v < best) {
			best = v
		}
	}
	if best < 0 {
		return nil
	}
	d := time.Duration(best) * time.Minute
	return &d
}

func tokenHasClaim(token, claim string) bool {
	parts := strings.Split(trimBearer(strings.TrimSpace(token)), ".")
	if len(parts) != 3 {
		return false
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return false
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(raw, &payload); err != nil {
		return false
	}
	_, ok := payload[claim]
	return ok
}

// orgMetadata extracts Clerk's org-aware claims into the cloud's
// Identity.Metadata map. Returns nil when no org claims are
// present so the Identity stays compact.
func orgMetadata(claims *clerk.SessionClaims) map[string]string {
	if claims == nil {
		return nil
	}
	out := map[string]string{}
	if claims.SessionID != "" {
		out["clerk_session_id"] = claims.SessionID
	}
	if claims.ActiveOrganizationID != "" {
		out["clerk_org_id"] = claims.ActiveOrganizationID
	}
	if claims.ActiveOrganizationSlug != "" {
		out["clerk_org_slug"] = claims.ActiveOrganizationSlug
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// trimBearer strips a leading "Bearer " (case-insensitive) prefix
// from the token value. Most HTTP clients send tokens that way; we
// accept both shapes so callers do not have to pre-strip.
func trimBearer(token string) string {
	const prefix = "bearer "
	if len(token) >= len(prefix) && strings.EqualFold(token[:len(prefix)], prefix) {
		return strings.TrimSpace(token[len(prefix):])
	}
	return token
}
