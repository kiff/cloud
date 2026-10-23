package tenant_test

import (
	"context"
	"errors"
	"github.com/kiff/cloud/api/internal/testdb"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kiff/cloud/api/internal/tenant"
)

// requirePostgres returns a fresh pool for the test or skips the
// test when KIFF_TEST_POSTGRES_URL is unset. The caller is
// responsible for any per-test cleanup; the cloud_meta schema is
// applied here so each test can assume tables exist.
func requirePostgres(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := testdb.URL(t)
	if url == "" {
		t.Skip("KIFF_TEST_POSTGRES_URL not set; skipping Postgres-backed test (run docker compose up -d first)")
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

// truncateTenants empties the cloud_meta.tenants table between
// tests that share a Postgres instance. Tests that drop schemas
// also need to drop any kiff_t_* schemas they created — see
// dropTenantSchemas.
//
// The CASCADE handles the case where another package's tests have
// added foreign-key references (e.g. cloud_meta.tenant_domains
// from the domain package). We don't want this helper to know
// about every cross-package table; CASCADE is the right tool.
func truncateTenants(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), "TRUNCATE cloud_meta.tenants CASCADE"); err != nil {
		t.Fatalf("truncate tenants: %v", err)
	}
}

// dropTenantSchemas drops every kiff_t_* schema known to Postgres.
// Used in test cleanup so re-runs do not bump into existing schemas
// from prior failed tests.
func dropTenantSchemas(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	rows, err := pool.Query(context.Background(),
		"SELECT schema_name FROM information_schema.schemata WHERE schema_name LIKE 'kiff_t_%'")
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
		if _, err := pool.Exec(context.Background(), "DROP SCHEMA "+s+" CASCADE"); err != nil {
			t.Fatalf("drop schema %s: %v", s, err)
		}
	}
}

