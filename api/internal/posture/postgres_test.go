package posture_test

import (
	"context"
	"github.com/kiff/cloud/api/internal/testdb"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kiff/cloud/api/internal/posture"
	"github.com/kiff/cloud/api/internal/tenant"
)

// requirePostgres gates the suite on KIFF_TEST_POSTGRES_URL and applies
// cloud_meta. Per-tenant kiff_audit schemas are created by provisioning
// (see provisionTenant). Without the env var the test skips cleanly.
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
	t.Cleanup(pool.Close)
	return pool
}

func resetCloudMeta(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	// Drop per-tenant schemas then truncate cloud_meta, mirroring the
	// proposals integration reset.
	rows, err := pool.Query(context.Background(),
		`SELECT schema_name FROM information_schema.schemata WHERE schema_name LIKE 'kiff_t_%'`)
	if err != nil {
		t.Fatalf("list schemas: %v", err)
	}
	var schemas []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			rows.Close()
			t.Fatalf("scan schema: %v", err)
		}
		schemas = append(schemas, s)
	}
	rows.Close()
	for _, s := range schemas {
		if _, err := pool.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+s+" CASCADE"); err != nil {
			t.Fatalf("drop schema %s: %v", s, err)
		}
	}
	if _, err := pool.Exec(context.Background(), "TRUNCATE cloud_meta.tenants CASCADE"); err != nil {
		t.Fatalf("truncate: %v", err)
	}
}

// provisionTenant creates a tenant row and applies its framework schema
// (which includes kiff_audit), returning the tenant.
func provisionTenant(t *testing.T, pool *pgxpool.Pool, slug string) tenant.Tenant {
	t.Helper()
	store := tenant.NewPostgresStore(pool)
	prov := tenant.NewProvisioner(pool, store)
	tt := tenant.Tenant{
		ID:          tenant.NewID(),
		Slug:        slug,
		DisplayName: "posture " + slug,
		Status:      tenant.StatusProvisioning,
	}
	if err := store.Create(context.Background(), tt); err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	if err := prov.Provision(context.Background(), tt.ID); err != nil {
		t.Fatalf("provision tenant: %v", err)
	}
	return tt
}

// TestChangeMode_FlipsAndRecords is the G5 acceptance case: a
// real transition atomically flips the tenant's mode (cloud_meta) and
// records a tamper-evident posture_changed row (kiff_audit), readable
// by the PostgresReader oldest-first with from/to/changed_by intact.
// Gated on KIFF_TEST_POSTGRES_URL.
func TestChangeMode_FlipsAndRecords(t *testing.T) {
	pool := requirePostgres(t)
	resetCloudMeta(t, pool)
	defer resetCloudMeta(t, pool)
	tt := provisionTenant(t, pool, "flip")

	store := tenant.NewPostgresStore(pool)
	schema := tenant.NewSchemaPool(pool)
	reader := posture.NewPostgresReader(schema)
	ctx := context.Background()

	// Default posture is enforce. First flip: enforce -> observe.
	at1 := time.Date(2026, time.May, 24, 12, 0, 0, 0, time.UTC)
	tr, changed, err := posture.ChangeMode(ctx, schema, tt.ID,
		tenant.ModeEnforce, tenant.ModeObserve, "ops-alice", func() time.Time { return at1 })
	if err != nil {
		t.Fatalf("ChangeMode observe: %v", err)
	}
	if !changed || tr.To != "observe" {
		t.Fatalf("first flip: changed=%v tr=%+v", changed, tr)
	}
	// The flip landed in tenant metadata atomically with the record.
	cur, _ := store.Get(ctx, tt.ID)
	if got := tenant.ModeFor(cur); got != tenant.ModeObserve {
		t.Fatalf("mode after flip = %q, want observe", got)
	}

	// Second flip: observe -> enforce.
	at2 := at1.Add(time.Hour)
	if _, changed, err := posture.ChangeMode(ctx, schema, tt.ID,
		tenant.ModeObserve, tenant.ModeEnforce, "ops-bob", func() time.Time { return at2 }); err != nil || !changed {
		t.Fatalf("second flip: changed=%v err=%v", changed, err)
	}
	cur, _ = store.Get(ctx, tt.ID)
	if got := tenant.ModeFor(cur); got != tenant.ModeEnforce {
		t.Fatalf("mode after second flip = %q, want enforce", got)
	}

	// Read the history back: two transitions, oldest-first.
	trs, err := reader.Transitions(ctx, tt.ID)
	if err != nil {
		t.Fatalf("Transitions: %v", err)
	}
	if len(trs) != 2 {
		t.Fatalf("got %d transitions, want 2", len(trs))
	}
	if trs[0].From != "enforce" || trs[0].To != "observe" || trs[0].ChangedBy != "ops-alice" {
		t.Errorf("transition[0] = %+v, want enforce->observe by ops-alice", trs[0])
	}
	if trs[1].From != "observe" || trs[1].To != "enforce" || trs[1].ChangedBy != "ops-bob" {
		t.Errorf("transition[1] = %+v, want observe->enforce by ops-bob", trs[1])
	}
	if !trs[0].ChangedAt.Equal(at1) || !trs[1].ChangedAt.Equal(at2) {
		t.Errorf("timestamps not preserved: %v, %v", trs[0].ChangedAt, trs[1].ChangedAt)
	}

	// History helpers over the read-back.
	h := posture.History{Current: string(tenant.ModeEnforce), Transitions: trs}
	be, ok := h.LastEnforce()
	if !ok || be.ChangedBy != "ops-bob" || !be.ChangedAt.Equal(at2) {
		t.Errorf("LastEnforce = %+v (ok=%v), want ops-bob @ at2", be, ok)
	}
}

