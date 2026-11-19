package playstore

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
	"strconv"
	"strings"
	"sync"
	"time"
)

// DefaultJWKSURL is where Google publishes the keys that sign its OIDC
// tokens, Pub/Sub push tokens among them.
const DefaultJWKSURL = "https://www.googleapis.com/oauth2/v3/certs"

// ErrPushUnauthorized is a push whose bearer token isn't Google's, for this
// audience, as this service account.
var ErrPushUnauthorized = errors.New("playstore: push token refused")

// clockSkew is the leeway on a token's times.
const clockSkew = time.Minute

// PushVerifier checks the OIDC token Pub/Sub sends with each push: an RS256
// JWT signed by one of Google's published keys, issued by Google, for the
// configured audience, as the configured service account, its email
// verified, and not expired.
type PushVerifier struct {
	audience string
	email    string
	jwksURL  string
	http     *http.Client
	now      func() time.Time

	mu      sync.Mutex
	keys    map[string]*rsa.PublicKey
	fresh   time.Time // keys are fresh until then
	fetched time.Time // last fetch, so an unknown kid refetches at most once a minute
}

// NewPushVerifier accepts pushes for audience, signed as serviceAccountEmail.
func NewPushVerifier(audience, serviceAccountEmail string) *PushVerifier {
	return &PushVerifier{audience: audience, email: serviceAccountEmail, jwksURL: DefaultJWKSURL,
		http: &http.Client{Timeout: 10 * time.Second}, now: time.Now}
}

// WithJWKS reads Google's keys from url instead: a fake's, for tests.
func (v *PushVerifier) WithJWKS(url string) *PushVerifier {
	v.jwksURL = url
	return v
}

// WithClock sets the clock, for tests.
func (v *PushVerifier) WithClock(now func() time.Time) *PushVerifier {
	v.now = now
	return v
}

// Verify checks the request's Authorization: Bearer token.
func (v *PushVerifier) Verify(ctx context.Context, r *http.Request) error {
	auth := r.Header.Get("Authorization")
	token, ok := strings.CutPrefix(auth, "Bearer ")
	if !ok || token == "" {
		return fmt.Errorf("%w: no bearer token", ErrPushUnauthorized)
	}
	return v.VerifyToken(ctx, strings.TrimSpace(token))
}

// VerifyToken checks one token.
func (v *PushVerifier) VerifyToken(ctx context.Context, token string) error {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return fmt.Errorf("%w: not a JWT", ErrPushUnauthorized)
	}
	var h struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	if err := decodeSegment(parts[0], &h); err != nil {
		return fmt.Errorf("%w: header", ErrPushUnauthorized)
	}
	if h.Alg != "RS256" || h.Kid == "" {
		return fmt.Errorf("%w: alg %q", ErrPushUnauthorized, h.Alg)
	}
	key, err := v.key(ctx, h.Kid)
	if err != nil {
		return err
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return fmt.Errorf("%w: signature", ErrPushUnauthorized)
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(key, crypto.SHA256, digest[:], sig); err != nil {
		return fmt.Errorf("%w: signature", ErrPushUnauthorized)
	}
	var c struct {
		Iss           string          `json:"iss"`
		Aud           json.RawMessage `json:"aud"`
		Email         string          `json:"email"`
		EmailVerified any             `json:"email_verified"`
		Exp           int64           `json:"exp"`
		Iat           int64           `json:"iat"`
	}
	if err := decodeSegment(parts[1], &c); err != nil {
		return fmt.Errorf("%w: claims", ErrPushUnauthorized)
	}
	now := v.now()
	switch {
	case c.Iss != "https://accounts.google.com" && c.Iss != "accounts.google.com":
		return fmt.Errorf("%w: issuer %q", ErrPushUnauthorized, c.Iss)
	case !audienceIs(c.Aud, v.audience):
		return fmt.Errorf("%w: another audience", ErrPushUnauthorized)
	case !strings.EqualFold(c.Email, v.email) || !verified(c.EmailVerified):
		return fmt.Errorf("%w: another service account", ErrPushUnauthorized)
	case c.Exp == 0 || now.After(time.Unix(c.Exp, 0).Add(clockSkew)):
		return fmt.Errorf("%w: expired", ErrPushUnauthorized)
	case c.Iat != 0 && time.Unix(c.Iat, 0).After(now.Add(clockSkew)):
		return fmt.Errorf("%w: issued in the future", ErrPushUnauthorized)
	}
	return nil
}

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

func verified(v any) bool {
	switch b := v.(type) {
	case bool:
		return b
	case string:
		return b == "true"
	}
	return false
}

// key is Google's key kid, from the cached set; an unknown kid, or a set
// gone stale, refetches it.
func (v *PushVerifier) key(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	now := v.now()
	if k, ok := v.keys[kid]; ok && now.Before(v.fresh) {
		return k, nil
	}
	if v.keys == nil || now.After(v.fresh) || now.Sub(v.fetched) > time.Minute {
		if err := v.fetch(ctx, now); err != nil {
			if k, ok := v.keys[kid]; ok {
				// Google's keys unreachable: the cached one still holds.
				return k, nil
			}
			return nil, err
		}
	}
	if k, ok := v.keys[kid]; ok {
		return k, nil
	}
	return nil, fmt.Errorf("%w: unknown key", ErrPushUnauthorized)
}

func (v *PushVerifier) fetch(ctx context.Context, now time.Time) error {
	v.fetched = now
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, v.jwksURL, nil)
	if err != nil {
		return err
	}
	resp, err := v.http.Do(req)
	if err != nil {
		return fmt.Errorf("playstore: google keys: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("playstore: google keys: status %d", resp.StatusCode)
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
		return fmt.Errorf("playstore: google keys: %w", err)
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
		return fmt.Errorf("playstore: google keys: none usable")
	}
	v.keys = keys
	v.fresh = now.Add(maxAge(resp.Header.Get("Cache-Control")))
	return nil
}

// maxAge is Cache-Control's max-age, an hour when absent, at most a day.
func maxAge(cc string) time.Duration {
	for _, part := range strings.Split(cc, ",") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(part), "max-age="); ok {
			if s, err := strconv.Atoi(v); err == nil && s > 0 {
				return min(time.Duration(s)*time.Second, 24*time.Hour)
			}
		}
	}
	return time.Hour
}
