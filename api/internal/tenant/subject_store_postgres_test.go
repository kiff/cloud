package tenant_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/tenant"
)

// TestPostgresSubjectStore_CRUD exercises the production
// SubjectStore against the local Postgres set up by
// `make pg-up`. Skips when KIFF_TEST_POSTGRES_URL is unset.
func TestPostgresSubjectStore_CRUD(t *testing.T) {
	pool := requirePostgres(t)
	ctx := context.Background()

	if err := tenant.ApplySubjectTenantsSchema(ctx, pool); err != nil {
		t.Fatalf("ApplySubjectTenantsSchema: %v", err)
	}
	t.Cleanup(func() {
		// Drop the subject mapping but leave the tenants table
		// alone for other tests that share the pool.
		if _, err := pool.Exec(ctx, "TRUNCATE cloud_meta.subject_tenants"); err != nil {
			t.Logf("truncate subject_tenants: %v", err)
		}
	})

	// Need a real tenant for the FK to accept the row.
	tenantID := tenant.NewID()
	tenants := tenant.NewPostgresStore(pool)
	if err := tenants.Create(ctx, tenant.Tenant{
		ID:     tenantID,
		Slug:   "user-pg-test-" + tenantID[:8],
		Status: tenant.StatusActive,
	}); err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	t.Cleanup(func() {
		if _, err := pool.Exec(ctx, "DELETE FROM cloud_meta.subject_tenants WHERE tenant_id = $1", tenantID); err != nil {
			t.Logf("cleanup subject mapping: %v", err)
		}
		if _, err := pool.Exec(ctx, "DELETE FROM cloud_meta.tenants WHERE id = $1", tenantID); err != nil {
			t.Logf("cleanup tenant: %v", err)
		}
	})

	store := tenant.NewPostgresSubjectStore(pool)
	now := time.Now().UTC()

	// Get on missing subject returns ErrSubjectNotMapped.
	if _, err := store.Get(ctx, "subj-pg-1"); !errors.Is(err, tenant.ErrSubjectNotMapped) {
		t.Fatalf("Get on missing: err = %v, want ErrSubjectNotMapped", err)
	}

	// Set + Get round-trip.
	if err := store.Set(ctx, "subj-pg-1", tenantID, now); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, err := store.Get(ctx, "subj-pg-1")
	if err != nil {
		t.Fatalf("Get after Set: %v", err)
	}
	if got.TenantID != tenantID {
		t.Fatalf("TenantID = %q, want %q", got.TenantID, tenantID)
	}

	// Set with same tenant id is idempotent.
	if err := store.Set(ctx, "subj-pg-1", tenantID, now.Add(time.Hour)); err != nil {
		t.Fatalf("idempotent Set: %v", err)
	}

	// Set with different tenant id returns ErrSubjectAlreadyMapped.
	otherID := tenant.NewID()
	if err := tenants.Create(ctx, tenant.Tenant{
		ID:     otherID,
		Slug:   "user-pg-other-" + otherID[:8],
		Status: tenant.StatusActive,
	}); err != nil {
		t.Fatalf("create other tenant: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM cloud_meta.tenants WHERE id = $1", otherID)
	})
	if err := store.Set(ctx, "subj-pg-1", otherID, now); !errors.Is(err, tenant.ErrSubjectAlreadyMapped) {
		t.Fatalf("re-Set with different tenant: err = %v, want ErrSubjectAlreadyMapped", err)
	}
}

func TestPostgresSubjectStore_RejectsNonexistentTenantFK(t *testing.T) {
	pool := requirePostgres(t)
	ctx := context.Background()
	if err := tenant.ApplySubjectTenantsSchema(ctx, pool); err != nil {
		t.Fatalf("ApplySubjectTenantsSchema: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "TRUNCATE cloud_meta.subject_tenants")
	})

	store := tenant.NewPostgresSubjectStore(pool)
	err := store.Set(ctx, "subj-pg-fk", "nonexistent-tenant-id", time.Now().UTC())
	if err == nil {
		t.Fatalf("expected FK violation when inserting against unknown tenant_id")
	}
}
