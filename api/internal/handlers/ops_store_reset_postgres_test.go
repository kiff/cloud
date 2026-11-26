package handlers

import (
	"context"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kiff/cloud/api/internal/appstore"
	"github.com/kiff/cloud/api/internal/operatoraudit"
	"github.com/kiff/cloud/api/internal/packs"
	"github.com/kiff/cloud/api/internal/tenant"
	"github.com/kiff/cloud/api/internal/testdb"
)

// The Postgres mutator's SetTenantMetadata, as Reset store billing uses it:
// every key and the audit row commit together, "" clears a key, and the
// subscriptions set aside read back as a list; a write that fails leaves the
// account as it was and no audit row.
func TestPostgresSetTenantMetadataWithItsAudit(t *testing.T) {
	url := testdb.URL(t)
	if url == "" {
		t.Skip("KIFF_TEST_POSTGRES_URL not set; skipping the Postgres ops mutator test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	t.Cleanup(pool.Close)
	if err := tenant.ApplyCloudMeta(ctx, pool); err != nil {
		t.Fatalf("ApplyCloudMeta: %v", err)
	}
	if err := operatoraudit.ApplySchema(ctx, pool); err != nil {
		t.Fatalf("operatoraudit.ApplySchema: %v", err)
	}
	tenants := tenant.NewPostgresStore(pool)
	audit := operatoraudit.NewPostgresStore(pool)
	id := tenant.NewID()
	if err := tenants.Create(ctx, tenant.Tenant{ID: id, Slug: "review-" + id[len(id)-8:], DisplayName: "Review", Status: tenant.StatusActive,
		Metadata: soloFromAStore()}); err != nil {
		t.Fatal(err)
	}
	m := NewPostgresOpsMutator(pool)
	before, _ := tenants.Get(ctx, id)
	set := appstore.ResetBilling(before, before.CreatedAt, storeBilling()...)
	entry := operatoraudit.Entry{OperatorSubject: opsSubject, Action: operatoraudit.ActionStoreBillingReset, TargetTenantID: id, Reason: "App Review"}
	if err := m.SetTenantMetadata(ctx, id, set, entry); err != nil {
		t.Fatalf("SetTenantMetadata: %v", err)
	}
	got, err := tenants.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if tenant.PlanFor(got).Canonical != tenant.PlanTrial || tenant.BillingSourceOf(got) != tenant.BillingNone || got.Metadata[appstore.MetaOriginalTransaction] != "" {
		t.Fatalf("after the reset: %v", got.Metadata)
	}
	if aside, _, ok := appstore.AppStore(packs.Catalog{}).WasReset(got); !ok || !slices.Equal(aside, []string{"orig-review"}) {
		t.Fatalf("set aside, read back from Postgres: %v %v", aside, ok)
	}
	if entries, _ := audit.List(ctx, id, 10); len(entries) != 1 || entries[0].Action != operatoraudit.ActionStoreBillingReset {
		t.Fatalf("audit: %+v", entries)
	}
	// An audit row that can't be written (metadata that won't encode) rolls the keys back too.
	bad := operatoraudit.Entry{OperatorSubject: opsSubject, Action: operatoraudit.ActionStoreBillingReset, TargetTenantID: id,
		Metadata: map[string]any{"x": func() {}}}
	if err := m.SetTenantMetadata(ctx, id, map[string]any{tenant.MetadataKeyPlan: "solo"}, bad); err == nil {
		t.Fatal("an audit entry that can't be encoded was written")
	}
	if got, _ := tenants.Get(ctx, id); tenant.PlanFor(got).Canonical != tenant.PlanTrial {
		t.Fatalf("a failed audit left the plan %s", tenant.PlanFor(got).Plan)
	}
}
