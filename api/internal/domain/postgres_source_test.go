package domain_test

import (
	"context"
	"errors"
	"github.com/kiff/cloud/api/internal/testdb"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kiff/cloud/api/internal/domain"
	"github.com/kiff/cloud/api/internal/tenant"
)

// requirePostgres mirrors the helper in the tenant package — same
// env-var gating, same auto-skip behavior. Duplication is
// preferable to package coupling between tenant and domain tests.
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
	if err := domain.ApplyTenantDomainsSchema(ctx, pool); err != nil {
		t.Fatalf("ApplyTenantDomainsSchema: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// truncate clears the cloud_meta tables. tenant_domains rows are
// removed first because they reference cloud_meta.tenants.
func truncate(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	if _, err := pool.Exec(context.Background(),
		"TRUNCATE cloud_meta.tenant_domains, cloud_meta.tenants CASCADE"); err != nil {
		t.Fatalf("truncate: %v", err)
	}
}

func TestPostgresSourceSaveLoadDelete(t *testing.T) {
	pool := requirePostgres(t)
	truncate(t, pool)
	defer truncate(t, pool)

	store := tenant.NewPostgresStore(pool)
	tt := tenant.Tenant{
		ID:          tenant.NewID(),
		Slug:        "yaml-test",
		DisplayName: "yaml-test",
		Status:      tenant.StatusProvisioning,
	}
	if err := store.Create(context.Background(), tt); err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	now := time.Now().UTC()
	if err := store.SetStatus(context.Background(), tt.ID, tenant.StatusActive, &now); err != nil {
		t.Fatalf("activate: %v", err)
	}

	src := domain.NewPostgresSource(pool)
	if _, err := src.Load(context.Background(), tt.ID); !errors.Is(err, domain.ErrDomainNotFound) {
		t.Fatalf("Load before save: got %v, want ErrDomainNotFound", err)
	}

	yamlA := []byte("domain: refund\nentity: Order\n")
	if err := src.Save(context.Background(), tt.ID, yamlA); err != nil {
		t.Fatalf("Save A: %v", err)
	}
	td, err := src.Load(context.Background(), tt.ID)
	if err != nil {
		t.Fatalf("Load A: %v", err)
	}
	if td.Version != 1 {
		t.Fatalf("version A: got %d, want 1", td.Version)
	}
	if string(td.YAML) != string(yamlA) {
		t.Fatalf("yaml A round-trip mismatch")
	}

	// Save again to confirm version bumps.
	yamlB := []byte("domain: refund\nentity: Order\nevents: [ORDER_PLACED]\n")
	if err := src.Save(context.Background(), tt.ID, yamlB); err != nil {
		t.Fatalf("Save B: %v", err)
	}
	td, err = src.Load(context.Background(), tt.ID)
	if err != nil {
		t.Fatalf("Load B: %v", err)
	}
	if td.Version != 2 {
		t.Fatalf("version B: got %d, want 2", td.Version)
	}

	// Delete; subsequent Load returns ErrDomainNotFound.
	if err := src.Delete(context.Background(), tt.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := src.Load(context.Background(), tt.ID); !errors.Is(err, domain.ErrDomainNotFound) {
		t.Fatalf("Load after delete: got %v, want ErrDomainNotFound", err)
	}
}

func TestMemorySourceRoundTrip(t *testing.T) {
	t.Parallel()

	src := domain.NewMemorySource()

	if _, err := src.Load(context.Background(), "ten-1"); !errors.Is(err, domain.ErrDomainNotFound) {
		t.Fatalf("Load before save: got %v, want ErrDomainNotFound", err)
	}

	if err := src.Save(context.Background(), "ten-1", []byte("a")); err != nil {
		t.Fatalf("save: %v", err)
	}
	td, err := src.Load(context.Background(), "ten-1")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if td.Version != 1 || string(td.YAML) != "a" {
		t.Fatalf("round-trip: %+v", td)
	}

	if err := src.Save(context.Background(), "ten-1", []byte("b")); err != nil {
		t.Fatalf("save 2: %v", err)
	}
	td, _ = src.Load(context.Background(), "ten-1")
	if td.Version != 2 || string(td.YAML) != "b" {
		t.Fatalf("version not bumped: %+v", td)
	}
}

// TestPostgresSourceList_MultipleDomains verifies the multi-domain
// read projection: a tenant with a default
// domain plus a second non-default domain gets both back from List,
// default first, and each is addressable by domain_id via
// LoadByDomain. The non-default row is inserted directly (the create
// API lands in the next slice); this test proves the read side is
// honestly multi-domain regardless of how the row got there.
func TestPostgresSourceList_MultipleDomains(t *testing.T) {
	pool := requirePostgres(t)
	truncate(t, pool)
	defer truncate(t, pool)

	ctx := context.Background()
	store := tenant.NewPostgresStore(pool)
	tt := tenant.Tenant{
		ID:          tenant.NewID(),
		Slug:        "multi-list",
		DisplayName: "multi-list",
		Status:      tenant.StatusProvisioning,
	}
	if err := store.Create(ctx, tt); err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	now := time.Now().UTC()
	if err := store.SetStatus(ctx, tt.ID, tenant.StatusActive, &now); err != nil {
		t.Fatalf("activate: %v", err)
	}

	src := domain.NewPostgresSource(pool)

	// Empty before any domain: List returns an empty slice, not an error.
	if got, err := src.List(ctx, tt.ID); err != nil || len(got) != 0 {
		t.Fatalf("List empty: got %d domains, err=%v; want 0, nil", len(got), err)
	}

	// Default domain via the canonical Save path.
	defaultYAML := []byte("domain: refund\nentity: Order\n")
	if err := src.Save(ctx, tt.ID, defaultYAML); err != nil {
		t.Fatalf("Save default: %v", err)
	}

	// Second, non-default domain inserted directly (no create API yet).
	secondYAML := []byte("domain: payments\nentity: Invoice\n")
	secondID, secondSlug := domain.DeriveStorageIdentity(secondYAML)
	if _, err := pool.Exec(ctx, `
		INSERT INTO cloud_meta.tenant_domains
			(tenant_id, domain_id, slug, yaml, version, is_default, created_at, updated_at)
		VALUES ($1, $2, $3, $4, 1, FALSE, NOW(), NOW())
	`, tt.ID, secondID, secondSlug, secondYAML); err != nil {
		t.Fatalf("insert non-default domain: %v", err)
	}

	got, err := src.List(ctx, tt.ID)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("List: got %d domains, want 2", len(got))
	}
	// Default first.
	if got[0].Slug != "refund" {
		t.Errorf("List[0].Slug: got %q, want refund (default first)", got[0].Slug)
	}
	if got[1].Slug != "payments" {
		t.Errorf("List[1].Slug: got %q, want payments", got[1].Slug)
	}

	// Each domain is addressable by its domain_id.
	pay, err := src.LoadByDomain(ctx, tt.ID, secondID)
	if err != nil {
		t.Fatalf("LoadByDomain payments: %v", err)
	}
	if string(pay.YAML) != string(secondYAML) {
		t.Errorf("LoadByDomain payments: yaml mismatch")
	}

	// Load (singular) still resolves the default.
	def, err := src.Load(ctx, tt.ID)
	if err != nil {
		t.Fatalf("Load default: %v", err)
	}
	if def.Slug != "refund" {
		t.Errorf("Load default: got %q, want refund", def.Slug)
	}
}

func TestMemorySourceList(t *testing.T) {
	t.Parallel()
	src := domain.NewMemorySource()

	// No domain yet: empty slice, no error.
	if got, err := src.List(context.Background(), "ten-1"); err != nil || len(got) != 0 {
		t.Fatalf("List empty: got %d, err=%v; want 0, nil", len(got), err)
	}

	if err := src.Save(context.Background(), "ten-1", []byte("domain: refund\nentity: Order\n")); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := src.List(context.Background(), "ten-1")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 || got[0].Slug != "refund" {
		t.Fatalf("List: got %+v, want one refund domain", got)
	}
}

// TestPostgresSourceCreate verifies the per-domain create write:
// it inserts an additional non-default
// domain, is idempotent-safe via ErrDomainExists on a duplicate
// domain_id, and never disturbs the tenant's default.
func TestPostgresSourceCreate(t *testing.T) {
	pool := requirePostgres(t)
	truncate(t, pool)
	defer truncate(t, pool)

	ctx := context.Background()
	store := tenant.NewPostgresStore(pool)
	tt := tenant.Tenant{
		ID:          tenant.NewID(),
		Slug:        "create-test",
		DisplayName: "create-test",
		Status:      tenant.StatusProvisioning,
	}
	if err := store.Create(ctx, tt); err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	now := time.Now().UTC()
	if err := store.SetStatus(ctx, tt.ID, tenant.StatusActive, &now); err != nil {
		t.Fatalf("activate: %v", err)
	}

	src := domain.NewPostgresSource(pool)

	// Establish the default via the canonical Save path.
	if err := src.Save(ctx, tt.ID, []byte("domain: refund\nentity: Order\n")); err != nil {
		t.Fatalf("Save default: %v", err)
	}

	// Create an additional, non-default domain.
	if err := src.Create(ctx, tt.ID, []byte("domain: payments\nentity: Invoice\n")); err != nil {
		t.Fatalf("Create payments: %v", err)
	}

	// It is non-default; the original default is untouched.
	var isDefault bool
	if err := pool.QueryRow(ctx, `
		SELECT is_default FROM cloud_meta.tenant_domains
		WHERE tenant_id = $1 AND domain_id = 'payments'
	`, tt.ID).Scan(&isDefault); err != nil {
		t.Fatalf("scan is_default: %v", err)
	}
	if isDefault {
		t.Error("created domain must be non-default")
	}

	// List returns both, default first.
	got, err := src.List(ctx, tt.ID)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 2 || got[0].Slug != "refund" || got[1].Slug != "payments" {
		t.Fatalf("List = %+v, want [refund, payments]", got)
	}

	// Re-creating the same domain is ErrDomainExists, not an overwrite.
	if err := src.Create(ctx, tt.ID, []byte("domain: payments\nentity: Invoice\nevents: [X]\n")); !errors.Is(err, domain.ErrDomainExists) {
		t.Fatalf("duplicate Create: got %v, want ErrDomainExists", err)
	}
	// And the original payments yaml is unchanged (no overwrite).
	pay, err := src.LoadByDomain(ctx, tt.ID, "payments")
	if err != nil {
		t.Fatalf("LoadByDomain payments: %v", err)
	}
	if string(pay.YAML) != "domain: payments\nentity: Invoice\n" {
		t.Errorf("payments yaml was overwritten: %q", string(pay.YAML))
	}
}

// TestPostgresSourceDeleteByDomain verifies the per-domain delete:
// it removes a single domain by id, leaves the
// rest, and reports ErrDomainNotFound for an unknown id.
func TestPostgresSourceDeleteByDomain(t *testing.T) {
	pool := requirePostgres(t)
	truncate(t, pool)
	defer truncate(t, pool)

	ctx := context.Background()
	store := tenant.NewPostgresStore(pool)
	tt := tenant.Tenant{
		ID:          tenant.NewID(),
		Slug:        "delete-test",
		DisplayName: "delete-test",
		Status:      tenant.StatusProvisioning,
	}
	if err := store.Create(ctx, tt); err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	now := time.Now().UTC()
	if err := store.SetStatus(ctx, tt.ID, tenant.StatusActive, &now); err != nil {
		t.Fatalf("activate: %v", err)
	}

	src := domain.NewPostgresSource(pool)
	if err := src.Save(ctx, tt.ID, []byte("domain: refund\nentity: Order\n")); err != nil {
		t.Fatalf("Save default: %v", err)
	}
	if err := src.Create(ctx, tt.ID, []byte("domain: payments\nentity: Invoice\n")); err != nil {
		t.Fatalf("Create payments: %v", err)
	}

	// Deleting an unknown domain is ErrDomainNotFound.
	if err := src.DeleteByDomain(ctx, tt.ID, "nope"); !errors.Is(err, domain.ErrDomainNotFound) {
		t.Fatalf("delete unknown: got %v, want ErrDomainNotFound", err)
	}

	// Delete the non-default domain.
	if err := src.DeleteByDomain(ctx, tt.ID, "payments"); err != nil {
		t.Fatalf("DeleteByDomain payments: %v", err)
	}
	got, err := src.List(ctx, tt.ID)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 || got[0].Slug != "refund" {
		t.Fatalf("after delete List = %+v, want [refund]", got)
	}
}

// TestPostgresSourceUpdateByDomain verifies the per-domain in-place
// update: the addressed domain's yaml is
// replaced and version bumped, while domain_id and slug are preserved;
// an unknown domain is ErrDomainNotFound.
func TestPostgresSourceUpdateByDomain(t *testing.T) {
	pool := requirePostgres(t)
	truncate(t, pool)
	defer truncate(t, pool)

	ctx := context.Background()
	store := tenant.NewPostgresStore(pool)
	tt := tenant.Tenant{ID: tenant.NewID(), Slug: "update-test", DisplayName: "update-test", Status: tenant.StatusProvisioning}
	if err := store.Create(ctx, tt); err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	now := time.Now().UTC()
	if err := store.SetStatus(ctx, tt.ID, tenant.StatusActive, &now); err != nil {
		t.Fatalf("activate: %v", err)
	}

	src := domain.NewPostgresSource(pool)
	if err := src.Save(ctx, tt.ID, []byte("domain: refund\nentity: Order\n")); err != nil {
		t.Fatalf("Save default: %v", err)
	}
	if err := src.Create(ctx, tt.ID, []byte("domain: payments\nentity: Invoice\n")); err != nil {
		t.Fatalf("Create payments: %v", err)
	}

	// Unknown domain → ErrDomainNotFound.
	if err := src.UpdateByDomain(ctx, tt.ID, "nope", []byte("domain: payments\nentity: Invoice\n")); !errors.Is(err, domain.ErrDomainNotFound) {
		t.Fatalf("update unknown: got %v, want ErrDomainNotFound", err)
	}

	// Re-author payments in place.
	edited := []byte("domain: payments\nentity: Invoice\nevents: [RAISED]\n")
	if err := src.UpdateByDomain(ctx, tt.ID, "payments", edited); err != nil {
		t.Fatalf("UpdateByDomain payments: %v", err)
	}
	td, err := src.LoadByDomain(ctx, tt.ID, "payments")
	if err != nil {
		t.Fatalf("LoadByDomain: %v", err)
	}
	if td.Version != 2 {
		t.Errorf("version = %d, want 2", td.Version)
	}
	if td.DomainID != "payments" || td.Slug != "payments" {
		t.Errorf("identity moved: domain_id=%q slug=%q, want payments/payments", td.DomainID, td.Slug)
	}
	if string(td.YAML) != string(edited) {
		t.Errorf("yaml not updated")
	}

	// The default is untouched.
	def, _ := src.Load(ctx, tt.ID)
	if def.Slug != "refund" || def.Version != 1 {
		t.Errorf("default disturbed: slug=%q v=%d", def.Slug, def.Version)
	}
}
