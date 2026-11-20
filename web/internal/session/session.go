// Package session manages the cloud dashboard's signed-in state.
//
// The dashboard binary maintains its own session cookie rather
// than forwarding the upstream auth provider's cookie verbatim.
// Why: the cloud's auth provider may be Clerk in production, a
// stub in local dev, or a future replacement. Putting a thin
// session layer in front of the provider lets the dashboard's
// HTTP handlers stay provider-agnostic — they just ask the
// session for the current subject.
//
// The cookie payload is a JSON-encoded Session struct, base64url
// encoded, then HMAC-signed with a per-deploy secret. Rotating
// the secret invalidates every existing session, which is the
// intended panic button for a credential leak.
//
// What this package is NOT:
//
//   - A replacement for the upstream auth provider. The provider
//     is still the source of truth for "is this user real". The
//     session just remembers the result of a successful sign-in
//     so subsequent requests don't have to re-verify.
//   - A federated identity layer. One provider at a time. The
//     stub provider is for local dev; production uses Clerk.
package session

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// CookieName is the name of the session cookie. Prefixed with
// "kiff_" so it does not collide with the upstream auth
// provider's cookies (Clerk's "__session" in particular).
const CookieName = "kiff_session"

// Session is the data carried by the cookie. Subject is the
// upstream auth subject (Clerk OIDC sub, or a synthetic id in
// stub mode). Email is best-effort — empty when the auth
// provider didn't supply one. ExpiresAt is when the cookie
// becomes invalid; the manager rejects expired cookies even when
// the signature still verifies.
//
// Bearer is the credential the dashboard forwards to the API on
// the user's behalf. In stub mode it is the literal subject
// string (the API's StubProvider treats the bearer as a subject
// id). In Clerk mode it is the Clerk session JWT, which the
// API's middleware verifies via clerksdk.
//
// SessionID is the Clerk session id (sess_…). Empty in stub mode.
// Stored alongside the bearer so the cloud can mint a fresh,
// short-lived JWT via Clerk's Backend API on each apps/api call —
// Clerk's session JWTs expire in ~60 seconds, so a long-lived
// cookie cannot store a single bearer and still drive interactive
// dashboard clicks.
//
// DisplayName is a human-readable label sourced from the auth
// provider (Clerk's first/last name, or email). The dashboard
// renders this in the welcome line instead of a raw subject id.
type Session struct {
	Subject     string    `json:"subject"`
	Email       string    `json:"email,omitempty"`
	Bearer      string    `json:"bearer"`
	SessionID   string    `json:"session_id,omitempty"`
	DisplayName string    `json:"display_name,omitempty"`
	ExpiresAt   time.Time `json:"expires_at"`
	// SignedInFor is the tafs app's sign-in request this session's sign-in
	// came back to (/sign-in/app?request=…), when it did: the app sign-in page
	// answers a request at once only for a session signed in for it.
	SignedInFor string `json:"signed_in_for,omitempty"`
	// LastDomain is the slug of the domain the user most recently
	// viewed (domain home or Activity). It lets the domain-scoped
	// spine survive a hop through a non-domain tab (Runtimes,
	// Examples): the slug-free /dashboard/activity resolves to this
	// domain instead of bouncing to the apex. Best-effort UX state,
	// validated against the live domain list before use, so a stale
	// or deleted slug never routes anywhere real.
	LastDomain string `json:"last_domain,omitempty"`
	// LastWindow is the evidence window preset ("7d"/"30d"/"90d") the
	// user most recently selected on Govern/Activity. It lets the window
	// survive a hop through another tab (Govern → Build → Govern) instead
	// of resetting to the 30-day default. Best-effort UX state; an unknown
	// value falls back to the default.
	LastWindow string `json:"last_window,omitempty"`
	// NotificationsClearedAt is when the user last cleared the bell.
	// The bell hides notifications older than this; the full
	// notifications page still lists them.
	NotificationsClearedAt time.Time `json:"notifications_cleared_at,omitzero"`
	// Tenant is the account the person is working in, when they belong
	// to more than one. The dashboard sends it as the Kiff-Tenant
	// header; it selects which membership the API checks and grants
	// nothing by itself. Re-checked against the person's memberships
	// on every request.
	Tenant string `json:"tenant,omitempty"`
}

// Valid returns nil when the session is non-empty and not
// expired. now is the clock used for the comparison; pass
// time.Now() in production code.
func (s Session) Valid(now time.Time) error {
	if s.Subject == "" {
		return errors.New("session: empty subject")
	}
	if s.Bearer == "" {
		return errors.New("session: empty bearer")
	}
	if s.ExpiresAt.Before(now) {
		return errors.New("session: expired")
	}
	return nil
}

