package auth_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	kiffactor "github.com/kiff/kiff/pkg/kiff/actor"

	"github.com/kiff/cloud/api/internal/auth"
)

// fakeEnsurer is a stub auth.SubjectTenantEnsurer.
type fakeEnsurer struct {
	tenantID string
	err      error
	calls    int
}

func (f *fakeEnsurer) EnsureForSubject(_ context.Context, _ string) (string, error) {
	f.calls++
	if f.err != nil {
		return "", f.err
	}
	return f.tenantID, nil
}

func TestLazyResolver_ResolveStampsTenantFromEnsurer(t *testing.T) {
	t.Parallel()
	ensurer := &fakeEnsurer{tenantID: "ten-1"}
	r := auth.NewLazyResolver(ensurer)

	id := auth.Identity{
		Subject: "subj-1",
		Email:   "alice@example.com",
		Method:  auth.MethodSession,
	}
	a, tenantID, err := r.Resolve(context.Background(), id)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if tenantID != "ten-1" {
		t.Fatalf("tenantID = %q, want ten-1", tenantID)
	}
	if a.ID != "actor-subj-1" {
		t.Fatalf("actor ID = %q, want actor-subj-1", a.ID)
	}
	if a.DisplayName != "alice@example.com" {
		t.Fatalf("display name = %q, want alice@example.com", a.DisplayName)
	}
	if ensurer.calls != 1 {
		t.Fatalf("ensurer called %d times, want 1", ensurer.calls)
	}
}

func TestLazyResolver_AnnotatesWalletOnDisplayName(t *testing.T) {
	t.Parallel()
	ensurer := &fakeEnsurer{tenantID: "ten-1"}
	r := auth.NewLazyResolver(ensurer)

	wallet := &auth.WalletLink{
		Address:  "0xabcdef0123456789abcdef0123456789abcdef01",
		ChainID:  84532,
		LinkedAt: timeNow(),
	}
	id := auth.Identity{
		Subject: "subj-1",
		Email:   "alice@example.com",
		Method:  auth.MethodSession,
		Wallet:  wallet,
	}
	a, _, err := r.Resolve(context.Background(), id)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !strings.Contains(a.DisplayName, "0xabcd") {
		t.Fatalf("wallet shortform missing from display name: %q", a.DisplayName)
	}
}

func TestLazyResolver_FallsBackOnEnsurerError(t *testing.T) {
	t.Parallel()
	primary := &fakeEnsurer{err: errors.New("not provisionable")}
	fallback := auth.NewMapResolver()
	fallback.Set("svc-1", kiffactor.Actor{ID: "actor-svc-1", Type: kiffactor.TypeService}, "ten-svc")

	r := auth.NewLazyResolver(primary, auth.WithFallback(fallback))

	id := auth.Identity{Subject: "svc-1", Method: auth.MethodSession}
	a, tenantID, err := r.Resolve(context.Background(), id)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if tenantID != "ten-svc" {
		t.Fatalf("tenantID = %q, want ten-svc", tenantID)
	}
	if a.ID != "actor-svc-1" {
		t.Fatalf("actor ID = %q, want actor-svc-1", a.ID)
	}
}

func TestLazyResolver_NoFallbackBubblesError(t *testing.T) {
	t.Parallel()
	primary := &fakeEnsurer{err: errors.New("upstream broken")}
	r := auth.NewLazyResolver(primary)

	id := auth.Identity{Subject: "subj-1", Method: auth.MethodSession}
	if _, _, err := r.Resolve(context.Background(), id); err == nil {
		t.Fatalf("expected error to propagate")
	} else if !errors.Is(err, auth.ErrResolverUnavailable) {
		t.Fatalf("expected ErrResolverUnavailable, got %v", err)
	}
}

func TestLazyResolver_RejectsInvalidIdentity(t *testing.T) {
	t.Parallel()
	r := auth.NewLazyResolver(&fakeEnsurer{tenantID: "ten-1"})
	id := auth.Identity{} // missing subject + method
	if _, _, err := r.Resolve(context.Background(), id); err == nil {
		t.Fatalf("expected validation error")
	}
}

func TestLazyResolver_NilEnsurer_Errors(t *testing.T) {
	t.Parallel()
	r := auth.NewLazyResolver(nil)
	id := auth.Identity{Subject: "subj-1", Method: auth.MethodSession}
	if _, _, err := r.Resolve(context.Background(), id); err == nil {
		t.Fatalf("expected error on nil ensurer")
	}
}
