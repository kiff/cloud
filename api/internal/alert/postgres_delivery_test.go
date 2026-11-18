package alert_test

import (
	"context"
	"fmt"
	"github.com/kiff/cloud/api/internal/testdb"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kiff/cloud/api/internal/alert"
	"github.com/kiff/cloud/api/internal/tenant"
)

// requirePostgres gates the delivery-store suite on
// KIFF_TEST_POSTGRES_URL, mirroring the receipt/tenant harnesses.
// Without it the Postgres-backed tests skip cleanly. It applies
// cloud_meta (the alert_deliveries FK needs cloud_meta.tenants) and the
// alert_deliveries DDL.
func requirePostgres(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := testdb.URL(t)
	if url == "" {
		t.Skip("KIFF_TEST_POSTGRES_URL not set; skipping Postgres-backed test")
	}
	c := context.Background()
	pool, err := pgxpool.New(c, url)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	if err := pool.Ping(c); err != nil {
		t.Fatalf("ping: %v", err)
	}
	if err := tenant.ApplyCloudMeta(c, pool); err != nil {
		t.Fatalf("ApplyCloudMeta: %v", err)
	}
	if err := alert.ApplyDeliveriesSchema(c, pool); err != nil {
		t.Fatalf("ApplyDeliveriesSchema: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func resetCloudMeta(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	if _, err := pool.Exec(context.Background(),
		"TRUNCATE cloud_meta.tenants CASCADE"); err != nil {
		t.Fatalf("truncate: %v", err)
	}
}

func provisionTenantRow(t *testing.T, pool *pgxpool.Pool) tenant.Tenant {
	t.Helper()
	store := tenant.NewPostgresStore(pool)
	tt := tenant.Tenant{
		ID:          tenant.NewID(),
		Slug:        "alert-deliv-pg",
		DisplayName: "alert deliveries pg",
		Status:      tenant.StatusProvisioning,
	}
	if err := store.Create(context.Background(), tt); err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	return tt
}

// TestPostgresDeliveryStore_RoundTrip persists three attempts for one
// triggering decision (delivered/failed/blocked) and reads them back
// newest-first, tenant- and proposal-scoped. Gated on
// KIFF_TEST_POSTGRES_URL.
func TestPostgresDeliveryStore_RoundTrip(t *testing.T) {
	pool := requirePostgres(t)
	resetCloudMeta(t, pool)
	defer resetCloudMeta(t, pool)
	tt := provisionTenantRow(t, pool)

	store := alert.NewPostgresDeliveryStore(pool)
	base := time.Date(2026, time.May, 24, 12, 0, 0, 0, time.UTC)

	recs := []alert.Delivery{
		{ID: "adlv-ok", TenantID: tt.ID, ProposalID: "prop-pg", TargetID: "t-ok",
			TargetType: "webhook", Action: "REFUND_ORDER", Outcome: "blocked",
			Status: alert.DeliveryDelivered, AttemptedAt: base},
		{ID: "adlv-fail", TenantID: tt.ID, ProposalID: "prop-pg", TargetID: "t-fail",
			TargetType: "webhook", Action: "REFUND_ORDER", Outcome: "blocked",
			Status: alert.DeliveryFailed, Detail: "502", AttemptedAt: base.Add(time.Minute)},
		{ID: "adlv-ssrf", TenantID: tt.ID, ProposalID: "prop-pg", TargetID: "t-ssrf",
			TargetType: "webhook", Action: "REFUND_ORDER", Outcome: "blocked",
			Status: alert.DeliveryBlocked, Detail: "blocked address", AttemptedAt: base.Add(2 * time.Minute)},
	}
	for _, r := range recs {
		if err := store.RecordDelivery(context.Background(), r); err != nil {
			t.Fatalf("RecordDelivery %s: %v", r.TargetID, err)
		}
	}

	got, err := store.ListDeliveries(context.Background(), tt.ID, "prop-pg")
	if err != nil {
		t.Fatalf("ListDeliveries: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d deliveries, want 3", len(got))
	}
	// Newest first: t-ssrf, t-fail, t-ok.
	if got[0].TargetID != "t-ssrf" || got[1].TargetID != "t-fail" || got[2].TargetID != "t-ok" {
		t.Errorf("order wrong: %s, %s, %s", got[0].TargetID, got[1].TargetID, got[2].TargetID)
	}
	if got[0].Status != alert.DeliveryBlocked || got[0].Detail != "blocked address" {
		t.Errorf("blocked row not persisted faithfully: %+v", got[0])
	}
	if got[2].Status != alert.DeliveryDelivered {
		t.Errorf("delivered row status = %q", got[2].Status)
	}
}

// TestPostgresDeliveryStore_ScopesByTenantAndProposal confirms a query
// only returns the asked-for tenant + proposal slice.
func TestPostgresDeliveryStore_ScopesByTenantAndProposal(t *testing.T) {
	pool := requirePostgres(t)
	resetCloudMeta(t, pool)
	defer resetCloudMeta(t, pool)
	tt := provisionTenantRow(t, pool)

	store := alert.NewPostgresDeliveryStore(pool)
	now := time.Now().UTC()
	n := 0
	mk := func(proposal, target string) alert.Delivery {
		n++
		return alert.Delivery{ID: fmt.Sprintf("adlv-%d", n), TenantID: tt.ID, ProposalID: proposal,
			TargetID: target, TargetType: "webhook", Action: "A", Outcome: "blocked",
			Status: alert.DeliveryDelivered, AttemptedAt: now}
	}
	for _, d := range []alert.Delivery{mk("p1", "a"), mk("p1", "b"), mk("p2", "c")} {
		if err := store.RecordDelivery(context.Background(), d); err != nil {
			t.Fatalf("RecordDelivery: %v", err)
		}
	}

	got, err := store.ListDeliveries(context.Background(), tt.ID, "p1")
	if err != nil {
		t.Fatalf("ListDeliveries: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d, want 2 for proposal p1", len(got))
	}

	// Unknown proposal -> empty, no error.
	none, err := store.ListDeliveries(context.Background(), tt.ID, "nope")
	if err != nil {
		t.Fatalf("ListDeliveries unknown: %v", err)
	}
	if len(none) != 0 {
		t.Fatalf("unknown proposal returned %d rows", len(none))
	}
}

// TestPostgresDeliveryStore_TenantDeleteCascades confirms deleting the
// tenant row drops its delivery attempts via the FK cascade — a deleted
// tenant's deliveries must not outlive the tenant.
func TestPostgresDeliveryStore_TenantDeleteCascades(t *testing.T) {
	pool := requirePostgres(t)
	resetCloudMeta(t, pool)
	defer resetCloudMeta(t, pool)
	tt := provisionTenantRow(t, pool)

	store := alert.NewPostgresDeliveryStore(pool)
	if err := store.RecordDelivery(context.Background(), alert.Delivery{
		ID: "adlv-cascade", TenantID: tt.ID, ProposalID: "prop-cascade", TargetID: "t",
		TargetType: "webhook", Action: "A", Outcome: "blocked",
		Status: alert.DeliveryDelivered, AttemptedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("RecordDelivery: %v", err)
	}

	if err := tenant.NewPostgresStore(pool).Delete(context.Background(), tt.ID); err != nil {
		t.Fatalf("tenant Delete: %v", err)
	}

	got, err := store.ListDeliveries(context.Background(), tt.ID, "prop-cascade")
	if err != nil {
		t.Fatalf("ListDeliveries: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("deliveries survived tenant delete: %+v", got)
	}
}
