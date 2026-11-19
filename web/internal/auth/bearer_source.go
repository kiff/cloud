package auth

import (
	"context"
	"errors"
	"strings"

	"github.com/kiff/cloud/web/internal/session"
)

// BearerMinter is the contract a hosted-auth provider exposes to
// the dashboard so it can mint short-lived bearers per apps/api
// call. ClerkProvider satisfies it via Clerk's Backend API.
type BearerMinter interface {
	FreshBearer(ctx context.Context, sessionID string) (string, error)
}

// HostedBearerSource refreshes the bearer on every call by asking
// the underlying minter for a fresh JWT keyed off the upstream
// session id stored in our cookie. This is what makes interactive
// dashboard clicks work past the ~60-second Clerk JWT lifetime —
// without it, every click after the first minute would 401 on
// apps/api and bounce the user back through sign-in.
//
// When the session was issued without an upstream session id
// (stub mode, or a Clerk session minted by an old build), the
// source falls back to the bearer stored at sign-in time.
type HostedBearerSource struct {
	minter BearerMinter
}

// NewHostedBearerSource wires the minter. minter must be non-nil.
func NewHostedBearerSource(minter BearerMinter) *HostedBearerSource {
	return &HostedBearerSource{minter: minter}
}

// BearerFor implements the dashboard's BearerSource contract.
func (s *HostedBearerSource) BearerFor(ctx context.Context, sess session.Session) (string, error) {
	if s == nil || s.minter == nil {
		return "", errors.New("auth: no bearer minter configured")
	}
	if sid := strings.TrimSpace(sess.SessionID); sid != "" {
		tok, err := s.minter.FreshBearer(ctx, sid)
		if err == nil {
			return tok, nil
		}
		// Fall through to the cached bearer; this is best-effort
		// resilience for a transient Backend API blip. The cached
		// bearer is short-lived too, so the apps/api call will
		// either succeed (if the cached JWT hasn't expired yet)
		// or 401, at which point the dashboard's normal
		// sign-out-on-401 path kicks in.
	}
	if strings.TrimSpace(sess.Bearer) == "" {
		return "", errors.New("auth: session has no bearer and minter unavailable")
	}
	return sess.Bearer, nil
}
