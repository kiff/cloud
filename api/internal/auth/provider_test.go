package auth

import (
	"context"
	"errors"
	"testing"

	"github.com/kiff/kiff/pkg/kiff/actor"
)

func TestStubProvider_VerifySession_Resolves(t *testing.T) {
	t.Parallel()
	backing := NewMapResolver()
	backing.Set("user_123", actor.Actor{ID: "u-123", Roles: []string{"operator"}}, "tenant-acme")
	p := NewStubProvider(backing)

	id, err := p.VerifySession(context.Background(), "Bearer user_123")
	if err != nil {
		t.Fatalf("VerifySession: %v", err)
	}
	if id.Subject != "user_123" || id.Method != MethodSession {
		t.Errorf("identity mismatch: %+v", id)
	}
	if !id.HasRole("operator") {
		t.Errorf("roles should propagate from MapResolver pair, got %v", id.Roles)
	}
}

func TestStubProvider_VerifySession_RejectsUnknownSubject(t *testing.T) {
	t.Parallel()
	p := NewStubProvider(NewMapResolver())

	_, err := p.VerifySession(context.Background(), "user_unknown")
	if !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("expected ErrInvalidToken for unknown subject, got %v", err)
	}
}

func TestStubProvider_VerifySession_RejectsEmptyToken(t *testing.T) {
	t.Parallel()
	p := NewStubProvider(NewMapResolver())

	for _, token := range []string{"", "   ", "Bearer ", "Bearer   "} {
		_, err := p.VerifySession(context.Background(), token)
		if !errors.Is(err, ErrInvalidToken) {
			t.Errorf("token %q: expected ErrInvalidToken, got %v", token, err)
		}
	}
}

func TestStubProvider_NoBackingResolver_RejectsAll(t *testing.T) {
	t.Parallel()
	p := NewStubProvider(nil)

	_, err := p.VerifySession(context.Background(), "any-token")
	if !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("expected ErrInvalidToken when no backing resolver, got %v", err)
	}
}

func TestClerkSessions_ImplementsProvider(t *testing.T) {
	t.Parallel()
	// Compile-time interface satisfaction; if this changes the
	// HTTP middleware (slice 3) breaks at compile.
	var _ Provider = (*ClerkSessions)(nil)
	var _ Provider = (*StubProvider)(nil)
}
