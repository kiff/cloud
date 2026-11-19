package proofs

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeGitHub publishes one RSA key as a JWKS and signs tokens with it.
type fakeGitHub struct {
	key    *rsa.PrivateKey
	kid    string
	issuer string
	srv    *httptest.Server
}

func newFakeGitHub(t *testing.T) *fakeGitHub {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeGitHub{key: key, kid: "k1"}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e := big.NewInt(int64(key.E)).Bytes()
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
			"kty": "RSA", "kid": f.kid, "alg": "RS256",
			"n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString(e),
		}}})
	}))
	t.Cleanup(f.srv.Close)
	f.issuer = f.srv.URL
	return f
}

func (f *fakeGitHub) sign(t *testing.T, header, claims map[string]any, key *rsa.PrivateKey) string {
	t.Helper()
	h, _ := json.Marshal(header)
	c, _ := json.Marshal(claims)
	input := base64.RawURLEncoding.EncodeToString(h) + "." + base64.RawURLEncoding.EncodeToString(c)
	digest := sha256.Sum256([]byte(input))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return input + "." + base64.RawURLEncoding.EncodeToString(sig)
}

var testOIDC = OIDCConfig{
	Audience:     "kiff-proofs",
	Repository:   "kiff/agents",
	RepositoryID: "700100",
	OwnerID:      "2176846",
	Ref:          "refs/heads/main",
	Workflow:     "kiff/agents/.github/workflows/proofs.yml@refs/heads/main",
}

func (f *fakeGitHub) claims(now time.Time) map[string]any {
	return map[string]any{
		"iss": f.issuer, "aud": testOIDC.Audience, "sub": "repo:kiff/agents:ref:refs/heads/main",
		"repository": testOIDC.Repository, "repository_id": testOIDC.RepositoryID, "repository_owner_id": testOIDC.OwnerID,
		"ref": testOIDC.Ref, "workflow_ref": testOIDC.Workflow, "sha": strings.Repeat("a", 40),
		"run_id": "123", "run_attempt": "2", "exp": now.Add(5 * time.Minute).Unix(), "nbf": now.Add(-time.Minute).Unix(), "iat": now.Unix(),
	}
}

func TestTheRunnersTokenIsChecked(t *testing.T) {
	f := newFakeGitHub(t)
	now := time.Now()
	v := NewOIDCVerifier(testOIDC).WithJWKS(f.issuer, f.srv.URL).WithClock(func() time.Time { return now })
	header := map[string]any{"alg": "RS256", "kid": f.kid}
	ctx := context.Background()

	if _, err := v.Verify(ctx, f.sign(t, header, f.claims(now), f.key)); err != nil {
		t.Fatalf("a good token was refused: %v", err)
	}

	other, _ := rsa.GenerateKey(rand.Reader, 2048)
	cases := map[string]struct {
		claims map[string]any
		header map[string]any
		key    *rsa.PrivateKey
	}{
		"another repository": {claims: with(f.claims(now), "repository", "kiff/tafs")},
		"another ref":        {claims: with(f.claims(now), "ref", "refs/heads/feature")},
		"a pull request ref": {claims: with(f.claims(now), "ref", "refs/pull/9/merge")},
		"another workflow": {claims: with(f.claims(now), "workflow_ref",
			"kiff/agents/.github/workflows/other.yml@refs/heads/main")},
		"the workflow on another branch": {claims: with(f.claims(now), "workflow_ref",
			"kiff/agents/.github/workflows/proofs.yml@refs/heads/feature")},
		"another audience":                 {claims: with(f.claims(now), "aud", "sts.example.com")},
		"another issuer":                   {claims: with(f.claims(now), "iss", "https://token.actions.githubusercontent.com.evil")},
		"expired":                          {claims: with(f.claims(now), "exp", now.Add(-2*time.Minute).Unix())},
		"no expiry":                        {claims: without(f.claims(now), "exp")},
		"not yet valid":                    {claims: with(f.claims(now), "nbf", now.Add(5*time.Minute).Unix())},
		"another key":                      {key: other},
		"alg none":                         {header: map[string]any{"alg": "none", "kid": f.kid}},
		"HS256":                            {header: map[string]any{"alg": "HS256", "kid": f.kid}},
		"an unknown key id":                {header: map[string]any{"alg": "RS256", "kid": "k9"}},
		"no repository":                    {claims: without(f.claims(now), "repository")},
		"another repository id, same name": {claims: with(f.claims(now), "repository_id", "999999")},
		"another owner id, same name":      {claims: with(f.claims(now), "repository_owner_id", "1")},
		"no repository id":                 {claims: without(f.claims(now), "repository_id")},
		"no owner id":                      {claims: without(f.claims(now), "repository_owner_id")},
		"the repository id as a number":    {claims: with(f.claims(now), "repository_id", 700100)},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			claims, hdr, key := c.claims, c.header, c.key
			if claims == nil {
				claims = f.claims(now)
			}
			if hdr == nil {
				hdr = header
			}
			if key == nil {
				key = f.key
			}
			if _, err := v.Verify(ctx, f.sign(t, hdr, claims, key)); !errors.Is(err, ErrUnauthorized) {
				t.Fatalf("Verify = %v, want ErrUnauthorized", err)
			}
		})
	}
	if _, err := v.Verify(ctx, "not.a.jwt.at.all"); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("garbage accepted")
	}
}