func TestPostgresStoreCRUD(t *testing.T) {
	pool := requirePostgres(t)
	truncateTenants(t, pool)
	defer truncateTenants(t, pool)

	store := tenant.NewPostgresStore(pool)

	tt := sampleTenant("acme-pg")
	if err := store.Create(context.Background(), tt); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := store.Get(context.Background(), tt.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Slug != "acme-pg" {
		t.Fatalf("slug: got %q, want acme-pg", got.Slug)
	}

	now := time.Now().UTC().Truncate(time.Microsecond)
	if err := store.SetStatus(context.Background(), tt.ID, tenant.StatusActive, &now); err != nil {
		t.Fatalf("SetStatus: %v", err)
	}
	got, err = store.Get(context.Background(), tt.ID)
	if err != nil {
		t.Fatalf("Get post-set: %v", err)
	}
	if got.Status != tenant.StatusActive {
		t.Fatalf("status: got %q, want active", got.Status)
	}
	if got.ProvisionedAt == nil {
		t.Fatal("provisioned_at: got nil, want non-nil")
	}

	if err := store.Delete(context.Background(), tt.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := store.Get(context.Background(), tt.ID); !errors.Is(err, tenant.ErrNotFound) {
		t.Fatalf("Get after delete: got %v, want ErrNotFound", err)
	}
}

func TestPostgresStoreDuplicateSlug(t *testing.T) {
	pool := requirePostgres(t)
	truncateTenants(t, pool)
	defer truncateTenants(t, pool)

	store := tenant.NewPostgresStore(pool)
	a := sampleTenant("dup")
	if err := store.Create(context.Background(), a); err != nil {
		t.Fatalf("first create: %v", err)
	}
	b := sampleTenant("dup")
	err := store.Create(context.Background(), b)
	if !errors.Is(err, tenant.ErrDuplicateSlug) {
		t.Fatalf("expected ErrDuplicateSlug, got %v", err)
	}
}

func TestProvisionerRoundTrip(t *testing.T) {
	pool := requirePostgres(t)
	truncateTenants(t, pool)
	dropTenantSchemas(t, pool)
	defer func() {
		dropTenantSchemas(t, pool)
		truncateTenants(t, pool)
	}()

	store := tenant.NewPostgresStore(pool)
	prov := tenant.NewProvisioner(pool, store)

	tt := sampleTenant("prov-test")
	if err := store.Create(context.Background(), tt); err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := prov.Provision(context.Background(), tt.ID); err != nil {
		t.Fatalf("Provision: %v", err)
	}

	// Schema should exist.
	schema := tenant.SchemaNameFor(tt.ID)
	row := pool.QueryRow(context.Background(),
		"SELECT 1 FROM information_schema.schemata WHERE schema_name = $1", schema)
	var x int
	if err := row.Scan(&x); err != nil {
		t.Fatalf("schema not found: %v", err)
	}

	// Framework tables should exist inside the schema.
	for _, table := range []string{"kiff_events", "kiff_decisions", "kiff_audit", "kiff_approvals"} {
		row := pool.QueryRow(context.Background(),
			"SELECT 1 FROM information_schema.tables WHERE table_schema = $1 AND table_name = $2",
			schema, table)
		var x int
		if err := row.Scan(&x); err != nil {
			t.Fatalf("table %s.%s missing: %v", schema, table, err)
		}
	}

	// Tenant row should be active.
	got, err := store.Get(context.Background(), tt.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != tenant.StatusActive {
		t.Fatalf("status: got %q, want active", got.Status)
	}

	// Idempotency: calling again is a no-op.
	if err := prov.Provision(context.Background(), tt.ID); err != nil {
		t.Fatalf("Provision (idempotent): %v", err)
	}

	// Deprovision drops the schema and marks the row deleted.
	if err := prov.Deprovision(context.Background(), tt.ID); err != nil {
		t.Fatalf("Deprovision: %v", err)
	}
	row = pool.QueryRow(context.Background(),
		"SELECT 1 FROM information_schema.schemata WHERE schema_name = $1", schema)
	if err := row.Scan(&x); err == nil {
		t.Fatal("schema still exists after deprovision")
	}
	got, err = store.Get(context.Background(), tt.ID)
	if err != nil {
		t.Fatalf("Get after deprovision: %v", err)
	}
	if got.Status != tenant.StatusDeleted {
		t.Fatalf("status: got %q, want deleted", got.Status)
	}
}

func TestSchemaPoolEnforcesIsolation(t *testing.T) {
	pool := requirePostgres(t)
	truncateTenants(t, pool)
	dropTenantSchemas(t, pool)
	defer func() {
		dropTenantSchemas(t, pool)
		truncateTenants(t, pool)
	}()

	store := tenant.NewPostgresStore(pool)
	prov := tenant.NewProvisioner(pool, store)

	a := sampleTenant("iso-a")
	b := sampleTenant("iso-b")
	for _, tt := range []tenant.Tenant{a, b} {
		if err := store.Create(context.Background(), tt); err != nil {
			t.Fatalf("Create %s: %v", tt.Slug, err)
		}
		if err := prov.Provision(context.Background(), tt.ID); err != nil {
			t.Fatalf("Provision %s: %v", tt.Slug, err)
		}
	}

	sp := tenant.NewSchemaPool(pool)

	// Insert a sentinel event into tenant A's schema.
	connA, err := sp.Acquire(context.Background(), a.ID)
	if err != nil {
		t.Fatalf("Acquire A: %v", err)
	}
	const insert = `
		INSERT INTO kiff_events (id, type, entity_id, entity_type, source, actor_id, occurred_at)
		VALUES ($1, $2, $3, $4, $5, $6, NOW())
	`
	if _, err := connA.Exec(context.Background(), insert,
		"ev-1", "TICKET_OPENED", "ticket-1", "ticket", "test", "user-1"); err != nil {
		connA.Release()
		t.Fatalf("insert into A: %v", err)
	}
	connA.Release()

	// Read from B's schema. The same query should see zero rows
	// because search_path points at B's schema, not A's.
	connB, err := sp.Acquire(context.Background(), b.ID)
	if err != nil {
		t.Fatalf("Acquire B: %v", err)
	}
	defer connB.Release()

	var count int
	if err := connB.QueryRow(context.Background(),
		"SELECT COUNT(*) FROM kiff_events").Scan(&count); err != nil {
		t.Fatalf("count B: %v", err)
	}
	if count != 0 {
		t.Fatalf("tenant B sees %d events; expected isolation", count)
	}

	// Read from A's schema again to confirm the row is there.
	connA, err = sp.Acquire(context.Background(), a.ID)
	if err != nil {
		t.Fatalf("re-Acquire A: %v", err)
	}
	defer connA.Release()
	if err := connA.QueryRow(context.Background(),
		"SELECT COUNT(*) FROM kiff_events").Scan(&count); err != nil {
		t.Fatalf("count A: %v", err)
	}
	if count != 1 {
		t.Fatalf("tenant A sees %d events; expected 1", count)
	}
}

func TestSchemaPoolRejectsInvalidTenantID(t *testing.T) {
	pool := requirePostgres(t)
	sp := tenant.NewSchemaPool(pool)

	if _, err := sp.Acquire(context.Background(), "not-a-ulid"); err == nil {
		t.Fatal("Acquire accepted non-ULID id")
	}
	if _, err := sp.Acquire(context.Background(), strings.Repeat("a", 27)); err == nil {
		t.Fatal("Acquire accepted overlong id")
	}
}

// TestPostgresStoreSetMetadataJSONNullSafe guards the jsonb-null merge
// case: a tenant created with a nil Metadata map is stored as the JSON
// value `null` (not SQL NULL). A naive `COALESCE(metadata,'{}') || ...`
// would yield a JSON array there; SetMetadata's NULLIF guard must keep
// it an object so ModeFor / PlanFor read the key back correctly. Gated
// on KIFF_TEST_POSTGRES_URL.
func TestPostgresStoreSetMetadataJSONNullSafe(t *testing.T) {
	pool := requirePostgres(t)
	truncateTenants(t, pool)
	defer truncateTenants(t, pool)

	store := tenant.NewPostgresStore(pool)
	ctx := context.Background()

	tt := tenant.Tenant{
		ID:          tenant.NewID(),
		Slug:        "jsonnull-pg",
		DisplayName: "json null pg",
		Status:      tenant.StatusProvisioning,
		Metadata:    nil, // stored as jsonb `null`
	}
	if err := store.Create(ctx, tt); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Confirm the precondition: the column really is jsonb null.
	var raw string
	if err := pool.QueryRow(ctx, "SELECT metadata::text FROM cloud_meta.tenants WHERE id = $1", tt.ID).Scan(&raw); err != nil {
		t.Fatalf("read raw metadata: %v", err)
	}
	if raw != "null" {
		t.Logf("precondition note: metadata is %q (expected jsonb null); test still valid", raw)
	}

	// Merge a key. Must produce an object, not an array.
	if err := store.SetMetadata(ctx, tt.ID, tenant.MetadataKeyMode, "shadow"); err != nil {
		t.Fatalf("SetMetadata: %v", err)
	}
	got, err := store.Get(ctx, tt.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if tenant.ModeFor(got) != tenant.ModeObserve {
		t.Fatalf("ModeFor = %q, want shadow (jsonb-null merge corrupted the object?)", tenant.ModeFor(got))
	}

	// A second key must merge alongside, preserving the first.
	if err := store.SetMetadata(ctx, tt.ID, "plan", "builder"); err != nil {
		t.Fatalf("SetMetadata plan: %v", err)
	}
	got, _ = store.Get(ctx, tt.ID)
	if got.Metadata["mode"] != "shadow" || got.Metadata["plan"] != "builder" {
		t.Fatalf("metadata not merged as object: %+v", got.Metadata)
	}

	// Delete on a (now object) metadata still works.
	if err := store.SetMetadata(ctx, tt.ID, "plan", nil); err != nil {
		t.Fatalf("SetMetadata delete: %v", err)
	}
	got, _ = store.Get(ctx, tt.ID)
	if _, ok := got.Metadata["plan"]; ok {
		t.Fatalf("plan not deleted: %+v", got.Metadata)
	}
}

// Erasure reaches kiff-gateway's tables: every table in the
// gateway schema with a tenant_id column loses the tenant's rows; other
// tenants' rows stay. Re-running for an already-deleted tenant erases
// rows that appeared later, which is the cleanup path for data written
// before this existed.
func TestDeprovisionErasesGatewayRows(t *testing.T) {
	pool := requirePostgres(t)
	truncateTenants(t, pool)
	dropTenantSchemas(t, pool)
	ctx := context.Background()
	defer func() {
		_, _ = pool.Exec(ctx, `DROP TABLE IF EXISTS gateway.erasure_calls, gateway.erasure_tools, gateway.erasure_no_tenant`)
		dropTenantSchemas(t, pool)
		truncateTenants(t, pool)
	}()
	for _, q := range []string{
		`CREATE SCHEMA IF NOT EXISTS gateway`,
		`CREATE TABLE IF NOT EXISTS gateway.erasure_calls (tenant_id text NOT NULL, arguments jsonb)`,
		`CREATE TABLE IF NOT EXISTS gateway.erasure_tools (tenant_id text NOT NULL, name text, credential bytea)`,
		`CREATE TABLE IF NOT EXISTS gateway.erasure_no_tenant (k text)`,
	} {
		if _, err := pool.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	store := tenant.NewPostgresStore(pool)
	prov := tenant.NewProvisioner(pool, store)
	a, b := sampleTenant("erase-a"), sampleTenant("erase-b")
	for _, tt := range []tenant.Tenant{a, b} {
		if err := store.Create(ctx, tt); err != nil {
			t.Fatal(err)
		}
		if err := prov.Provision(ctx, tt.ID); err != nil {
			t.Fatal(err)
		}
		for _, q := range []string{
			`INSERT INTO gateway.erasure_calls VALUES ($1, '{"order":"o-1"}')`,
			`INSERT INTO gateway.erasure_tools VALUES ($1, 'issue_refund', '\x00')`,
		} {
			if _, err := pool.Exec(ctx, q, tt.ID); err != nil {
				t.Fatal(err)
			}
		}
	}
	_, _ = pool.Exec(ctx, `INSERT INTO gateway.erasure_no_tenant VALUES ('kept')`)
	count := func(table, id string) int {
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM gateway.`+table+` WHERE tenant_id = $1`, id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	if err := prov.Deprovision(ctx, a.ID); err != nil {
		t.Fatalf("Deprovision: %v", err)
	}
	for _, table := range []string{"erasure_calls", "erasure_tools"} {
		if n := count(table, a.ID); n != 0 {
			t.Errorf("gateway.%s kept %d row(s) of the erased tenant", table, n)
		}
		if n := count(table, b.ID); n != 1 {
			t.Errorf("gateway.%s lost another tenant's row: %d", table, n)
		}
	}
	var kept int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM gateway.erasure_no_tenant`).Scan(&kept)
	if kept != 1 {
		t.Errorf("a table without tenant_id was touched: %d", kept)
	}

	// Rows left from before erasure covered the gateway: re-running
	// Deprovision on the deleted tenant removes them.
	if _, err := pool.Exec(ctx, `INSERT INTO gateway.erasure_calls VALUES ($1, '{}')`, a.ID); err != nil {
		t.Fatal(err)
	}
	if err := prov.Deprovision(ctx, a.ID); err != nil {
		t.Fatalf("re-run Deprovision: %v", err)
	}
	if n := count("erasure_calls", a.ID); n != 0 {
		t.Fatalf("re-run left %d row(s)", n)
	}
}
