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
// route policy verified (PhoneSignedRules), with which key signed it.
type deviceSignedCtxKey struct{}

// SignedWith says how the route policy found a phone's request signed
// (kiff/tafs#494): by which key (KeyDevice or KeyCritical) and, for a
// routine action inside an approval window, that window's id. A handler
// whose route is routine or critical by its content (a draft that widens
// a Card, "No" to "Did this happen?") reads it and refuses a device-signed
// request with critical_key_required.
type SignedWith struct {
	Key    string
	Window string
}

// Critical reports whether the approval key signed, behind the person's
// fingerprint or face.
func (s SignedWith) Critical() bool { return s.Key == KeyCritical }

func withDeviceSigned(ctx context.Context, with SignedWith) context.Context {
	return context.WithValue(ctx, deviceSignedCtxKey{}, with)
}

// DeviceSigned reports whether the route policy verified this request's
// KIFF-Device-Signature: the phone's key signed exactly this request,
// behind the device's unlock, within minutes, and it is used once. Only
// RoutePolicy.Wrap sets it.
func DeviceSigned(ctx context.Context) bool {
	_, ok := ctx.Value(deviceSignedCtxKey{}).(SignedWith)
	return ok
}

// SignedWithFrom is how the route policy found this request signed, and
// false when it verified no signature on it.
func SignedWithFrom(ctx context.Context) (SignedWith, bool) {
	v, ok := ctx.Value(deviceSignedCtxKey{}).(SignedWith)
	return v, ok
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