func TestAnIncompleteConfigAcceptsNobody(t *testing.T) {
	f := newFakeGitHub(t)
	now := time.Now()
	cfg := testOIDC
	cfg.Workflow = ""
	v := NewOIDCVerifier(cfg).WithJWKS(f.issuer, f.srv.URL).WithClock(func() time.Time { return now })
	tok := f.sign(t, map[string]any{"alg": "RS256", "kid": f.kid}, with(f.claims(now), "workflow_ref", ""), f.key)
	if _, err := v.Verify(context.Background(), tok); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("an empty workflow config accepted a token with an empty workflow_ref: %v", err)
	}
}

func TestTheRepositoryIDsAreRequiredInConfig(t *testing.T) {
	for _, drop := range []func(*OIDCConfig){func(c *OIDCConfig) { c.RepositoryID = "" }, func(c *OIDCConfig) { c.OwnerID = "" }} {
		cfg := testOIDC
		drop(&cfg)
		if cfg.Complete() {
			t.Fatalf("complete without an id: %+v", cfg)
		}
	}
}

func TestTheTokensShaAndRunAreRead(t *testing.T) {
	f := newFakeGitHub(t)
	now := time.Now()
	v := NewOIDCVerifier(testOIDC).WithJWKS(f.issuer, f.srv.URL).WithClock(func() time.Time { return now })
	c, err := v.Verify(context.Background(), f.sign(t, map[string]any{"alg": "RS256", "kid": f.kid}, f.claims(now), f.key))
	if err != nil || c.Sha != strings.Repeat("a", 40) || c.RunID != "123" || c.RunAttempt != "2" {
		t.Fatalf("claims %+v, %v", c, err)
	}
}

// A slow GitHub never holds up a token whose key is cached: the fetch for
// an unknown key runs outside the lock.
func TestASlowKeyFetchDoesntBlockACachedKey(t *testing.T) {
	f := newFakeGitHub(t)
	gate := make(chan struct{})
	var slow atomic.Bool
	inner := f.srv.Config.Handler
	f.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if slow.Load() {
			<-gate
		}
		inner.ServeHTTP(w, r)
	})
	var mu sync.Mutex
	now := time.Now()
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	v := NewOIDCVerifier(testOIDC).WithJWKS(f.issuer, f.srv.URL).WithClock(clock)
	good := func() string {
		return f.sign(t, map[string]any{"alg": "RS256", "kid": f.kid}, f.claims(clock()), f.key)
	}
	ctx := context.Background()
	if _, err := v.Verify(ctx, good()); err != nil { // caches the keys
		t.Fatal(err)
	}
	mu.Lock()
	now = now.Add(2 * time.Minute) // a refetch is allowed again
	mu.Unlock()
	slow.Store(true)
	unknown := f.sign(t, map[string]any{"alg": "RS256", "kid": "k-new"}, f.claims(clock()), f.key)
	fetching := make(chan error, 1)
	go func() { _, err := v.Verify(ctx, unknown); fetching <- err }()
	time.Sleep(50 * time.Millisecond) // the slow fetch is under way

	done := make(chan error, 1)
	go func() { _, err := v.Verify(ctx, good()); done <- err }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("cached key during a slow fetch: %v", err)
		}
	case <-time.After(2 * time.Second):
		close(gate)
		t.Fatal("a cached key waited for a slow fetch")
	}
	close(gate)
	if err := <-fetching; !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("the unknown key: %v", err)
	}
}

func TestTheAudienceMayBeAList(t *testing.T) {
	f := newFakeGitHub(t)
	now := time.Now()
	v := NewOIDCVerifier(testOIDC).WithJWKS(f.issuer, f.srv.URL).WithClock(func() time.Time { return now })
	tok := f.sign(t, map[string]any{"alg": "RS256", "kid": f.kid}, with(f.claims(now), "aud", []string{"x", testOIDC.Audience}), f.key)
	if _, err := v.Verify(context.Background(), tok); err != nil {
		t.Fatalf("aud list refused: %v", err)
	}
}

func with(m map[string]any, k string, v any) map[string]any {
	m[k] = v
	return m
}

func without(m map[string]any, k string) map[string]any {
	delete(m, k)
	return m
}
