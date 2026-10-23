package auth

import (
	"context"
	"errors"

	"github.com/kiff/kiff/pkg/kiff/actor"
)

// Context-key types are unexported single-value structs so callers
// cannot collide with the package's keys by accident. Same pattern
// used by tenant.WithTenant / tenant.From.
type (
	identityCtxKey struct{}
	actorCtxKey    struct{}
)

// ErrNoIdentity / ErrNoActor are returned by Identity / Actor when
// nothing has been attached to the context. Middleware (this slice)
// guarantees both for authenticated routes; helper functions exist
// for handlers that want to read the values without a fresh
// authentication step.
var (
	ErrNoIdentity = errors.New("auth: no Identity in context")
	ErrNoActor    = errors.New("auth: no actor.Actor in context")
)

// WithIdentity returns a derived context carrying id. The
// middleware calls this once per request, after the Provider has
// verified the inbound credential and the ActorResolver has
// resolved the tenant + actor.
func WithIdentity(ctx context.Context, id Identity) context.Context {
	return context.WithValue(ctx, identityCtxKey{}, id)
}

// deviceSignedCtxKey marks a request whose KIFF-Device-Signature the
// route policy verified (PhoneSignedRules).
type deviceSignedCtxKey struct{}

func withDeviceSigned(ctx context.Context) context.Context {
	return context.WithValue(ctx, deviceSignedCtxKey{}, true)
}

// DeviceSigned reports whether the route policy verified this request's
// KIFF-Device-Signature: the phone's key signed exactly this request,
// behind the device's unlock, within minutes, and it is used once. Only
// RoutePolicy.Wrap sets it.
func DeviceSigned(ctx context.Context) bool {
	v, _ := ctx.Value(deviceSignedCtxKey{}).(bool)
	return v
}

// IdentityFrom returns the Identity attached to ctx. ErrNoIdentity
// when none is present.
func IdentityFrom(ctx context.Context) (Identity, error) {
	id, ok := ctx.Value(identityCtxKey{}).(Identity)
	if !ok {
		return Identity{}, ErrNoIdentity
	}
	return id, nil
}

// MustIdentity is the panicking variant of IdentityFrom. Use only
// in handlers gated behind authenticating middleware.
func MustIdentity(ctx context.Context) Identity {
	id, err := IdentityFrom(ctx)
	if err != nil {
		panic(err)
	}
	return id
}

// WithActor returns a derived context carrying a. The middleware
// calls this once per request, alongside WithIdentity.
func WithActor(ctx context.Context, a actor.Actor) context.Context {
	return context.WithValue(ctx, actorCtxKey{}, a)
}

// ActorFrom returns the actor.Actor attached to ctx.
func ActorFrom(ctx context.Context) (actor.Actor, error) {
	a, ok := ctx.Value(actorCtxKey{}).(actor.Actor)
	if !ok {
		return actor.Actor{}, ErrNoActor
	}
	return a, nil
}

// MustActor is the panicking variant of ActorFrom.
func MustActor(ctx context.Context) actor.Actor {
	a, err := ActorFrom(ctx)
	if err != nil {
		panic(err)
	}
	return a
}
