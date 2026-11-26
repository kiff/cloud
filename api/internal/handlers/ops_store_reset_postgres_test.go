package handlers

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kiff/cloud/api/internal/appstore"
	"github.com/kiff/cloud/api/internal/meter"
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

// The Postgres mutator's ResetStoreBilling: the keys, the account's App Store
// and Google Play packs set aside, and the audit row saying how many, commit
// together; an audit row that can't be written leaves the packs as they were.
func TestPostgresResetStoreBillingSetsAsideStorePacks(t *testing.T) {
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
	if err := meter.ApplySchema(ctx, pool); err != nil {
		t.Fatalf("meter.ApplySchema: %v", err)
	}
	if err := packs.ApplySchema(ctx, pool); err != nil {
		t.Fatalf("packs.ApplySchema: %v", err)
	}
	tenants := tenant.NewPostgresStore(pool)
	audit := operatoraudit.NewPostgresStore(pool)
	ledger := packs.NewPostgres(pool)
	id := tenant.NewID()
	if err := tenants.Create(ctx, tenant.Tenant{ID: id, Slug: "review-" + id[len(id)-8:], DisplayName: "Review", Status: tenant.StatusActive,
		Metadata: soloFromAStore()}); err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC().Truncate(time.Second)
	for _, g := range []packs.CreditGrant{
		{Pack: "credits-500", Credits: 500, Source: packs.SourceApple, SourceTxn: id + "-500"},
		{Pack: "credits-1500", Credits: 1500, Source: packs.SourceGoogle, SourceTxn: id + "-1500"},
		{Pack: "bonus", Credits: 40, Kind: packs.KindBonus, Source: packs.SourceKIFF, SourceTxn: id + "-bonus"},
	} {
		g.TenantID, g.At = id, at.Add(-time.Hour)
		if _, _, err := ledger.RecordCredits(ctx, g); err != nil {
			t.Fatal(err)
		}
	}
	m := NewPostgresOpsMutator(pool)
	left := func() int64 {
		t.Helper()
		_, l, err := ledger.Credits(ctx, id, at.AddDate(0, -1, 0), at)
		if err != nil {
			t.Fatal(err)
		}
		return l
	}
	// An audit row that can't be written: nothing is set aside, and no key changes.
	bad := operatoraudit.Entry{OperatorSubject: opsSubject, Action: operatoraudit.ActionStoreBillingReset, TargetTenantID: id,
		Metadata: map[string]any{"x": func() {}}}
	if _, err := m.ResetStoreBilling(ctx, id, map[string]any{tenant.MetadataKeyPlan: "trial"}, at, bad); err == nil {
		t.Fatal("an audit entry that can't be encoded was written")
	}
	if got, _ := tenants.Get(ctx, id); left() != 2040 || tenant.PlanFor(got).Canonical != tenant.PlanSolo {
		t.Fatalf("a failed reset changed something: %d pack credits, plan %s", left(), tenant.PlanFor(got).Plan)
	}
	before, _ := tenants.Get(ctx, id)
	entry := operatoraudit.Entry{OperatorSubject: opsSubject, Action: operatoraudit.ActionStoreBillingReset, TargetTenantID: id, Reason: "App Review",
		Metadata: map[string]any{"from_plan": "solo"}}
	aside, err := m.ResetStoreBilling(ctx, id, appstore.ResetBilling(before, at, storeBilling()...), at, entry)
	if err != nil || aside != (packs.StorePacksSetAside{Packs: 2, Credits: 2000}) {
		t.Fatalf("ResetStoreBilling: %+v %v", aside, err)
	}
	if got, _ := tenants.Get(ctx, id); left() != 40 || tenant.PlanFor(got).Canonical != tenant.PlanTrial {
		t.Fatalf("after the reset: %d pack credits, plan %s", left(), tenant.PlanFor(got).Plan)
	}
	entries, _ := audit.List(ctx, id, 10)
	if len(entries) != 1 || entries[0].Metadata["from_plan"] != "solo" ||
		entries[0].Metadata["store_packs_set_aside"] != float64(2) || entries[0].Metadata["store_pack_credits_set_aside"] != float64(2000) {
		t.Fatalf("audit: %+v", entries)
	}
}
