package usage_test

import (
	"context"
	"github.com/kiff/cloud/api/internal/testdb"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kiff/cloud/api/internal/tenant"
	"github.com/kiff/cloud/api/internal/usage"
)

func requirePostgres(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := testdb.URL(t)
	if url == "" {
		t.Skip("KIFF_TEST_POSTGRES_URL not set; skipping Postgres-backed test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("ping: %v", err)
	}
	if err := tenant.ApplyCloudMeta(ctx, pool); err != nil {
		t.Fatalf("ApplyCloudMeta: %v", err)
	}
	if err := usage.ApplySchema(ctx, pool); err != nil {
		t.Fatalf("usage.ApplySchema: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// resetCloudMeta clears the cloud_meta tables. tenant_usage rows go
// away through the cascade on cloud_meta.tenants.
func resetCloudMeta(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	if _, err := pool.Exec(context.Background(),
		"TRUNCATE cloud_meta.tenants CASCADE"); err != nil {
		t.Fatalf("truncate: %v", err)
	}
}

func TestPostgresStoreFlushAndSnapshot(t *testing.T) {
	pool := requirePostgres(t)
	resetCloudMeta(t, pool)
	defer resetCloudMeta(t, pool)

	store := tenant.NewPostgresStore(pool)
	tt := tenant.Tenant{
		ID:          tenant.NewID(),
		Slug:        "usage-pg",
		DisplayName: "usage",
		Status:      tenant.StatusProvisioning,
	}
	if err := store.Create(context.Background(), tt); err != nil {
		t.Fatalf("create tenant: %v", err)
	}

	usageStore := usage.NewPostgresStore(pool)
	hour := time.Date(2026, 5, 24, 10, 0, 0, 0, time.UTC)
	counters := []usage.Counter{
		{TenantID: tt.ID, CounterName: "kiff.events.ingested", EntityType: "Order", HourBucket: hour, Count: 5},
		{TenantID: tt.ID, CounterName: "kiff.actions.executed", EntityType: "Order", HourBucket: hour, Count: 2},
	}
	if err := usageStore.Flush(context.Background(), counters); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	got, err := usageStore.Snapshot(context.Background(), usage.Filter{TenantID: tt.ID})
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("rows: got %d, want 2", len(got))
	}

	// Re-flush the same counters; the upsert should add to the
	// existing rows rather than insert duplicates.
	if err := usageStore.Flush(context.Background(), counters); err != nil {
		t.Fatalf("re-Flush: %v", err)
	}
	got, _ = usageStore.Snapshot(context.Background(), usage.Filter{TenantID: tt.ID})
	if len(got) != 2 {
		t.Fatalf("rows after re-flush: got %d, want 2 (no duplicates)", len(got))
	}
	for _, c := range got {
		switch c.CounterName {
		case "kiff.events.ingested":
			if c.Count != 10 {
				t.Errorf("events.ingested count: got %d, want 10", c.Count)
			}
		case "kiff.actions.executed":
			if c.Count != 4 {
				t.Errorf("actions.executed count: got %d, want 4", c.Count)
			}
		}
	}
}

func TestPostgresStoreSnapshotFiltersByCounter(t *testing.T) {
	pool := requirePostgres(t)
	resetCloudMeta(t, pool)
	defer resetCloudMeta(t, pool)

	store := tenant.NewPostgresStore(pool)
	tt := tenant.Tenant{
		ID:          tenant.NewID(),
		Slug:        "usage-filter",
		DisplayName: "usage",
		Status:      tenant.StatusProvisioning,
	}
	if err := store.Create(context.Background(), tt); err != nil {
		t.Fatalf("create tenant: %v", err)
	}

	usageStore := usage.NewPostgresStore(pool)
	hour := time.Date(2026, 5, 24, 10, 0, 0, 0, time.UTC)
	counters := []usage.Counter{
		{TenantID: tt.ID, CounterName: "kiff.events.ingested", EntityType: "Order", HourBucket: hour, Count: 5},
		{TenantID: tt.ID, CounterName: "kiff.actions.executed", EntityType: "Order", HourBucket: hour, Count: 2},
	}
	if err := usageStore.Flush(context.Background(), counters); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	got, err := usageStore.Snapshot(context.Background(), usage.Filter{
		TenantID:    tt.ID,
		CounterName: "kiff.events.ingested",
	})
	if err != nil {
		t.Fatalf("Snapshot filter: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("filtered rows: got %d, want 1", len(got))
	}
}
