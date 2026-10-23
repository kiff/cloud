package receipt_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/receipt"
)

// fixtureReceipt constructs a deterministic receipt for store
// tests. tenant and trace are the only knobs the test cares
// about; everything else is filled in.
func fixtureReceipt(tenantID, traceID string) receipt.Receipt {
	return receipt.Receipt{
		SchemaVersion: receipt.SchemaVersion,
		TenantID:      tenantID,
		TraceID:       traceID,
		EntityID:      "order-001",
		EntityType:    "Order",
		ActionName:    "MARK_PAID",
		IssuedAt:      time.Date(2026, time.May, 24, 12, 0, 0, 0, time.UTC),
		CausalChain: []receipt.ChainStep{
			{Sequence: 1, Kind: "event_ingested", DataHash: "deadbeef", OccurredAt: time.Date(2026, time.May, 24, 12, 0, 0, 0, time.UTC)},
			{Sequence: 2, Kind: "action_executed", DataHash: "cafebabe", OccurredAt: time.Date(2026, time.May, 24, 12, 0, 0, 1, time.UTC)},
		},
		ComplianceMap: refundDomainCompliance(),
	}
}

func TestMemoryStore_RoundTrip(t *testing.T) {
	t.Parallel()

	store := receipt.NewMemoryStore()
	r := fixtureReceipt("ten-1", "trace-1")
	if err := store.Save(context.Background(), r); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := store.Get(context.Background(), "ten-1", "trace-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ActionName != "MARK_PAID" {
		t.Errorf("ActionName: got %q", got.ActionName)
	}
	if got, want := len(got.CausalChain), 2; got != want {
		t.Errorf("CausalChain length: got %d, want %d", got, want)
	}
}

func TestMemoryStore_GetMissingReturnsNotFound(t *testing.T) {
	t.Parallel()

	store := receipt.NewMemoryStore()
	_, err := store.Get(context.Background(), "ten-1", "trace-missing")
	if !errors.Is(err, receipt.ErrReceiptNotFound) {
		t.Fatalf("err: got %v, want ErrReceiptNotFound", err)
	}
}

func TestMemoryStore_SaveUpserts(t *testing.T) {
	t.Parallel()

	// Saving twice for the same (tenant, trace) replaces the row.
	// This is the upsert path the anchoring slice will rely on:
	// save the unanchored draft, then save again with the
	// attestation populated after the next anchor cycle.
	store := receipt.NewMemoryStore()
	r := fixtureReceipt("ten-1", "trace-1")
	if err := store.Save(context.Background(), r); err != nil {
		t.Fatalf("Save #1: %v", err)
	}

	r.Attestation = &receipt.Attestation{
		Algorithm:     "secp256k1+keccak256",
		Signature:     "0xsignature",
		WalletAddress: "0xwallet",
		AnchorTxHash:  "0xtx",
		AnchoredAt:    time.Date(2026, time.May, 24, 13, 0, 0, 0, time.UTC),
	}
	if err := store.Save(context.Background(), r); err != nil {
		t.Fatalf("Save #2: %v", err)
	}

	got, err := store.Get(context.Background(), "ten-1", "trace-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Attestation == nil {
		t.Fatal("Attestation: got nil after upsert")
	}
	if got.Attestation.AnchorTxHash != "0xtx" {
		t.Errorf("AnchorTxHash: got %q", got.Attestation.AnchorTxHash)
	}
}

func TestMemoryStore_ListUnanchoredFiltersByTenantAndAttestation(t *testing.T) {
	t.Parallel()

	store := receipt.NewMemoryStore()

	// Mix of (tenant, anchored?) so we can confirm both filters.
	r1 := fixtureReceipt("ten-1", "trace-1")
	r1.IssuedAt = time.Date(2026, time.May, 24, 12, 0, 0, 0, time.UTC)
	r2 := fixtureReceipt("ten-1", "trace-2")
	r2.IssuedAt = time.Date(2026, time.May, 24, 12, 5, 0, 0, time.UTC)
	r2.Attestation = &receipt.Attestation{AnchorTxHash: "0xtx", AnchoredAt: r2.IssuedAt}
	r3 := fixtureReceipt("ten-2", "trace-3")
	r3.IssuedAt = time.Date(2026, time.May, 24, 12, 1, 0, 0, time.UTC)

	for _, r := range []receipt.Receipt{r1, r2, r3} {
		if err := store.Save(context.Background(), r); err != nil {
			t.Fatalf("Save %s: %v", r.TraceID, err)
		}
	}

	got, err := store.ListUnanchored(context.Background(), "ten-1")
	if err != nil {
		t.Fatalf("ListUnanchored: %v", err)
	}
	// ten-1 has one unanchored (trace-1); trace-2 is attested,
	// trace-3 belongs to ten-2.
	if len(got) != 1 {
		t.Fatalf("rows: got %d, want 1", len(got))
	}
	if got[0].TraceID != "trace-1" {
		t.Errorf("trace: got %q, want %q", got[0].TraceID, "trace-1")
	}
}

func TestMemoryStore_RejectsInvalidSave(t *testing.T) {
	t.Parallel()

	store := receipt.NewMemoryStore()

	cases := []struct {
		name string
		r    receipt.Receipt
	}{
		{"empty tenant", receipt.Receipt{TraceID: "t", SchemaVersion: "v0.1", EntityID: "e", IssuedAt: time.Now()}},
		{"empty trace", receipt.Receipt{TenantID: "ten", SchemaVersion: "v0.1", EntityID: "e", IssuedAt: time.Now()}},
		{"empty schema", receipt.Receipt{TenantID: "ten", TraceID: "t", EntityID: "e", IssuedAt: time.Now()}},
		{"empty entity", receipt.Receipt{TenantID: "ten", TraceID: "t", SchemaVersion: "v0.1", IssuedAt: time.Now()}},
		{"zero issued_at", receipt.Receipt{TenantID: "ten", TraceID: "t", SchemaVersion: "v0.1", EntityID: "e"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := store.Save(context.Background(), tc.r); err == nil {
				t.Fatal("expected error from Save")
			}
		})
	}
}

func TestMemoryStore_GetReturnsCloneNotAlias(t *testing.T) {
	t.Parallel()

	// Mutating the slice on a returned receipt must not affect the
	// stored row. The store has to clone on read; if it doesn't, a
	// caller's append could mutate every other reader's view.
	store := receipt.NewMemoryStore()
	r := fixtureReceipt("ten-1", "trace-1")
	if err := store.Save(context.Background(), r); err != nil {
		t.Fatalf("Save: %v", err)
	}

	first, _ := store.Get(context.Background(), "ten-1", "trace-1")
	first.CausalChain = append(first.CausalChain, receipt.ChainStep{Sequence: 99, Kind: "tampered"})

	second, _ := store.Get(context.Background(), "ten-1", "trace-1")
	for _, step := range second.CausalChain {
		if step.Kind == "tampered" {
			t.Fatal("Get returned an aliased slice")
		}
	}
}
