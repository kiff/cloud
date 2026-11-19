package proofs

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"
)

// GitHub Actions' OIDC issuer and its keys.
const (
	GitHubIssuer  = "https://token.actions.githubusercontent.com"
	GitHubJWKSURL = "https://token.actions.githubusercontent.com/.well-known/jwks"
)

// ErrUnauthorized wraps every refusal of a runner's token.
var ErrUnauthorized = errors.New("proofs: runner not authorized")

// OIDCConfig is who may send runs: one workflow file, on one repository's
// one branch, for one audience. Every field is required.
type OIDCConfig struct {
	// Audience is the aud the workflow asks GitHub for (KIFF_PROOF_OIDC_AUDIENCE).
	Audience string
	// Repository is owner/name, e.g. kiff/agents (KIFF_PROOF_OIDC_REPOSITORY).
	Repository string
	// RepositoryID and OwnerID are GitHub's numeric ids of the repository
	// and its owner (KIFF_PROOF_OIDC_REPOSITORY_ID, _OWNER_ID): a deleted,
	// renamed or transferred repository's name can be taken by someone
	// else, its ids can't.
	RepositoryID string
	OwnerID      string
	// Ref is the branch ref, e.g. refs/heads/main (KIFF_PROOF_OIDC_REF).
	Ref string
	// Workflow is the token's workflow_ref, e.g.
	// kiff/agents/.github/workflows/proofs.yml@refs/heads/main (KIFF_PROOF_OIDC_WORKFLOW).
	Workflow string
}

// Complete reports whether every field is set; an incomplete config
// accepts nobody.
func (c OIDCConfig) Complete() bool {
	return c.Audience != "" && c.Repository != "" && c.RepositoryID != "" && c.OwnerID != "" && c.Ref != "" && c.Workflow != ""
}

// OIDCVerifier checks a GitHub Actions OIDC token: RS256 signed by one of
// GitHub's published keys, from GitHub's issuer, for the configured
// audience, repository, ref and workflow, and not expired. Nothing else is
// accepted, and there is no other credential.
type OIDCVerifier struct {
	cfg     OIDCConfig
	issuer  string
	jwksURL string
	http    *http.Client
	now     func() time.Time

	// mu guards the cached keys; it's never held during a fetch, so a slow
	// GitHub never holds up a token whose key is cached. One fetch runs at
	// a time (inflight); others wanting fresh keys wait for it.
	mu       sync.Mutex
	keys     map[string]*rsa.PublicKey
	fresh    time.Time
	fetched  time.Time
	inflight chan struct{}
}

// NewOIDCVerifier accepts tokens matching cfg.
func NewOIDCVerifier(cfg OIDCConfig) *OIDCVerifier {
	return &OIDCVerifier{cfg: cfg, issuer: GitHubIssuer, jwksURL: GitHubJWKSURL,
		http: &http.Client{Timeout: 10 * time.Second}, now: time.Now}
}

// WithJWKS reads the keys from url and expects iss to be issuer: a fake's, for tests.
func (v *OIDCVerifier) WithJWKS(issuer, url string) *OIDCVerifier {
	v.issuer, v.jwksURL = issuer, url
	return v
}

// WithClock sets the clock, for tests.
func (v *OIDCVerifier) WithClock(now func() time.Time) *OIDCVerifier { v.now = now; return v }

// Claims are the token's claims this check reads.
type Claims struct {
	Iss        string          `json:"iss"`
	Aud        json.RawMessage `json:"aud"`
	Sub        string          `json:"sub"`
	Repository string          `json:"repository"`
	// RepositoryID and OwnerID are GitHub's numeric ids, sent as strings.
	RepositoryID string `json:"repository_id"`
	OwnerID      string `json:"repository_owner_id"`
	// Sha is the commit the workflow ran: the run's runner_version.
	Sha string `json:"sha"`
	// RunAttempt is which attempt of the GitHub run this is.
	RunAttempt  string `json:"run_attempt"`
	Ref         string `json:"ref"`
	WorkflowRef string `json:"workflow_ref"`
	RunID       string `json:"run_id"`
	Exp         int64  `json:"exp"`
	Nbf         int64  `json:"nbf"`
	Iat         int64  `json:"iat"`
}

// VerifyRequest checks the request's Authorization: Bearer token.
func (v *OIDCVerifier) VerifyRequest(ctx context.Context, r *http.Request) (Claims, error) {
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || strings.TrimSpace(token) == "" {
		return Claims{}, fmt.Errorf("%w: no bearer token", ErrUnauthorized)
	}
	return v.Verify(ctx, strings.TrimSpace(token))
}

