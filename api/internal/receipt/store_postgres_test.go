package receipt_test

import (
	"context"
	"errors"
	"github.com/kiff/cloud/api/internal/testdb"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kiff/cloud/api/internal/receipt"
	"github.com/kiff/cloud/api/internal/tenant"
)

// requirePostgres mirrors the helper used by tenant + auth +
// usage. KIFF_TEST_POSTGRES_URL gates the suite; without it the
// Postgres-backed tests skip cleanly.
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
	if err := receipt.ApplySchema(ctx, pool); err != nil {
		t.Fatalf("receipt.ApplySchema: %v", err)
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
		Slug:        "receipt-pg",
		DisplayName: "receipt pg",
		Status:      tenant.StatusProvisioning,
	}
	if err := store.Create(context.Background(), tt); err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	return tt
}

func TestPostgresStore_RoundTrip(t *testing.T) {
	pool := requirePostgres(t)
	resetCloudMeta(t, pool)
	defer resetCloudMeta(t, pool)
	tt := provisionTenantRow(t, pool)

	store := receipt.NewPostgresStore(pool)
	r := fixtureReceipt(tt.ID, "trace-pg-1")

	if err := store.Save(context.Background(), r); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := store.Get(context.Background(), tt.ID, "trace-pg-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ActionName != "MARK_PAID" {
		t.Errorf("ActionName: got %q", got.ActionName)
	}
	if got.SchemaVersion != receipt.SchemaVersion {
		t.Errorf("SchemaVersion: got %q", got.SchemaVersion)
	}
	if got, want := len(got.CausalChain), 2; got != want {
		t.Errorf("CausalChain length: got %d, want %d", got, want)
	}
}

func TestPostgresStore_GetMissingReturnsNotFound(t *testing.T) {
	pool := requirePostgres(t)
	resetCloudMeta(t, pool)
	defer resetCloudMeta(t, pool)
	tt := provisionTenantRow(t, pool)

	store := receipt.NewPostgresStore(pool)
	_, err := store.Get(context.Background(), tt.ID, "trace-missing")
	if !errors.Is(err, receipt.ErrReceiptNotFound) {
		t.Fatalf("err: got %v, want ErrReceiptNotFound", err)
	}
}

func TestPostgresStore_SaveUpsertsAttestation(t *testing.T) {
	pool := requirePostgres(t)
	resetCloudMeta(t, pool)
	defer resetCloudMeta(t, pool)
	tt := provisionTenantRow(t, pool)

	store := receipt.NewPostgresStore(pool)
	r := fixtureReceipt(tt.ID, "trace-pg-2")
	if err := store.Save(context.Background(), r); err != nil {
		t.Fatalf("Save #1: %v", err)
	}

	r.Attestation = &receipt.Attestation{
		Algorithm:     "secp256k1+keccak256",
		Signature:     "0xsig",
		WalletAddress: "0xwallet",
		AnchorTxHash:  "0xtx",
		AnchoredAt:    time.Date(2026, time.May, 24, 13, 0, 0, 0, time.UTC),
	}
	if err := store.Save(context.Background(), r); err != nil {
		t.Fatalf("Save #2: %v", err)
	}

	got, err := store.Get(context.Background(), tt.ID, "trace-pg-2")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Attestation == nil || got.Attestation.AnchorTxHash != "0xtx" {
		t.Fatalf("Attestation not persisted: %+v", got.Attestation)
	}

	// One last check: the unanchored list should now be empty.
	unanchored, err := store.ListUnanchored(context.Background(), tt.ID)
	if err != nil {
		t.Fatalf("ListUnanchored: %v", err)
	}
	if len(unanchored) != 0 {
		t.Fatalf("attested receipt still in unanchored list: %+v", unanchored)
	}
}

