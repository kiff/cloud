// Package auth wires the dashboard's sign-in flow.
//
// Two providers are supported:
//
//   - stub: local dev. Renders a one-field email form on
//     /sign-in; on submit, mints a session whose subject is the
//     email and whose bearer is the literal subject string. The
//     apps/api StubProvider treats the bearer as a subject id
//     and resolves it through MapResolver — fine for tests and
//     local smoke runs. NEVER enable in production.
//
//   - clerk: production (follow-up slice). Reads the Clerk
//     session cookie that Clerk's hosted JS sets, verifies it
//     server-side via clerksdk, and mints a session whose
//     bearer is a short-lived JWT the apps/api side already
//     accepts in clerk mode. Stubbed here as ProviderClerk so
//     env wiring is in place without dragging the Clerk SDK
//     into the web binary on this slice.
//
// The dashboard's HTTP handlers depend on Provider, not on a
// concrete implementation, so swapping in Clerk later is one
// constructor change in cmd/web/main.go.
package auth

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"
)

// Provider is the contract the sign-in handlers depend on.
//
// SignInForm renders the provider-specific form (or, for
// hosted-redirect providers, performs the redirect). The form
// posts to /sign-in which calls VerifySignIn.
//
// VerifySignIn validates a sign-in attempt and returns the
// minted Identity. The cloud's session manager wraps Identity
// in a session.Session.
type Provider interface {
	// Name returns a label for logging ("stub", "clerk", etc.).
	Name() string

	// SignInForm renders the form (or redirect) shown at GET /sign-in.
	SignInForm(w http.ResponseWriter, r *http.Request)

	// VerifySignIn parses the POST /sign-in body and produces
	// an Identity on success. Validation failures (empty fields,
	// malformed inputs) return an error that the handler
	// surfaces to the user.
	VerifySignIn(ctx context.Context, r *http.Request) (Identity, error)
}

// Identity is what the dashboard side knows about a successful
// sign-in. Subject is the upstream auth subject. Email is
// best-effort. Bearer is the credential the dashboard forwards
// to apps/api on the user's behalf. SessionID is the upstream
// session id (Clerk's sess_…) used to mint short-lived JWTs on
// demand. DisplayName is a human-readable label (full name,
// email, or empty).
type Identity struct {
	Subject     string
	Email       string
	Bearer      string
	SessionID   string
	DisplayName string
}

// Validate ensures the identity has the minimum fields the
// downstream session/forwarding path needs.
func (i Identity) Validate() error {
	if strings.TrimSpace(i.Subject) == "" {
		return errors.New("auth: empty subject")
	}
	if strings.TrimSpace(i.Bearer) == "" {
		return errors.New("auth: empty bearer")
	}
	return nil
}

// ProviderName labels a configured provider. Used by the env
// switcher in cmd/web.
type ProviderName string

const (
	ProviderStub  ProviderName = "stub"
	ProviderClerk ProviderName = "clerk"
)

// Errors surfaced by the auth flow.
var (
	ErrInvalidCredentials = errors.New("auth: invalid credentials")
	ErrProviderNotWired   = errors.New("auth: provider not yet wired")
)

// ttlForRole picks the session lifetime by the provider. Stub
// sessions are short (24h) so a forgotten dev-mode cookie does
// not stick around indefinitely. Clerk follow-up will respect
// the Clerk session's own expiry.
//
// Currently unused — kept for the Clerk slice to wire in.
func ttlForRole(_ ProviderName) time.Duration { return 24 * time.Hour }
