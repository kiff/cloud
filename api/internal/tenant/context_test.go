package tenant_test

import (
	"context"
	"errors"
	"testing"

	"github.com/kiff/cloud/api/internal/tenant"
)

func TestContextRoundTrip(t *testing.T) {
	t.Parallel()

	tt := tenant.Tenant{ID: tenant.NewID(), Slug: "acme"}
	ctx := tenant.WithTenant(context.Background(), tt)

	got, err := tenant.From(ctx)
	if err != nil {
		t.Fatalf("From: %v", err)
	}
	if got.ID != tt.ID {
		t.Fatalf("id: got %q, want %q", got.ID, tt.ID)
	}
}

func TestFromMissingReturnsErrNoTenant(t *testing.T) {
	t.Parallel()

	if _, err := tenant.From(context.Background()); !errors.Is(err, tenant.ErrNoTenant) {
		t.Fatalf("err: got %v, want ErrNoTenant", err)
	}
}

func TestMustFromPanicsWithoutTenant(t *testing.T) {
	t.Parallel()

	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic")
		}
	}()
	_ = tenant.MustFrom(context.Background())
}
