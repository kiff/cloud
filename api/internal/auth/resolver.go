package auth

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/kiff/kiff/pkg/kiff/actor"
)

// ErrUnknownIdentity is returned by an ActorResolver when the
// identity cannot be mapped to an actor + tenant. Middleware
// translates this to 401 by default; route handlers may translate
// it to 403 when the auth check has already passed but
// tenant-level membership is missing.
var ErrUnknownIdentity = errors.New("auth: identity is not known to the cloud")

// ErrResolverUnavailable marks failures that occur after a credential has
// already been verified but before its tenant can be loaded. Middleware maps
// this to 503, not 401: retrying sign-in cannot repair a database outage.
var ErrResolverUnavailable = errors.New("auth: identity resolver unavailable")

// ActorResolver translates a verified identity into a framework
// actor and the tenant id the actor belongs to. Implementations
// must be safe for concurrent use.
//
// The interface deliberately accepts an Identity value and not a
// *http.Request: the auth middleware handles HTTP, and the resolver
// runs on already-verified inputs. This makes the resolver trivial
// to test against in-memory fixtures and reusable from non-HTTP
// surfaces (CLI tooling, background jobs that need a tenant +
// actor pair).
type ActorResolver interface {
	Resolve(ctx context.Context, id Identity) (actor.Actor, string, error)
}

// MapResolver is an in-memory ActorResolver suitable for tests and
// for the API server's stub auth provider. It looks up identities
// by Subject and returns the pre-registered actor + tenant pair.
//
// MapResolver is not a security boundary; it accepts any subject
// the test or stub registered, and returns ErrUnknownIdentity for
// anything else. Production deployments use ClerkActorResolver
// (slice 2) and APIKeyActorResolver (slice 3) instead.
type MapResolver struct {
	mu    sync.RWMutex
	pairs map[string]resolvedIdentity
}

type resolvedIdentity struct {
	Actor  actor.Actor
	Tenant string
}

// NewMapResolver returns an empty MapResolver. Register subjects
// with Set before resolving.
func NewMapResolver() *MapResolver {
	return &MapResolver{pairs: make(map[string]resolvedIdentity)}
}

// Set registers (or replaces) the actor + tenant pair for a given
// subject. Subject is the same identifier the auth layer puts on
// Identity.Subject (OIDC sub or API key id). Returns the previous
// pair when a subject is replaced; the second return is true when
// a previous pair existed.
func (r *MapResolver) Set(subject string, a actor.Actor, tenantID string) (actor.Actor, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	prev, existed := r.pairs[subject]
	r.pairs[subject] = resolvedIdentity{Actor: a, Tenant: tenantID}
	return prev.Actor, existed
}

// Delete removes the registration for a given subject. Returns true
// when a pair was removed.
func (r *MapResolver) Delete(subject string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.pairs[subject]; !ok {
		return false
	}
	delete(r.pairs, subject)
	return true
}

// Resolve implements ActorResolver. It returns ErrUnknownIdentity
// when no registration exists for the identity's subject. The
// returned actor is a copy: callers may safely mutate the result
// without affecting the resolver's state.
//
// When the identity carries a wallet link, the resolver attaches
// the address to the actor's display name as " (0x…)" so audit
// records carry attribution out of the box. Actor.ID is unchanged
// because the framework treats it as a stable identifier.
func (r *MapResolver) Resolve(_ context.Context, id Identity) (actor.Actor, string, error) {
	if err := id.Validate(); err != nil {
		return actor.Actor{}, "", fmt.Errorf("auth: invalid identity: %w", err)
	}
	r.mu.RLock()
	pair, ok := r.pairs[id.Subject]
	r.mu.RUnlock()
	if !ok {
		return actor.Actor{}, "", ErrUnknownIdentity
	}
	resolved := pair.Actor
	resolved.Roles = append([]string(nil), pair.Actor.Roles...)
	if id.Wallet != nil {
		resolved.DisplayName = annotateDisplay(resolved.DisplayName, id.Wallet.Address)
	}
	return resolved, pair.Tenant, nil
}

// annotateDisplay returns the display name with the wallet's
// shortened address appended. Used to surface wallet attribution on
// the actor without changing actor.ID. Pure function, exported only
// to the package so tests can verify the format without depending on
// the Resolve flow.
func annotateDisplay(display, address string) string {
	if address == "" {
		return display
	}
	short := address
	if len(short) > 10 {
		short = short[:6] + "…" + short[len(short)-4:]
	}
	if display == "" {
		return short
	}
	return display + " (" + short + ")"
}
