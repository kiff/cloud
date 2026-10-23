package auth

import (
	"context"
	"errors"
	"fmt"

	"github.com/kiff/kiff/pkg/kiff/actor"
)

// SubjectTenantEnsurer is the cloud-side primitive the lazy
// resolver depends on: turn an authenticated subject into a
// tenant id, provisioning one on first sign-in if necessary.
//
// Defined here (rather than imported from the tenant package) so
// the auth package keeps a clean dependency surface — the cloud
// runtime wires a concrete adapter at startup, and tests can stub
// the contract directly.
//
// The returned tenant id is the cloud's stable identifier; the
// resolver stamps it onto the request's Identity.Tenant.
type SubjectTenantEnsurer interface {
	EnsureForSubject(ctx context.Context, subject string) (string, error)
}

// LazyResolver is the production ActorResolver for the
// session/Clerk path. On every authenticated request it:
//
//  1. Calls SubjectTenantEnsurer.EnsureForSubject to look up (or
//     provision) the tenant for the identity's subject.
//  2. Constructs an actor.Actor from the verified Identity. The
//     framework treats actor.ID as a stable identifier; we use
//     the subject as the id and the Clerk-supplied display name
//     when present.
//
// Unlike MapResolver, LazyResolver has no in-memory state. The
// per-tenant runtime cache lives in runtime.TenantRuntime; the
// subject-to-tenant mapping lives in cloud_meta.subject_tenants.
// The resolver itself is a thin transformation layer.
//
// API-key paths bypass this resolver because API keys
// self-identify their tenant via the embedded prefix (see
// auth.Middleware.verifyAPIKey). LazyResolver only fires for
// session-method identities.
type LazyResolver struct {
	ensurer SubjectTenantEnsurer

	// fallback is consulted for subjects that EnsureForSubject
	// cannot place. v0.1 leaves it nil; if a deploy needs to mix
	// stub/fixture subjects with the lazy path, wiring a
	// MapResolver here keeps the fixture path working.
	fallback ActorResolver
}

// LazyResolverOption configures a LazyResolver.
type LazyResolverOption func(*LazyResolver)

// WithFallback wires a secondary resolver consulted only when the
// primary EnsureForSubject path returns ErrUnknownIdentity. Useful
// for mixed deployments where some subjects are fixture-only
// (e.g. service tokens) and some go through the auto-provision
// flow.
func WithFallback(r ActorResolver) LazyResolverOption {
	return func(l *LazyResolver) { l.fallback = r }
}

// NewLazyResolver constructs a LazyResolver. ensurer is required.
func NewLazyResolver(ensurer SubjectTenantEnsurer, opts ...LazyResolverOption) *LazyResolver {
	l := &LazyResolver{ensurer: ensurer}
	for _, opt := range opts {
		opt(l)
	}
	return l
}

// Resolve implements ActorResolver. Derives an actor.Actor from
// the verified Identity and resolves the tenant id via the
// SubjectTenantEnsurer.
//
// Error contract:
//   - Identity validation failure: returns the underlying error
//     wrapped, mapped to 401 by the middleware.
//   - Ensurer returns an error: the fallback is consulted. Unknown
//     identities remain authentication failures; backend failures are
//     wrapped with ErrResolverUnavailable so middleware returns 503.
//   - Successful path: returns (actor, tenantID, nil) with the
//     wallet attribution (when present on Identity) annotated
//     onto the actor's display name.
func (l *LazyResolver) Resolve(ctx context.Context, id Identity) (actor.Actor, string, error) {
	if l == nil || l.ensurer == nil {
		return actor.Actor{}, "", fmt.Errorf("%w: LazyResolver has no ensurer", ErrResolverUnavailable)
	}
	if err := id.Validate(); err != nil {
		return actor.Actor{}, "", fmt.Errorf("auth: invalid identity: %w", err)
	}

	tenantID, err := l.ensurer.EnsureForSubject(ctx, id.Subject)
	if err != nil {
		// Fallback for subjects the ensurer doesn't recognize
		// (typically fixture / service identities seeded into a
		// MapResolver alongside the lazy path).
		if l.fallback != nil {
			a, t, ferr := l.fallback.Resolve(ctx, id)
			if ferr == nil {
				return a, t, nil
			}
			if errors.Is(err, ErrUnknownIdentity) && errors.Is(ferr, ErrUnknownIdentity) {
				return actor.Actor{}, "", fmt.Errorf("ensure tenant for %q: %w", id.Subject, ErrUnknownIdentity)
			}
			return actor.Actor{}, "", fmt.Errorf("%w: ensure tenant for %q: %v (fallback: %v)", ErrResolverUnavailable, id.Subject, err, ferr)
		}
		if errors.Is(err, ErrUnknownIdentity) {
			return actor.Actor{}, "", fmt.Errorf("ensure tenant for %q: %w", id.Subject, err)
		}
		return actor.Actor{}, "", fmt.Errorf("%w: ensure tenant for %q: %v", ErrResolverUnavailable, id.Subject, err)
	}

	a := actor.Actor{
		ID:          "actor-" + id.Subject,
		Type:        actor.TypeHuman,
		DisplayName: defaultDisplayName(id),
		Roles:       append([]string(nil), id.Roles...),
	}
	if id.Wallet != nil {
		a.DisplayName = annotateDisplay(a.DisplayName, id.Wallet.Address)
	}
	return a, tenantID, nil
}

// defaultDisplayName picks a reasonable label for the actor. The
// auth layer prefers the email when Clerk supplied one (verified
// + accessible to operators looking at audit timelines), falling
// back to the subject id when not.
func defaultDisplayName(id Identity) string {
	if id.Email != "" {
		return id.Email
	}
	return id.Subject
}

// Compile-time interface satisfaction.
var _ ActorResolver = (*LazyResolver)(nil)