// TestChangeMode_NoopWhenUnchanged confirms from==to writes nothing and
// reports changed=false.
func TestChangeMode_NoopWhenUnchanged(t *testing.T) {
	pool := requirePostgres(t)
	resetCloudMeta(t, pool)
	defer resetCloudMeta(t, pool)
	tt := provisionTenant(t, pool, "noop")

	schema := tenant.NewSchemaPool(pool)
	ctx := context.Background()

	tr, changed, err := posture.ChangeMode(ctx, schema, tt.ID,
		tenant.ModeEnforce, tenant.ModeEnforce, "ops", nil)
	if err != nil {
		t.Fatalf("ChangeMode noop: %v", err)
	}
	if changed || tr != (posture.Transition{}) {
		t.Fatalf("noop reported changed=%v tr=%+v", changed, tr)
	}

	trs, err := posture.NewPostgresReader(schema).Transitions(ctx, tt.ID)
	if err != nil {
		t.Fatalf("Transitions: %v", err)
	}
	if len(trs) != 0 {
		t.Fatalf("noop wrote %d records, want 0", len(trs))
	}
}

// TestChangeMode_UnknownTenant returns ErrNotFound and writes nothing.
func TestChangeMode_UnknownTenant(t *testing.T) {
	pool := requirePostgres(t)
	resetCloudMeta(t, pool)
	defer resetCloudMeta(t, pool)
	// A well-formed but unprovisioned tenant id: the schema must exist
	// for the audit insert, so provision a throwaway tenant and use a
	// DIFFERENT id that has no row. Simplest: provision one, then target
	// its id after deleting the row is overkill — instead target a valid
	// ULID with a provisioned schema but no cloud_meta row is impossible.
	// We assert the cloud_meta UPDATE-affected-zero path by provisioning
	// the schema then truncating only the tenants row.
	tt := provisionTenant(t, pool, "ghost")
	if _, err := pool.Exec(context.Background(), "DELETE FROM cloud_meta.tenants WHERE id = $1", tt.ID); err != nil {
		t.Fatalf("delete tenant row: %v", err)
	}

	schema := tenant.NewSchemaPool(pool)
	_, changed, err := posture.ChangeMode(context.Background(), schema, tt.ID,
		tenant.ModeEnforce, tenant.ModeObserve, "ops", nil)
	if err == nil {
		t.Fatal("expected an error for a missing tenant row")
	}
	if changed {
		t.Error("changed=true on a failed flip")
	}
	// The transaction rolled back, so no posture record persisted.
	trs, _ := posture.NewPostgresReader(schema).Transitions(context.Background(), tt.ID)
	if len(trs) != 0 {
		t.Errorf("rolled-back flip left %d records", len(trs))
	}
}
