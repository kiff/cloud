package domain_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kiff/cloud/api/internal/domain"
	"github.com/kiff/cloud/api/internal/tenant"
)

// oldTenantDomainsDDL is the pre-#612 single-domain shape: tenant_id
// is the whole primary key, no domain_id / slug / is_default. The
// migration test recreates it to prove migrateTenantDomainsComposite
// brings a real existing database forward.
const oldTenantDomainsDDL = `
CREATE TABLE cloud_meta.tenant_domains (
    tenant_id   TEXT        PRIMARY KEY REFERENCES cloud_meta.tenants(id) ON DELETE CASCADE,
    yaml        BYTEA       NOT NULL,
    version     INT         NOT NULL DEFAULT 1,
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);`

// seedActiveTenant creates an active tenant and returns its id.
func seedActiveTenant(t *testing.T, pool *pgxpool.Pool, slug string) string {
	t.Helper()
	store := tenant.NewPostgresStore(pool)
	tt := tenant.Tenant{ID: tenant.NewID(), Slug: slug, DisplayName: slug, Status: tenant.StatusProvisioning}
	if err := store.Create(context.Background(), tt); err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	now := time.Now().UTC()
	if err := store.SetStatus(context.Background(), tt.ID, tenant.StatusActive, &now); err != nil {
		t.Fatalf("activate tenant: %v", err)
	}
	return tt.ID
}

// TestMigrateTenantDomainsComposite drives the full old→new migration
// against a real Postgres: an existing single-domain table with a
// legacy row is migrated to the composite (tenant_id, domain_id)
// shape, the legacy row's identity is backfilled from its YAML and
// marked default, the primary key becomes composite, and the
// one-default guard holds. Re-running the migration is a no-op.
func TestMigrateTenantDomainsComposite(t *testing.T) {
	pool := requirePostgres(t)
	ctx := context.Background()

	// Start from the legacy shape: drop the composite table
	// requirePostgres created and recreate the pre-#612 one.
	truncate(t, pool)
	if _, err := pool.Exec(ctx, "DROP TABLE IF EXISTS cloud_meta.tenant_domains CASCADE"); err != nil {
		t.Fatalf("drop table: %v", err)
	}
	if _, err := pool.Exec(ctx, oldTenantDomainsDDL); err != nil {
		t.Fatalf("create old table: %v", err)
	}
	defer func() {
		// Leave the DB in the migrated shape for any subsequent test.
		_, _ = pool.Exec(context.Background(), "DROP TABLE IF EXISTS cloud_meta.tenant_domains CASCADE")
		_ = domain.ApplyTenantDomainsSchema(context.Background(), pool)
		truncate(t, pool)
	}()

	tenantID := seedActiveTenant(t, pool, "migrate-test")
	if _, err := pool.Exec(ctx,
		"INSERT INTO cloud_meta.tenant_domains (tenant_id, yaml, version) VALUES ($1, $2, 3)",
		tenantID, []byte("domain: Refund Flow\nentity: Order\n")); err != nil {
		t.Fatalf("insert legacy row: %v", err)
	}

	// Migrate.
	if err := domain.ApplyTenantDomainsSchema(ctx, pool); err != nil {
		t.Fatalf("ApplyTenantDomainsSchema (migrate): %v", err)
	}

	// The legacy row gained its identity and became the default; the
	// version is preserved.
	var (
		domainID  string
		slug      string
		isDefault bool
		version   int
	)
	if err := pool.QueryRow(ctx,
		"SELECT domain_id, slug, is_default, version FROM cloud_meta.tenant_domains WHERE tenant_id = $1",
		tenantID).Scan(&domainID, &slug, &isDefault, &version); err != nil {
		t.Fatalf("read migrated row: %v", err)
	}
	if domainID != "refund-flow" || slug != "refund-flow" {
		t.Errorf("backfilled identity = (%q, %q), want (refund-flow, refund-flow)", domainID, slug)
	}
	if !isDefault {
		t.Error("migrated row should be the tenant's default")
	}
	if version != 3 {
		t.Errorf("version = %d, want 3 (preserved)", version)
	}

	// The primary key is now composite: a second domain for the same
	// tenant inserts cleanly.
	if _, err := pool.Exec(ctx,
		"INSERT INTO cloud_meta.tenant_domains (tenant_id, domain_id, slug, yaml, is_default) VALUES ($1, 'payouts', 'payouts', $2, FALSE)",
		tenantID, []byte("domain: payouts\n")); err != nil {
		t.Fatalf("insert second domain (composite PK): %v", err)
	}

	// The one-default guard rejects a second default for the tenant.
	if _, err := pool.Exec(ctx,
		"INSERT INTO cloud_meta.tenant_domains (tenant_id, domain_id, slug, yaml, is_default) VALUES ($1, 'second-default', 'second-default', $2, TRUE)",
		tenantID, []byte("domain: second\n")); err == nil {
		t.Error("expected unique-violation inserting a second default domain, got nil")
	}

	// Idempotent: re-running the migration changes nothing.
	if err := domain.ApplyTenantDomainsSchema(ctx, pool); err != nil {
		t.Fatalf("ApplyTenantDomainsSchema (re-run): %v", err)
	}
	var after string
	if err := pool.QueryRow(ctx,
		"SELECT domain_id FROM cloud_meta.tenant_domains WHERE tenant_id = $1 AND is_default",
		tenantID).Scan(&after); err != nil {
		t.Fatalf("read after re-run: %v", err)
	}
	if after != "refund-flow" {
		t.Errorf("domain_id after re-run = %q, want refund-flow", after)
	}
}

// TestPostgresSourceDefaultDomainUpsert confirms the Source contract
// on the composite table: Save upserts the tenant's single default
// domain in place — version bumps, slug follows a rename, domain_id
// stays immutable, and the tenant never accumulates duplicate rows.
func TestPostgresSourceDefaultDomainUpsert(t *testing.T) {
	pool := requirePostgres(t)
	ctx := context.Background()
	truncate(t, pool)
	defer truncate(t, pool)

	tenantID := seedActiveTenant(t, pool, "upsert-test")
	src := domain.NewPostgresSource(pool)

	if err := src.Save(ctx, tenantID, []byte("domain: Refunds\nentity: Order\n")); err != nil {
		t.Fatalf("Save 1: %v", err)
	}
	if err := src.Save(ctx, tenantID, []byte("domain: Refunds V2\nentity: Order\n")); err != nil {
		t.Fatalf("Save 2 (rename): %v", err)
	}

	td, err := src.Load(ctx, tenantID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if td.DomainID != "refunds" {
		t.Errorf("DomainID = %q, want refunds (immutable across rename)", td.DomainID)
	}
	if td.Slug != "refunds-v2" {
		t.Errorf("Slug = %q, want refunds-v2 (follows rename)", td.Slug)
	}
	if td.Version != 2 {
		t.Errorf("Version = %d, want 2", td.Version)
	}

	bySlug, err := src.LoadByDomain(ctx, tenantID, "refunds-v2")
	if err != nil {
		t.Fatalf("LoadByDomain by renamed slug: %v", err)
	}
	if bySlug.DomainID != "refunds" || bySlug.Slug != "refunds-v2" {
		t.Errorf("renamed slug resolved %+v, want immutable id refunds and slug refunds-v2", bySlug)
	}

	var rowCount int
	if err := pool.QueryRow(ctx,
		"SELECT COUNT(*) FROM cloud_meta.tenant_domains WHERE tenant_id = $1", tenantID).Scan(&rowCount); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if rowCount != 1 {
		t.Errorf("row count = %d, want 1 (upsert in place, no duplicate)", rowCount)
	}
}