// Manager issues, verifies, and clears session cookies. The
// secret must be at least 32 bytes; production deploys read it
// from the env var named by the binary, tests pass a fixed
// value.
type Manager struct {
	secret       []byte
	cookieSecure bool
	cookieDomain string
	defaultTTL   time.Duration
	now          func() time.Time
	reveals      *revealVault
}

// ManagerOption configures Manager.
type ManagerOption func(*Manager)

// WithSecure flags whether the cookie carries the Secure
// attribute (HTTPS-only). Production sets this true; local dev
// over plain HTTP sets it false.
func WithSecure(secure bool) ManagerOption {
	return func(m *Manager) { m.cookieSecure = secure }
}

// WithDomain sets the cookie's Domain attribute. Empty (default)
// scopes the cookie to the host that issued it; production
// deploys spanning kiff.dev + app.kiff.dev should set this to
// ".kiff.dev" if cross-subdomain sessions are needed (they are
// not in v0.1).
func WithDomain(domain string) ManagerOption {
	return func(m *Manager) { m.cookieDomain = domain }
}

// WithTTL overrides the default session lifetime. Defaults to
// 24h, long enough that a user's day does not interrupted by
// re-sign-in but short enough that a stolen cookie expires
// before most credential-rotation windows.
func WithTTL(d time.Duration) ManagerOption {
	return func(m *Manager) { m.defaultTTL = d }
}

// WithClock substitutes the clock used for cookie expiration.
// Tests use this to drive cookies past their expiry without
// real-time waits.
func WithClock(now func() time.Time) ManagerOption {
	return func(m *Manager) {
		if now != nil {
			m.now = now
		}
	}
}

// ErrSecretTooShort is returned by NewManager when secret is
// less than 32 bytes. The cookie's HMAC is sha-256, so anything
// shorter weakens the keyspace.
var ErrSecretTooShort = errors.New("session: secret must be at least 32 bytes")

// NewManager constructs a Manager with the given HMAC secret.
// Returns ErrSecretTooShort when secret is below the minimum
// length.
func NewManager(secret []byte, opts ...ManagerOption) (*Manager, error) {
	if len(secret) < 32 {
		return nil, ErrSecretTooShort
	}
	m := &Manager{
		secret:     append([]byte(nil), secret...),
		defaultTTL: 24 * time.Hour,
		now:        func() time.Time { return time.Now().UTC() },
		reveals:    newRevealVault(),
	}
	for _, opt := range opts {
		opt(m)
	}
	return m, nil
}

// Issue persists s as a cookie on w. ExpiresAt is set to
// now()+defaultTTL when the caller leaves it zero. The cookie
// is HttpOnly + SameSite=Lax + Path=/.
func (m *Manager) Issue(w http.ResponseWriter, s Session) error {
	if s.Subject == "" || s.Bearer == "" {
		return errors.New("session: subject and bearer are required to issue")
	}
	if s.ExpiresAt.IsZero() {
		s.ExpiresAt = m.now().Add(m.defaultTTL)
	}
	encoded, err := encode(s, m.secret)
	if err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    encoded,
		Path:     "/",
		Domain:   m.cookieDomain,
		Expires:  s.ExpiresAt,
		HttpOnly: true,
		Secure:   m.cookieSecure,
		SameSite: http.SameSiteLaxMode,
	})
	return nil
}

// Read returns the session encoded in r's cookie. Returns
// ErrNoSession when no cookie is present, ErrInvalid when the
// cookie's signature does not verify, or any session-validation
// error returned by Session.Valid.
//
// The browser can present MORE THAN ONE cookie named CookieName
// when the deploy's cookie Domain changed: a stale host-only
// cookie (Domain=<host>, minted before KIFF_COOKIE_DOMAIN was
// set) coexists with the current domain-scoped cookie
// (Domain=.kiff.dev). net/http's r.Cookie returns only the first
// by name — which, by RFC 6265 send-order, is usually the older
// stale one — so a valid session would be shadowed by a dead
// cookie and the user would loop through sign-in forever. We
// therefore scan every cookie of this name and accept the first
// that decodes and validates, so a good session is never hidden
// behind a stale duplicate.
func (m *Manager) Read(r *http.Request) (Session, error) {
	cookies := r.CookiesNamed(CookieName)
	if len(cookies) == 0 {
		return Session{}, ErrNoSession
	}
	lastErr := error(ErrNoSession)
	for _, c := range cookies {
		s, err := decode(c.Value, m.secret)
		if err != nil {
			lastErr = err
			continue
		}
		if err := s.Valid(m.now()); err != nil {
			lastErr = err
			continue
		}
		return s, nil
	}
	return Session{}, lastErr
}