func TestPostgresStore_ListUnanchoredOrdersByIssuedAt(t *testing.T) {
	pool := requirePostgres(t)
	resetCloudMeta(t, pool)
	defer resetCloudMeta(t, pool)
	tt := provisionTenantRow(t, pool)

	store := receipt.NewPostgresStore(pool)
	older := fixtureReceipt(tt.ID, "trace-pg-old")
	older.IssuedAt = time.Date(2026, time.May, 24, 12, 0, 0, 0, time.UTC)
	newer := fixtureReceipt(tt.ID, "trace-pg-new")
	newer.IssuedAt = time.Date(2026, time.May, 24, 12, 30, 0, 0, time.UTC)

	// Save in reverse order to confirm sort isn't accidental.
	if err := store.Save(context.Background(), newer); err != nil {
		t.Fatalf("Save newer: %v", err)
	}
	if err := store.Save(context.Background(), older); err != nil {
		t.Fatalf("Save older: %v", err)
	}

	got, err := store.ListUnanchored(context.Background(), tt.ID)
	if err != nil {
		t.Fatalf("ListUnanchored: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("rows: got %d, want 2", len(got))
	}
	if got[0].TraceID != "trace-pg-old" {
		t.Errorf("first row: got %q, want trace-pg-old", got[0].TraceID)
	}
	if got[1].TraceID != "trace-pg-new" {
		t.Errorf("second row: got %q, want trace-pg-new", got[1].TraceID)
	}
}

func TestPostgresStore_TenantDeleteCascadesReceipts(t *testing.T) {
	pool := requirePostgres(t)
	resetCloudMeta(t, pool)
	defer resetCloudMeta(t, pool)
	tt := provisionTenantRow(t, pool)

	store := receipt.NewPostgresStore(pool)
	r := fixtureReceipt(tt.ID, "trace-pg-cascade")
	if err := store.Save(context.Background(), r); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Delete the tenant row; the FK cascade should drop the
	// receipt with it. This is the property that protects a
	// deleted tenant's receipts from outliving the tenant.
	tenantStore := tenant.NewPostgresStore(pool)
	if err := tenantStore.Delete(context.Background(), tt.ID); err != nil {
		t.Fatalf("tenant Delete: %v", err)
	}

	_, err := store.Get(context.Background(), tt.ID, "trace-pg-cascade")
	if !errors.Is(err, receipt.ErrReceiptNotFound) {
		t.Fatalf("expected receipt removed by FK cascade; got err=%v", err)
	}
}

// TestPostgresStore_GetByProposalID exercises the explicit
// decision->receipt link SQL: Save persists proposal_id and
// GetByProposalID resolves by it, tenant-scoped. Gated on
// KIFF_TEST_POSTGRES_URL.
func TestPostgresStore_GetByProposalID(t *testing.T) {
	pool := requirePostgres(t)
	resetCloudMeta(t, pool)
	defer resetCloudMeta(t, pool)
	tt := provisionTenantRow(t, pool)

	store := receipt.NewPostgresStore(pool)
	r := fixtureReceipt(tt.ID, "trace-pl-pg")
	r.ProposalID = "prop-pl-pg"
	if err := store.Save(context.Background(), r); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := store.GetByProposalID(context.Background(), tt.ID, "prop-pl-pg")
	if err != nil {
		t.Fatalf("GetByProposalID: %v", err)
	}
	if got.TraceID != "trace-pl-pg" || got.ProposalID != "prop-pl-pg" {
		t.Errorf("resolved wrong receipt: trace=%q proposal=%q", got.TraceID, got.ProposalID)
	}

	// Unknown proposal id and empty -> not found.
	if _, err := store.GetByProposalID(context.Background(), tt.ID, "nope"); !errors.Is(err, receipt.ErrReceiptNotFound) {
		t.Errorf("unknown: got %v, want ErrReceiptNotFound", err)
	}
	if _, err := store.GetByProposalID(context.Background(), tt.ID, ""); !errors.Is(err, receipt.ErrReceiptNotFound) {
		t.Errorf("empty: got %v, want ErrReceiptNotFound", err)
	}
}

// At the same issue time the settled receipt wins, as in
// memory, whichever was saved first; its step data round-trips.
func TestPostgresStore_SettledWinsATie(t *testing.T) {
	pool := requirePostgres(t)
	resetCloudMeta(t, pool)
	defer resetCloudMeta(t, pool)
	tt := provisionTenantRow(t, pool)
	store := receipt.NewPostgresStore(pool)
	ctx := context.Background()
	settled := fixtureReceipt(tt.ID, "settled-p-tie")
	decision := fixtureReceipt(tt.ID, "decision-p-tie")
	decision.IssuedAt = settled.IssuedAt
	settled.ProposalID, decision.ProposalID = "p-tie", "p-tie"
	settled.CausalChain = append(settled.CausalChain, receipt.ChainStep{Sequence: len(settled.CausalChain) + 1, Kind: "hold_settled",
		OccurredAt: settled.IssuedAt, DataHash: "x", Data: map[string]any{"role": "admin", "note": "Ask for photos."}})
	for _, r := range []receipt.Receipt{settled, decision} {
		if err := store.Save(ctx, r); err != nil {
			t.Fatalf("Save %s: %v", r.TraceID, err)
		}
	}
	got, err := store.GetByProposalID(ctx, tt.ID, "p-tie")
	if err != nil || got.TraceID != "settled-p-tie" {
		t.Fatalf("by proposal = %s (%v), want the settled receipt", got.TraceID, err)
	}
	if d := got.CausalChain[len(got.CausalChain)-1].Data; d["role"] != "admin" || d["note"] != "Ask for photos." {
		t.Errorf("step data after the store = %v", d)
	}
}
