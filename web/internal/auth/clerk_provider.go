package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/clerk/clerk-sdk-go/v2"
	"github.com/clerk/clerk-sdk-go/v2/jwt"
	"github.com/clerk/clerk-sdk-go/v2/session"
	"github.com/clerk/clerk-sdk-go/v2/user"
)

// ClerkProvider integrates Clerk's hosted sign-in flow.
//
// Why the JS bridge: the v2 Go SDK only verifies tokens passed in
// the Authorization header. Clerk's __session cookie is set on
// the frontend API host (clerk.kiff.dev) with cookie attributes
// that don't reliably propagate to the apex (kiff.dev) on every
// browser, especially after Account-Portal-hosted sign-in. The
// reliable, documented pattern is to load Clerk's frontend SDK
// on the callback page, ask it for a session JWT via
// Clerk.session.getToken(), and POST that JWT to our backend.
// We then verify it with the Go SDK like any other API request.
// One JS file, ~15 lines of inline glue, no Node toolchain.
//
// The flow:
//
//  1. /sign-in (GET) redirects to the hosted Account Portal at
//     https://<account-portal>/sign-in?redirect_url=<callback>.
//     Clerk shows the sign-in widget (their UI, their CSS); we
//     render none of it.
//
//  2. After sign-in, Clerk redirects to <callback>. The callback
//     page is a tiny templ shell that loads
//     https://<frontend-api>/npm/@clerk/clerk-js@latest/dist/clerk.browser.js
//     plus a <15-line inline script that:
//     a. waits for Clerk to load and the session to hydrate
//     b. calls clerk.session.getToken() to mint a session JWT
//     c. POSTs the JWT to /sign-in/callback (this same path,
//     but POST instead of GET) as JSON
//     d. follows the redirect_url returned by the POST
//
//  3. POST /sign-in/callback verifies the JWT via the Go SDK,
//     mints our session cookie (Subject + Email + Bearer = the
//     Clerk JWT), and returns the redirect target as JSON. The
//     client-side script does window.location = <target>.
//
// Sign-out hits Clerk's hosted /sign-out URL after we clear our
// own cookie, so Clerk's session is also revoked.
type ClerkProvider struct {
	// frontendAPI is the host the SDK calls for JWKS verification,
	// session lookups, and other backend-API operations. It's
	// also the origin we load clerk.browser.js from on the
	// callback page.
	frontendAPI string

	// accountPortal is the hosted Account Portal host where users
	// are redirected to sign in. Distinct from the frontend API:
	// the portal serves HTML, the API serves JSON.
	accountPortal string

	// publishableKey is the pk_live_… or pk_test_… string. Embedded
	// in the callback page so Clerk's frontend SDK can identify
	// the instance.
	publishableKey string

	// callbackPath is where Clerk redirects after sign-in.
	// Defaults to "/sign-in/callback"; tests override.
	callbackPath string
}

// ClerkProviderOption configures ClerkProvider.
type ClerkProviderOption func(*ClerkProvider)

// WithCallbackPath overrides the default /sign-in/callback path.
// Tests use this; production keeps the default.
func WithCallbackPath(p string) ClerkProviderOption {
	return func(c *ClerkProvider) {
		if strings.HasPrefix(p, "/") {
			c.callbackPath = p
		}
	}
}

// WithAccountPortal overrides the auto-derived Account Portal
// host. Useful for instances where the portal host doesn't
// follow the "accounts.<frontend-apex>" convention.
func WithAccountPortal(host string) ClerkProviderOption {
	return func(c *ClerkProvider) {
		if h := normalizeHost(host); h != "" {
			c.accountPortal = h
		}
	}
}

// NewClerkProvider builds a ClerkProvider. frontendAPI is the
// instance's frontend API host (without protocol) — used by the
// SDK's JWKS verification path AND as the origin we load
// clerk.browser.js from on the callback page. publishableKey is
// the pk_… string the frontend SDK reads to identify the instance.
//
// accountPortal can be left empty: when omitted, NewClerkProvider
// derives it by prefixing "accounts." to frontendAPI's apex.
//
// Returns an error when frontendAPI or publishableKey is empty —
// both are required to render the callback page.
func NewClerkProvider(frontendAPI, publishableKey string, opts ...ClerkProviderOption) (*ClerkProvider, error) {
	frontendAPI = normalizeHost(frontendAPI)
	if frontendAPI == "" {
		return nil, errors.New("auth: clerk frontend API host is required")
	}
	publishableKey = strings.TrimSpace(publishableKey)
	if publishableKey == "" {
		return nil, errors.New("auth: clerk publishable key is required (loads clerk.browser.js on the callback page)")
	}
	c := &ClerkProvider{
		frontendAPI:    frontendAPI,
		accountPortal:  deriveAccountPortal(frontendAPI),
		publishableKey: publishableKey,
		callbackPath:   "/sign-in/callback",
	}
	for _, opt := range opts {
		opt(c)
	}
	return c, nil
}

