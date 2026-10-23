package receipt_test

import (
	"context"
	"errors"
	"testing"
	"time"

	kiffaudit "github.com/kiff/kiff/pkg/kiff/audit"
	kiffruntime "github.com/kiff/kiff/pkg/kiff/runtime"

	"github.com/kiff/cloud/api/internal/anchor"
	"github.com/kiff/cloud/api/internal/receipt"
)

func mustWallet(t *testing.T) *anchor.Wallet {
	t.Helper()
	w, err := anchor.NewRandomWallet()
	if err != nil {
		t.Fatalf("wallet: %v", err)
	}
	return w
}

func fixedNow() func() time.Time {
	return func() time.Time { return time.Date(2026, time.June, 15, 12, 0, 5, 0, time.UTC) }
}

func newCursorPipeline(t *testing.T, cursor receipt.DecisionCursor) (*receipt.Pipeline, *receipt.MemoryStore) {
	t.Helper()
	store := receipt.NewMemoryStore()
	pl, err := receipt.NewPipeline(store, receipt.NewLocalAttester(mustWallet(t), fixedNow()), receipt.WithDecisionCursor(cursor))
	if err != nil {
		t.Fatalf("pipeline: %v", err)
	}
	return pl, store
}

// With a cursor wired, a clean sweep advances the watermark to the newest
// decision; a second sweep then skips the already-reconciled history (it
// mints nothing) — the steady-state win.
func TestReconcileDecisions_CursorAdvancesAndSkips(t *testing.T) {
	t.Parallel()
	cursor := receipt.NewMemoryDecisionCursor()
	pl, store := newCursorPipeline(t, cursor)
	ctx := context.Background()
	t0 := time.Date(2026, time.June, 15, 12, 0, 0, 0, time.UTC)

	audit := newAuditStoreWith(t, []kiffaudit.Record{
		proposalDecisionRecord("p1", "allowed", t0),
		proposalDecisionRecord("p2", "blocked", t0.Add(time.Second)),
	})
	rt := &kiffruntime.Runtime{Audit: audit}

	built, err := pl.ReconcileDecisions(ctx, "ten-1", rt)
	if err != nil {
		t.Fatalf("first sweep: %v", err)
	}
	if built != 2 {
		t.Fatalf("first sweep built = %d, want 2", built)
	}
	// Watermark advanced to the newest decision time.
	through, _ := cursor.Through(ctx, "ten-1")
	if !through.Equal(t0.Add(time.Second)) {
		t.Errorf("watermark = %v, want %v", through, t0.Add(time.Second))
	}

	// Second sweep over the same audit: old rows are strictly before the
	// mark (skipped) and the at-mark row is re-checked (idempotent no-op),
	// so nothing is re-minted.
	built, err = pl.ReconcileDecisions(ctx, "ten-1", rt)
	if err != nil {
		t.Fatalf("second sweep: %v", err)
	}
	if built != 0 {
		t.Errorf("second sweep built = %d, want 0 (skipped via watermark)", built)
	}

	// A NEW decision after the mark is still picked up.
	if err := audit.Append(ctx, proposalDecisionRecord("p3", "allowed", t0.Add(time.Hour))); err != nil {
		t.Fatalf("append: %v", err)
	}
	built, err = pl.ReconcileDecisions(ctx, "ten-1", rt)
	if err != nil {
		t.Fatalf("third sweep: %v", err)
	}
	if built != 1 {
		t.Errorf("third sweep built = %d, want 1 (new decision after mark)", built)
	}
	if _, err := store.Get(ctx, "ten-1", receipt.DecisionReceiptTraceID("p3")); err != nil {
		t.Errorf("new decision receipt not minted: %v", err)
	}
}