// Verify checks one token.
func (v *OIDCVerifier) Verify(ctx context.Context, token string) (Claims, error) {
	if v == nil || !v.cfg.Complete() {
		return Claims{}, fmt.Errorf("%w: not configured", ErrUnauthorized)
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return Claims{}, fmt.Errorf("%w: not a JWT", ErrUnauthorized)
	}
	var h struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	if err := decodeSegment(parts[0], &h); err != nil {
		return Claims{}, fmt.Errorf("%w: header", ErrUnauthorized)
	}
	if h.Alg != "RS256" || h.Kid == "" {
		return Claims{}, fmt.Errorf("%w: alg %q", ErrUnauthorized, h.Alg)
	}
	key, err := v.key(ctx, h.Kid)
	if err != nil {
		return Claims{}, err
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return Claims{}, fmt.Errorf("%w: signature", ErrUnauthorized)
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(key, crypto.SHA256, digest[:], sig); err != nil {
		return Claims{}, fmt.Errorf("%w: signature", ErrUnauthorized)
	}
	var c Claims
	if err := decodeSegment(parts[1], &c); err != nil {
		return Claims{}, fmt.Errorf("%w: claims", ErrUnauthorized)
	}
	now := v.now()
	switch {
	case c.Iss != v.issuer:
		return Claims{}, fmt.Errorf("%w: issuer", ErrUnauthorized)
	case !audienceIs(c.Aud, v.cfg.Audience):
		return Claims{}, fmt.Errorf("%w: audience", ErrUnauthorized)
	case c.Repository != v.cfg.Repository:
		return Claims{}, fmt.Errorf("%w: repository", ErrUnauthorized)
	case c.RepositoryID != v.cfg.RepositoryID:
		return Claims{}, fmt.Errorf("%w: repository_id", ErrUnauthorized)
	case c.OwnerID != v.cfg.OwnerID:
		return Claims{}, fmt.Errorf("%w: repository_owner_id", ErrUnauthorized)
	case c.Ref != v.cfg.Ref:
		return Claims{}, fmt.Errorf("%w: ref", ErrUnauthorized)
	case c.WorkflowRef != v.cfg.Workflow:
		return Claims{}, fmt.Errorf("%w: workflow", ErrUnauthorized)
	case c.Exp == 0 || now.After(time.Unix(c.Exp, 0).Add(oidcSkew)):
		return Claims{}, fmt.Errorf("%w: expired", ErrUnauthorized)
	case c.Nbf != 0 && time.Unix(c.Nbf, 0).After(now.Add(oidcSkew)):
		return Claims{}, fmt.Errorf("%w: not yet valid", ErrUnauthorized)
	case c.Iat != 0 && time.Unix(c.Iat, 0).After(now.Add(oidcSkew)):
		return Claims{}, fmt.Errorf("%w: issued in the future", ErrUnauthorized)
	}
	return c, nil
}

const oidcSkew = time.Minute

func decodeSegment(seg string, out any) error {
	b, err := base64.RawURLEncoding.DecodeString(seg)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, out)
}

func audienceIs(raw json.RawMessage, want string) bool {
	if want == "" {
		return false
	}
	var one string
	if json.Unmarshal(raw, &one) == nil {
		return one == want
	}
	var many []string
	if json.Unmarshal(raw, &many) == nil {
		for _, a := range many {
			if a == want {
				return true
			}
		}
	}
	return false
}

// key is GitHub's key kid from the cached set; an unknown kid, or a stale
// set, refetches it (at most once a minute). The fetch runs outside the
// lock; while one runs, a cached fresh key is still answered at once.
func (v *OIDCVerifier) key(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	for {
		v.mu.Lock()
		now := v.now()
		if k, ok := v.keys[kid]; ok && now.Before(v.fresh) {
			v.mu.Unlock()
			return k, nil
		}
		due := v.keys == nil || now.After(v.fresh) || now.Sub(v.fetched) > time.Minute
		if !due {
			k, ok := v.keys[kid]
			v.mu.Unlock()
			if ok {
				return k, nil
			}
			return nil, fmt.Errorf("%w: unknown key", ErrUnauthorized)
		}
		if wait := v.inflight; wait != nil {
			// Someone is fetching: wait for it, then look again.
			v.mu.Unlock()
			select {
			case <-wait:
				continue
			case <-ctx.Done():
				return nil, fmt.Errorf("%w: keys: %v", ErrUnauthorized, ctx.Err())
			}
		}
		done := make(chan struct{})
		v.inflight, v.fetched = done, now
		v.mu.Unlock()

		keys, err := v.fetchKeys(ctx)

		v.mu.Lock()
		if err == nil {
			v.keys, v.fresh = keys, now.Add(time.Hour)
		}
		v.inflight = nil
		close(done)
		k, ok := v.keys[kid]
		v.mu.Unlock()
		switch {
		case ok:
			return k, nil // a fresh key, or on a failed fetch the last one we had
		case err != nil:
			return nil, fmt.Errorf("%w: keys: %v", ErrUnauthorized, err)
		default:
			return nil, fmt.Errorf("%w: unknown key", ErrUnauthorized)
		}
	}
}

// fetchKeys reads GitHub's key set. It touches no state.
func (v *OIDCVerifier) fetchKeys(ctx context.Context) (map[string]*rsa.PublicKey, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, v.jwksURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := v.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	var set struct {
		Keys []struct {
			Kty string `json:"kty"`
			Kid string `json:"kid"`
			N   string `json:"n"`
			E   string `json:"e"`
		} `json:"keys"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 256<<10)).Decode(&set); err != nil {
		return nil, err
	}
	keys := map[string]*rsa.PublicKey{}
	for _, k := range set.Keys {
		if k.Kty != "RSA" || k.Kid == "" {
			continue
		}
		n, err1 := base64.RawURLEncoding.DecodeString(k.N)
		e, err2 := base64.RawURLEncoding.DecodeString(k.E)
		if err1 != nil || err2 != nil || len(e) == 0 || len(e) > 4 {
			continue
		}
		pub := &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(new(big.Int).SetBytes(e).Int64())}
		if pub.N.BitLen() < 2048 {
			continue
		}
		keys[k.Kid] = pub
	}
	if len(keys) == 0 {
		return nil, errors.New("none usable")
	}
	return keys, nil
}
