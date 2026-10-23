package auth

import (
	"context"
	"errors"
	"fmt"
)

// Provider is the runtime auth surface the API server depends on.
// It captures the two operations the HTTP middleware needs:
// verifying a session token, and (later, in slice 3) verifying an
// API key. v0.1 wires session verification only; the API-key path
// is part of slice 3.
//
// Provider is not the same thing as ActorResolver: the provider's
// job is "is this token / key valid, who does it belong to" and
// the resolver's job is "given that identity, what tenant + actor
// is it." The middleware composes both.
type Provider interface {
	// VerifySession verifies a user-session token and returns an
	// Identity. The Identity's Tenant is empty at this point; the
	// resolver fills it in.
	VerifySession(ctx context.Context, token string) (Identity, error)
}

// ProviderName labels the configured provider for logging and
// observability. Stub is the safe default for tests and dev;
// Clerk is production.
type ProviderName string

const (
	ProviderClerk ProviderName = "clerk"
	ProviderStub  ProviderName = "stub"
)

// ErrProviderUnsupported is returned when the configured provider name
// isn't a known one.
var ErrProviderUnsupported = errors.New("auth: unsupported provider")

// VerifySession on ClerkSessions is the Provider implementation
// for the Clerk path. The thin shim exists so ClerkSessions can be
// used directly for non-HTTP callers that want VerifyToken's name.
func (c *ClerkSessions) VerifySession(ctx context.Context, token string) (Identity, error) {
	return c.VerifyToken(ctx, token)
}

// StubProvider is the dev/test provider. It treats the supplied
// "token" as a subject id and looks the subject up in the backing
// MapResolver. The intent is that integration tests register
// fixture identities and exercise the full middleware path without
// a real Clerk dependency. Production code should never reach this
// provider.
type StubProvider struct {
	backing *MapResolver
}

// NewStubProvider wires a StubProvider against the given
// MapResolver. A nil MapResolver yields a provider that rejects
// every token; useful for smoke-testing the unauthenticated path.
func NewStubProvider(backing *MapResolver) *StubProvider {
	return &StubProvider{backing: backing}
}

// VerifySession treats the supplied token string as a subject id
// and emits an Identity if and only if the MapResolver knows about
// it. The returned Identity carries Method=MethodSession so the
// rest of the pipeline behaves identically to the Clerk path.
func (s *StubProvider) VerifySession(_ context.Context, token string) (Identity, error) {
	if s == nil || s.backing == nil {
		return Identity{}, fmt.Errorf("%w: stub provider has no backing resolver", ErrInvalidToken)
	}
	subject := trimBearer(token)
	if subject == "" {
		return Identity{}, fmt.Errorf("%w: empty token", ErrInvalidToken)
	}
	s.backing.mu.RLock()
	pair, ok := s.backing.pairs[subject]
	s.backing.mu.RUnlock()
	if !ok {
		return Identity{}, fmt.Errorf("%w: subject %q not registered with stub provider", ErrInvalidToken, subject)
	}
	return Identity{
		Subject: subject,
		Method:  MethodSession,
		Roles:   append([]string(nil), pair.Actor.Roles...),
	}, nil
}
