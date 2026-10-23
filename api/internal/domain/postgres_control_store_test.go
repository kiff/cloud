package domain_test

import (
	"context"
	"testing"

	"github.com/kiff/cloud/api/internal/domain"
	"github.com/kiff/cloud/api/internal/tenant"
)

// TestPostgresControlStore_VersionMonotonic exercises the version SQL:
// COALESCE(MAX(version),0)+1 per (tenant, environment) inside
// the activation transaction, and the version column scan. Gated on
// KIFF_TEST_POSTGRES_URL.
func TestPostgresControlStore_VersionMonotonic(t *testing.T) {
	pool := requirePostgres(t)
	truncate(t, pool)
	defer truncate(t, pool)

	ctx := context.Background()
	if err := domain.ApplyTenantControlsSchema(ctx, pool); err != nil {
		t.Fatalf("ApplyTenantControlsSchema: %v", err)
	}

	tenants := tenant.NewPostgresStore(pool)
	tt := tenant.Tenant{ID: tenant.NewID(), Slug: "ctrl-ver", DisplayName: "ctrl-ver", Status: tenant.StatusActive}
	if err := tenants.Create(ctx, tt); err != nil {
		t.Fatalf("create tenant: %v", err)
	}

	store := domain.NewPostgresControlStore(pool)
	mk := func(id, env string) {
		if err := store.Create(ctx, domain.Control{
			ID: id, TenantID: tt.ID, Domain: "d", Entity: "E",
			Actions: []string{"A"}, Environment: env,
		}); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
	}
	mk("p1", "prod")
	mk("p2", "prod")
	mk("s1", "staging")

	list, err := store.List(ctx, tt.ID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	byID := map[string]domain.Control{}
	for _, c := range list {
		byID[c.ID] = c
	}
	if byID["p1"].Version != 1 || byID["p2"].Version != 2 {
		t.Errorf("prod versions: p1=%d p2=%d, want 1,2", byID["p1"].Version, byID["p2"].Version)
	}
	if byID["s1"].Version != 1 {
		t.Errorf("staging version: %d, want 1 (independent lineage)", byID["s1"].Version)
	}
	if byID["p1"].Status != "superseded" || byID["p2"].Status != "active" {
		t.Errorf("supersession: p1=%s p2=%s", byID["p1"].Status, byID["p2"].Status)
	}
	if byID["s1"].Status != "active" {
		t.Errorf("staging should stay active: %s", byID["s1"].Status)
	}
}

// TestPostgresControlStore_ConcurrentFirstActivation proves the
// one-active-per-env unique index + Create retry close the race where
// two racing FIRST activations would otherwise both insert version=1
// active. After both, exactly one active control
// remains and versions are distinct (1 and 2). Gated on
// KIFF_TEST_POSTGRES_URL.
func TestPostgresControlStore_ConcurrentFirstActivation(t *testing.T) {
	pool := requirePostgres(t)
	truncate(t, pool)
	defer truncate(t, pool)

	ctx := context.Background()
	if err := domain.ApplyTenantControlsSchema(ctx, pool); err != nil {
		t.Fatalf("ApplyTenantControlsSchema: %v", err)
	}
	tenants := tenant.NewPostgresStore(pool)
	tt := tenant.Tenant{ID: tenant.NewID(), Slug: "ctrl-race", DisplayName: "ctrl-race", Status: tenant.StatusActive}
	if err := tenants.Create(ctx, tt); err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	store := domain.NewPostgresControlStore(pool)

	// Two racing first-activations in the same (tenant, prod).
	const n = 2
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		go func(i int) {
			errs <- store.Create(ctx, domain.Control{
				ID: tenant.NewID(), TenantID: tt.ID, Domain: "d", Entity: "E",
				Actions: []string{"A"}, Environment: "prod",
			})
		}(i)
	}
	for i := 0; i < n; i++ {
		if err := <-errs; err != nil {
			t.Fatalf("concurrent create: %v", err)
		}
	}

	list, err := store.List(ctx, tt.ID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	active := 0
	versions := map[int]int{}
	for _, c := range list {
		if c.Environment != "prod" {
			continue
		}
		versions[c.Version]++
		if c.Status == "active" {
			active++
		}
	}
	if active != 1 {
		t.Errorf("active prod controls = %d, want exactly 1", active)
	}
	if versions[1] != 1 || versions[2] != 1 || len(list) != 2 {
		t.Errorf("versions not distinct 1,2: versions=%v total=%d", versions, len(list))
	}
}