// normalizeHost strips scheme + trailing slash from a configured
// Clerk host. Inputs like "https://clerk.kiff.dev/" become
// "clerk.kiff.dev".
func normalizeHost(raw string) string {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "https://")
	raw = strings.TrimPrefix(raw, "http://")
	return strings.TrimSuffix(raw, "/")
}

// deriveAccountPortal returns the Account Portal host for a given
// frontend API host. The convention Clerk uses is to prefix
// "accounts." to the frontend host, swapping "clerk." for
// "accounts." when the frontend host already has that prefix
// (clerk.kiff.dev → accounts.kiff.dev).
//
// For dev hosts (<name>.clerk.accounts.dev), the convention is
// accounts.<name>.clerk.accounts.dev.
func deriveAccountPortal(frontendAPI string) string {
	if strings.HasPrefix(frontendAPI, "clerk.") {
		return "accounts." + strings.TrimPrefix(frontendAPI, "clerk.")
	}
	return "accounts." + frontendAPI
}

// Name implements Provider.
func (c *ClerkProvider) Name() string { return string(ProviderClerk) }

// SignInForm implements Provider. Redirects to Clerk's hosted
// sign-in URL. The next= query parameter (set by RequireSession
// when the user tried to reach a gated route) is preserved
// through the round-trip via the redirect_url's "next" query.
func (c *ClerkProvider) SignInForm(w http.ResponseWriter, r *http.Request) {
	callback := c.absoluteCallback(r)
	if next := strings.TrimSpace(r.URL.Query().Get("next")); next != "" {
		callbackURL, _ := url.Parse(callback)
		q := callbackURL.Query()
		q.Set("next", next)
		callbackURL.RawQuery = q.Encode()
		callback = callbackURL.String()
	}
	target := "https://" + c.accountPortal + "/sign-in?redirect_url=" + url.QueryEscape(callback)
	http.Redirect(w, r, target, http.StatusFound)
}

// VerifySignIn implements Provider. The Clerk path with the JS
// bridge accepts a JSON body {"token": "<clerk-jwt>"} from the
// callback page's inline script. The JWT is verified via the Go
// SDK and an Identity is produced.
//
// On success the returned Identity carries:
//   - Subject: the Clerk user id (claims.Subject)
//   - SessionID: the Clerk session id, used by FreshBearer to
//     mint short-lived JWTs on demand
//   - Bearer: the JWT we just verified, valid for ~60s — fine for
//     the redirect that follows but not stored long-term
//   - Email + DisplayName: best-effort lookups via the Backend
//     API; failures are non-fatal (the dashboard renders the
//     subject id when nothing better is known).
func (c *ClerkProvider) VerifySignIn(ctx context.Context, r *http.Request) (Identity, error) {
	if r.Header.Get("Content-Type") == "" || !strings.Contains(r.Header.Get("Content-Type"), "application/json") {
		return Identity{}, errors.New("auth: clerk: callback expects application/json body with the Clerk session JWT")
	}
	var body struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		return Identity{}, fmt.Errorf("auth: clerk: decode callback body: %w", err)
	}
	token := strings.TrimSpace(body.Token)
	if token == "" {
		return Identity{}, errors.New("auth: clerk: callback body missing token")
	}

	claims, err := jwt.Verify(ctx, &jwt.VerifyParams{Token: token})
	if err != nil {
		return Identity{}, fmt.Errorf("auth: clerk: verify session jwt: %w", err)
	}
	if claims == nil || claims.Subject == "" {
		return Identity{}, errors.New("auth: clerk: claims missing subject")
	}

	id := Identity{
		Subject:   claims.Subject,
		Bearer:    token,
		SessionID: claims.SessionID,
	}
	// Best-effort enrichment from the Backend API. Failures here
	// are not fatal; the dashboard renders the subject id when
	// these are empty.
	if u, ferr := user.Get(ctx, claims.Subject); ferr == nil && u != nil {
		id.Email = primaryEmail(u)
		id.DisplayName = displayNameFor(u, id.Email)
	}
	if err := id.Validate(); err != nil {
		return Identity{}, err
	}
	return id, nil
}

