package tenant

import (
	"context"
	"errors"
)

// ctxKey is unexported so callers cannot collide with the package's
// context key by accident. The single-value type also prevents the
// usual "string key" antipattern.
type ctxKey struct{}

// ErrNoTenant is returned by From when no tenant is present in the
// context. Callers (handlers, runtime resolvers) decide whether
// this is fatal or whether a fallback (the demo tenant, an admin
// scope) applies.
var ErrNoTenant = errors.New("tenant: no tenant in context")

// WithTenant returns a derived context carrying t. The middleware
// and any code that hand-resolves a tenant call this.
func WithTenant(ctx context.Context, t Tenant) context.Context {
	return context.WithValue(ctx, ctxKey{}, t)
}

// From returns the tenant attached to ctx. Returns ErrNoTenant when
// no tenant has been attached.
func From(ctx context.Context) (Tenant, error) {
	t, ok := ctx.Value(ctxKey{}).(Tenant)
	if !ok {
		return Tenant{}, ErrNoTenant
	}
	return t, nil
}

// MustFrom is the panicking variant of From. Use only in handlers
// that are gated behind middleware that guarantees a tenant; never
// in shared code that may be called outside a request.
func MustFrom(ctx context.Context) Tenant {
	t, err := From(ctx)
	if err != nil {
		panic(err)
	}
	return t
}
