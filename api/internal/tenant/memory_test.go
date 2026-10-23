package tenant_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/tenant"
)

func sampleTenant(slug string) tenant.Tenant {
	return tenant.Tenant{
		ID:          tenant.NewID(),
		Slug:        slug,
		DisplayName: "Test " + slug,
		Status:      tenant.StatusProvisioning,
	}
}

func TestMemoryStoreCreateAndGet(t *testing.T) {
	t.Parallel()

	s := tenant.NewMemoryStore()
	tt := sampleTenant("acme")

	if err := s.Create(context.Background(), tt); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := s.Get(context.Background(), tt.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Slug != "acme" {
		t.Fatalf("slug: got %q, want acme", got.Slug)
	}

	bySlug, err := s.GetBySlug(context.Background(), "acme")
	if err != nil {
		t.Fatalf("GetBySlug: %v", err)
	}
	if bySlug.ID != tt.ID {
		t.Fatalf("id: got %q, want %q", bySlug.ID, tt.ID)
	}
}

func TestMemoryStoreDuplicateSlug(t *testing.T) {
	t.Parallel()

	s := tenant.NewMemoryStore()
	if err := s.Create(context.Background(), sampleTenant("acme")); err != nil {
		t.Fatalf("first create: %v", err)
	}
	if err := s.Create(context.Background(), sampleTenant("acme")); !errors.Is(err, tenant.ErrDuplicateSlug) {
		t.Fatalf("expected ErrDuplicateSlug, got %v", err)
	}
}

func TestMemoryStoreSetStatus(t *testing.T) {
	t.Parallel()

	s := tenant.NewMemoryStore()
	tt := sampleTenant("acme")
	if err := s.Create(context.Background(), tt); err != nil {
		t.Fatalf("Create: %v", err)
	}

	now := time.Now().UTC()
	if err := s.SetStatus(context.Background(), tt.ID, tenant.StatusActive, &now); err != nil {
		t.Fatalf("SetStatus: %v", err)
	}

	got, err := s.Get(context.Background(), tt.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != tenant.StatusActive {
		t.Fatalf("status: got %q, want active", got.Status)
	}
	if got.ProvisionedAt == nil || !got.ProvisionedAt.Equal(now) {
		t.Fatalf("provisioned_at: got %v, want %v", got.ProvisionedAt, now)
	}
}

func TestMemoryStoreList(t *testing.T) {
	t.Parallel()

	s := tenant.NewMemoryStore()
	a := sampleTenant("a")
	b := sampleTenant("b")
	b.Status = tenant.StatusActive
	for _, tt := range []tenant.Tenant{a, b} {
		if err := s.Create(context.Background(), tt); err != nil {
			t.Fatalf("create %s: %v", tt.Slug, err)
		}
	}

	all, err := s.List(context.Background(), tenant.ListFilter{})
	if err != nil {
		t.Fatalf("List all: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("len all: got %d, want 2", len(all))
	}

	active, err := s.List(context.Background(), tenant.ListFilter{Status: tenant.StatusActive})
	if err != nil {
		t.Fatalf("List active: %v", err)
	}
	if len(active) != 1 || active[0].Slug != "b" {
		t.Fatalf("active: got %+v, want only b", active)
	}
}

// TestMemoryStoreSetMetadata covers the operator-write path the
// kiff-tenant CLI uses for plan flips. The store
// must merge keys (preserving unrelated metadata), tolerate a
// nil starting map, delete the key on a nil value, and return
// ErrNotFound for an unknown tenant.
func TestMemoryStoreSetMetadata(t *testing.T) {
	t.Parallel()
	store := tenant.NewMemoryStore()
	ctx := context.Background()

	tt := sampleTenant("acme")
	tt.Status = tenant.StatusActive
	if err := store.Create(ctx, tt); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// 1) Set a key on a tenant that has no metadata yet.
	if err := store.SetMetadata(ctx, tt.ID, "plan", "builder"); err != nil {
		t.Fatalf("SetMetadata: %v", err)
	}
	got, err := store.Get(ctx, tt.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Metadata["plan"] != "builder" {
		t.Errorf("plan = %v, want builder", got.Metadata["plan"])
	}

	// 2) Set an unrelated key. The "plan" must survive.
	if err := store.SetMetadata(ctx, tt.ID, "billing_owner", "ops@example.com"); err != nil {
		t.Fatalf("SetMetadata billing_owner: %v", err)
	}
	got, _ = store.Get(ctx, tt.ID)
	if got.Metadata["plan"] != "builder" {
		t.Errorf("plan was clobbered: %v", got.Metadata)
	}
	if got.Metadata["billing_owner"] != "ops@example.com" {
		t.Errorf("billing_owner = %v", got.Metadata["billing_owner"])
	}

	// 3) Update an existing key.
	if err := store.SetMetadata(ctx, tt.ID, "plan", "agency_team"); err != nil {
		t.Fatalf("SetMetadata update: %v", err)
	}
	got, _ = store.Get(ctx, tt.ID)
	if got.Metadata["plan"] != "agency_team" {
		t.Errorf("plan after update = %v, want agency_team", got.Metadata["plan"])
	}

	// 4) Delete a key by passing nil.
	if err := store.SetMetadata(ctx, tt.ID, "billing_owner", nil); err != nil {
		t.Fatalf("SetMetadata delete: %v", err)
	}
	got, _ = store.Get(ctx, tt.ID)
	if _, ok := got.Metadata["billing_owner"]; ok {
		t.Errorf("billing_owner should be deleted, still present: %v", got.Metadata)
	}
	if got.Metadata["plan"] != "agency_team" {
		t.Errorf("plan was clobbered by sibling delete: %v", got.Metadata)
	}

	// 5) Unknown tenant returns ErrNotFound.
	if err := store.SetMetadata(ctx, tenant.NewID(), "plan", "builder"); !errors.Is(err, tenant.ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}
