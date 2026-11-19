package usage_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kiff/cloud/api/internal/pgschema"
	"github.com/kiff/cloud/api/internal/tenant"
	"github.com/kiff/cloud/api/internal/usage"
)

// oldTenantUsageDDL is the earlier shape: 4-column primary key, no
// domain_id. The migration test recreates it to prove ApplySchema
// migrates a real existing table forward.
const oldTenantUsageDDL = `
CREATE TABLE cloud_meta.tenant_usage (
    tenant_id     TEXT        NOT NULL REFERENCES cloud_meta.tenants(id) ON DELETE CASCADE,
    counter_name  TEXT        NOT NULL,
    entity_type   TEXT        NOT NULL,
    hour_bucket   TIMESTAMPTZ NOT NULL,
    count         BIGINT      NOT NULL DEFAULT 0,
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (tenant_id, counter_name, entity_type, hour_bucket)
);`

func seedUsageTenant(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	store := tenant.NewPostgresStore(pool)
	tt := tenant.Tenant{ID: tenant.NewID(), Slug: "u-" + tenant.NewID(), DisplayName: "usage", Status: tenant.StatusActive}
	if err := store.Create(context.Background(), tt); err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	return tt.ID
}

// TestMigrateTenantUsageDomain drives the old→new migration against a
// real Postgres: an existing 4-column-keyed tenant_usage gains the
// domain_id column and a 5-column primary key, an existing row keeps
// its data with domain_id ”, and the table then accepts two rows
// differing only by domain_id. Re-running is a no-op.
func TestMigrateTenantUsageDomain(t *testing.T) {
	pool := requirePostgres(t)
	ctx := context.Background()
	resetCloudMeta(t, pool)

	// Recreate the legacy shape.
	if _, err := pool.Exec(ctx, "DROP TABLE IF EXISTS cloud_meta.tenant_usage CASCADE"); err != nil {
		t.Fatalf("drop: %v", err)
	}
	// The recorded schema no longer matches the table: run it again.
	if err := pgschema.Forget(ctx, pool, "usage/usage"); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, oldTenantUsageDDL); err != nil {
		t.Fatalf("create old table: %v", err)
	}
	defer func() {
		_, _ = pool.Exec(context.Background(), "DROP TABLE IF EXISTS cloud_meta.tenant_usage CASCADE")
		_ = pgschema.Forget(context.Background(), pool, "usage/usage")
		_ = usage.ApplySchema(context.Background(), pool)
		resetCloudMeta(t, pool)
	}()

	tenantID := seedUsageTenant(t, pool)
	hour := usage.HourBucket(time.Now())
	if _, err := pool.Exec(ctx,
		"INSERT INTO cloud_meta.tenant_usage (tenant_id, counter_name, entity_type, hour_bucket, count) VALUES ($1,'kiff.cloud.proposals.submitted','proposal',$2,5)",
		tenantID, hour); err != nil {
		t.Fatalf("insert legacy row: %v", err)
	}

	// Migrate.
	if err := usage.ApplySchema(ctx, pool); err != nil {
		t.Fatalf("ApplySchema (migrate): %v", err)
	}

	// Legacy row preserved with empty domain_id.
	var domainID string
	var count int64
	if err := pool.QueryRow(ctx,
		"SELECT domain_id, count FROM cloud_meta.tenant_usage WHERE tenant_id=$1", tenantID).Scan(&domainID, &count); err != nil {
		t.Fatalf("read migrated row: %v", err)
	}
	if domainID != "" || count != 5 {
		t.Errorf("migrated row = (domain_id %q, count %d), want ('', 5)", domainID, count)
	}

	// The 5-column key now lets the same counter exist per domain.
	store := usage.NewPostgresStore(pool)
	if err := store.Flush(ctx, []usage.Counter{
		{TenantID: tenantID, CounterName: "kiff.cloud.proposals.submitted", EntityType: "proposal", DomainID: "refunds", HourBucket: hour, Count: 3},
		{TenantID: tenantID, CounterName: "kiff.cloud.proposals.submitted", EntityType: "proposal", DomainID: "payouts", HourBucket: hour, Count: 9},
	}); err != nil {
		t.Fatalf("flush per-domain rows: %v", err)
	}

	refunds, err := store.Snapshot(ctx, usage.Filter{TenantID: tenantID, DomainID: "refunds"})
	if err != nil {
		t.Fatalf("snapshot refunds: %v", err)
	}
	if len(refunds) != 1 || refunds[0].Count != 3 {
		t.Errorf("refunds snapshot = %+v, want one row count 3", refunds)
	}

	// Idempotent re-run.
	if err := usage.ApplySchema(ctx, pool); err != nil {
		t.Fatalf("ApplySchema (re-run): %v", err)
	}
}