// Clear removes the session cookie from the client by setting
// an expired empty value. When a cookie Domain is configured it
// also emits a host-only deletion (Domain="") so a stale
// host-only duplicate from a pre-domain deploy is evicted too —
// otherwise Clear would only delete the domain-scoped variant
// and the stale one would keep shadowing it (the sign-in loop).
func (m *Manager) Clear(w http.ResponseWriter) {
	m.clearCookie(w, m.cookieDomain)
	if m.cookieDomain != "" {
		m.clearCookie(w, "")
	}
}

// CookieSecure reports whether the manager issues Secure
// (HTTPS-only) cookies. Exposed so sibling handlers (the Clerk
// callback loop-breaker, in particular) can mint short-lived
// guard cookies with the same Secure attribute as the session
// cookie, rather than guessing.
func (m *Manager) CookieSecure() bool { return m.cookieSecure }

func (m *Manager) clearCookie(w http.ResponseWriter, domain string) {
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    "",
		Path:     "/",
		Domain:   domain,
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   m.cookieSecure,
		SameSite: http.SameSiteLaxMode,
	})
}

// Errors surfaced by Read and the encode/decode helpers.
var (
	ErrNoSession = errors.New("session: no cookie")
	ErrInvalid   = errors.New("session: cookie signature does not verify")
)

// encode JSON-encodes s, signs it with secret, and returns
// "<base64url-payload>.<base64url-signature>".
func encode(s Session, secret []byte) (string, error) {
	body, err := json.Marshal(s)
	if err != nil {
		return "", fmt.Errorf("session: marshal: %w", err)
	}
	return encodeRaw(body, secret), nil
}

// encodeRaw signs already-marshaled JSON. Exposed package-locally
// so the flash cookie shares the same signing path without
// duplicating logic.
func encodeRaw(body, secret []byte) string {
	enc := base64.RawURLEncoding
	payload := enc.EncodeToString(body)
	sig := sign(secret, []byte(payload))
	return payload + "." + enc.EncodeToString(sig)
}

// decode parses and verifies a session cookie value. Returns
// ErrInvalid for any signature mismatch or malformed cookie, and
// a wrapped JSON error for malformed payload.
func decode(value string, secret []byte) (Session, error) {
	body, err := decodeRaw(value, secret)
	if err != nil {
		return Session{}, err
	}
	var s Session
	if err := json.Unmarshal(body, &s); err != nil {
		return Session{}, fmt.Errorf("session: unmarshal: %w", err)
	}
	return s, nil
}

// decodeRaw verifies the signature and returns the JSON payload
// bytes. Same contract as decode but without unmarshalling, so
// the flash cookie can decode into its own type.
func decodeRaw(value string, secret []byte) ([]byte, error) {
	parts := strings.SplitN(value, ".", 2)
	if len(parts) != 2 {
		return nil, ErrInvalid
	}
	enc := base64.RawURLEncoding
	gotSig, err := enc.DecodeString(parts[1])
	if err != nil {
		return nil, ErrInvalid
	}
	wantSig := sign(secret, []byte(parts[0]))
	if !hmac.Equal(gotSig, wantSig) {
		return nil, ErrInvalid
	}
	return enc.DecodeString(parts[0])
}

func sign(secret, payload []byte) []byte {
	h := hmac.New(sha256.New, secret)
	h.Write(payload)
	return h.Sum(nil)
}

// sealed is a value signed for one purpose (Seal): the purpose keeps a value
// sealed for one cookie from passing as another's, or as a session.
type sealed struct {
	Purpose string `json:"purpose"`
	Value   string `json:"value"`
}

// Seal signs value for purpose with the manager's secret, for a cookie the
// browser carries and can't forge. The value isn't hidden: never seal a
// secret.
func (m *Manager) Seal(purpose, value string) string {
	body, _ := json.Marshal(sealed{Purpose: purpose, Value: value})
	return encodeRaw(body, m.secret)
}

// Open returns the value Seal signed for purpose, and false when the cookie
// is forged, malformed, or sealed for anything else.
func (m *Manager) Open(purpose, value string) (string, bool) {
	body, err := decodeRaw(value, m.secret)
	if err != nil {
		return "", false
	}
	var s sealed
	if json.Unmarshal(body, &s) != nil || s.Purpose == "" || s.Purpose != purpose {
		return "", false
	}
	return s.Value, true
}
