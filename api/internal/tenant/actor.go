package tenant

import "context"

// ActorMapper maps an identifier coming out of the auth layer
// (Clerk subject, API key id, wallet address) to the tenant that
// owns it. The interface is declared here so that future code —
// the cloud-side MetricsRecorder and the auth-attached request
// pipeline — can depend on the shape without depending on an
// implementation that does not yet exist.
//
// At v0.1 there is no implementation. The auth layer
// chooses what to feed here once the Clerk vs Privy decision
// resolves. Until then, the mapping is intentionally absent: nothing
// produces actors yet, so nothing needs to map them to tenants.
type ActorMapper interface {
	// TenantForActor returns the tenant id the named actor belongs
	// to. The error semantics mirror Store.Get: ErrNotFound when
	// the actor is unknown, real errors otherwise.
	TenantForActor(ctx context.Context, actorID string) (string, error)
}