// FreshBearer mints a short-lived session JWT for the given
// Clerk session id via the Backend API. The dashboard calls this
// on every apps/api request because Clerk session JWTs expire in
// ~60 seconds — caching the one we got at sign-in time would
// force the user back through the sign-in flow on every click
// after the first minute.
//
// Returns the empty string and an error when sessionID is empty
// (stub mode never sets it) or the Backend API call fails. The
// caller should treat the failure path as "session is no longer
// valid" and bounce the user to /sign-in.
func (c *ClerkProvider) FreshBearer(ctx context.Context, sessionID string) (string, error) {
	if strings.TrimSpace(sessionID) == "" {
		return "", errors.New("auth: clerk: empty session id")
	}
	tok, err := session.CreateToken(ctx, &session.CreateTokenParams{ID: sessionID})
	if err != nil {
		return "", fmt.Errorf("auth: clerk: mint fresh session token: %w", err)
	}
	if tok == nil || strings.TrimSpace(tok.JWT) == "" {
		return "", errors.New("auth: clerk: empty fresh session token")
	}
	return tok.JWT, nil
}

// primaryEmail picks the best email address from a Clerk user
// record. Falls back to the first email if no primary is set.
func primaryEmail(u *clerk.User) string {
	if u == nil {
		return ""
	}
	if u.PrimaryEmailAddressID != nil {
		want := *u.PrimaryEmailAddressID
		for _, e := range u.EmailAddresses {
			if e != nil && e.ID == want {
				return e.EmailAddress
			}
		}
	}
	for _, e := range u.EmailAddresses {
		if e != nil && e.EmailAddress != "" {
			return e.EmailAddress
		}
	}
	return ""
}

// displayNameFor returns a friendly name for the dashboard
// header. Order: "First Last", "First", email local-part, email,
// empty.
func displayNameFor(u *clerk.User, email string) string {
	if u != nil {
		first := strings.TrimSpace(strDeref(u.FirstName))
		last := strings.TrimSpace(strDeref(u.LastName))
		switch {
		case first != "" && last != "":
			return first + " " + last
		case first != "":
			return first
		case last != "":
			return last
		}
	}
	if email != "" {
		if at := strings.IndexByte(email, '@'); at > 0 {
			return email[:at]
		}
		return email
	}
	return ""
}

func strDeref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// absoluteCallback returns the full URL Clerk redirects back to
// after sign-in. r.Host is trusted because the request reaches
// us through Caddy / our own listener; both terminate on the
// dashboard's host.
func (c *ClerkProvider) absoluteCallback(r *http.Request) string {
	scheme := "https"
	if r.TLS == nil && !strings.HasSuffix(r.Host, ":443") {
		if proto := r.Header.Get("X-Forwarded-Proto"); proto != "" {
			scheme = proto
		} else if strings.HasPrefix(r.Host, "localhost") || strings.HasPrefix(r.Host, "127.0.0.1") {
			scheme = "http"
		}
	}
	return scheme + "://" + r.Host + c.callbackPath
}

// RevokeSession revokes the Clerk session identified by sessionID
// via the Backend API. This is called server-side during sign-out
// so Clerk's own session is invalidated before we clear our cookie.
//
// Clerk's hosted Account Portal does not expose a /sign-out page
// (hitting accounts.kiff.dev/sign-out returns a 404). The correct
// pattern is to revoke server-side and then redirect directly to
// the home page — no round-trip through Clerk's hosted UI needed.
//
// A best-effort call: if sessionID is empty (stub sessions never
// set it) or the Backend API call fails, the error is returned so
// the caller can log it, but sign-out can still proceed by clearing
// the local cookie.
func (c *ClerkProvider) RevokeSession(ctx context.Context, sessionID string) error {
	if strings.TrimSpace(sessionID) == "" {
		return errors.New("auth: clerk: empty session id — cannot revoke")
	}
	if _, err := session.Revoke(ctx, &session.RevokeParams{ID: sessionID}); err != nil {
		return fmt.Errorf("auth: clerk: revoke session %s: %w", sessionID, err)
	}
	return nil
}

// FrontendAPI returns the configured Clerk frontend API host.
// Exposed so the callback page template knows where to load
// clerk.browser.js from.
func (c *ClerkProvider) FrontendAPI() string { return c.frontendAPI }

// AccountPortal returns the configured Account Portal host.
// Exposed for templates that link directly to hosted UI
// endpoints (sign-up, manage-account, etc.).
func (c *ClerkProvider) AccountPortal() string { return c.accountPortal }

// PublishableKey returns the configured publishable key. The
// callback page renders it into the inline Clerk JS init.
func (c *ClerkProvider) PublishableKey() string { return c.publishableKey }