// A record whose CreatedAt is exactly the watermark must NOT be skipped:
// it is re-checked each sweep (idempotent via dedup), so a later audit
// record sharing the mark's exact timestamp is still minted — the
// timestamp-only boundary never silently drops a same-timestamp decision.
func TestReconcileDecisions_RecordAtWatermarkNotSkipped(t *testing.T) {
	t.Parallel()
	cursor := receipt.NewMemoryDecisionCursor()
	pl, store := newCursorPipeline(t, cursor)
	ctx := context.Background()
	t0 := time.Date(2026, time.June, 15, 12, 0, 0, 0, time.UTC)
	t1 := t0.Add(time.Second)

	audit := newAuditStoreWith(t, []kiffaudit.Record{
		proposalDecisionRecord("p1", "allowed", t0),
		proposalDecisionRecord("p2", "allowed", t1),
	})
	rt := &kiffruntime.Runtime{Audit: audit}

	if _, err := pl.ReconcileDecisions(ctx, "ten-1", rt); err != nil {
		t.Fatalf("first sweep: %v", err)
	}
	if through, _ := cursor.Through(ctx, "ten-1"); !through.Equal(t1) {
		t.Fatalf("watermark = %v, want %v", through, t1)
	}

	// A new decision sharing the watermark's EXACT timestamp.
	if err := audit.Append(ctx, proposalDecisionRecord("p3", "allowed", t1)); err != nil {
		t.Fatalf("append: %v", err)
	}

	built, err := pl.ReconcileDecisions(ctx, "ten-1", rt)
	if err != nil {
		t.Fatalf("second sweep: %v", err)
	}
	if built != 1 {
		t.Errorf("built = %d, want 1 (the same-timestamp record at the mark must be minted, not skipped)", built)
	}
	if _, err := store.Get(ctx, "ten-1", receipt.DecisionReceiptTraceID("p3")); err != nil {
		t.Errorf("same-timestamp decision p3 was skipped (no receipt): %v", err)
	}
	// p2 (also at the mark) is re-checked but not double-minted; idempotent.
	if _, err := store.Get(ctx, "ten-1", receipt.DecisionReceiptTraceID("p2")); err != nil {
		t.Errorf("p2 receipt missing: %v", err)
	}
}

type recordingCursor struct {
	through  time.Time
	advanced []time.Time
}

func (c *recordingCursor) Through(_ context.Context, _ string) (time.Time, error) {
	return c.through, nil
}
func (c *recordingCursor) Advance(_ context.Context, _ string, through time.Time) error {
	c.advanced = append(c.advanced, through)
	return nil
}

// failingStore wraps a MemoryStore and fails Save for one trace id, to
// simulate a transient mint failure that must hold the watermark.
type failingSaveStore struct {
	*receipt.MemoryStore
	failTrace string
}

func (s *failingSaveStore) Save(ctx context.Context, r receipt.Receipt) error {
	if r.TraceID == s.failTrace {
		return errors.New("simulated transient save failure")
	}
	return s.MemoryStore.Save(ctx, r)
}

// A transient failure on any in-window decision must NOT advance the
// watermark, so the next sweep re-attempts from the same mark (self-healing).
func TestReconcileDecisions_FailureHoldsWatermark(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	t0 := time.Date(2026, time.June, 15, 12, 0, 0, 0, time.UTC)

	mem := receipt.NewMemoryStore()
	store := &failingSaveStore{MemoryStore: mem, failTrace: receipt.DecisionReceiptTraceID("p-fail")}
	cursor := &recordingCursor{} // starts at zero
	pl, err := receipt.NewPipeline(store, receipt.NewLocalAttester(mustWallet(t), fixedNow()), receipt.WithDecisionCursor(cursor))
	if err != nil {
		t.Fatalf("pipeline: %v", err)
	}

	rt := &kiffruntime.Runtime{Audit: newAuditStoreWith(t, []kiffaudit.Record{
		proposalDecisionRecord("p-ok", "allowed", t0),
		proposalDecisionRecord("p-fail", "allowed", t0.Add(time.Second)),
	})}

	_, _ = pl.ReconcileDecisions(ctx, "ten-1", rt)
	if len(cursor.advanced) != 0 {
		t.Errorf("watermark advanced despite a failed mint: %v", cursor.advanced)
	}
}

// No cursor wired → ReconcileDecisions still mints everything (unchanged
// behavior), proving the cursor is purely additive.
func TestReconcileDecisions_NilCursorFullPass(t *testing.T) {
	t.Parallel()
	pl, store, _ := newLocalPipeline(t) // no cursor
	ctx := context.Background()
	t0 := time.Date(2026, time.June, 15, 12, 0, 0, 0, time.UTC)
	rt := &kiffruntime.Runtime{Audit: newAuditStoreWith(t, []kiffaudit.Record{
		proposalDecisionRecord("p1", "allowed", t0),
	})}
	built, err := pl.ReconcileDecisions(ctx, "ten-1", rt)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if built != 1 {
		t.Fatalf("built = %d, want 1", built)
	}
	if _, err := store.Get(ctx, "ten-1", receipt.DecisionReceiptTraceID("p1")); err != nil {
		t.Errorf("receipt not minted on nil-cursor pass: %v", err)
	}
}
