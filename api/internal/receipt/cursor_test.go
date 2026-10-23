package receipt_test

import (
	"context"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/receipt"
)

func TestMemoryDecisionCursor_RoundTripAndMonotonic(t *testing.T) {
	t.Parallel()
	c := receipt.NewMemoryDecisionCursor()
	ctx := context.Background()

	// Unset → zero time.
	if got, err := c.Through(ctx, "ten-1"); err != nil || !got.IsZero() {
		t.Fatalf("empty cursor: got %v err %v, want zero", got, err)
	}

	t1 := time.Date(2026, time.June, 15, 12, 0, 0, 0, time.UTC)
	if err := c.Advance(ctx, "ten-1", t1); err != nil {
		t.Fatalf("advance: %v", err)
	}
	if got, _ := c.Through(ctx, "ten-1"); !got.Equal(t1) {
		t.Errorf("through = %v, want %v", got, t1)
	}

	// Advancing backwards is a no-op (monotonic).
	if err := c.Advance(ctx, "ten-1", t1.Add(-time.Hour)); err != nil {
		t.Fatalf("advance back: %v", err)
	}
	if got, _ := c.Through(ctx, "ten-1"); !got.Equal(t1) {
		t.Errorf("through regressed to %v, want %v (monotonic)", got, t1)
	}

	// Forward advance moves it.
	t2 := t1.Add(time.Hour)
	_ = c.Advance(ctx, "ten-1", t2)
	if got, _ := c.Through(ctx, "ten-1"); !got.Equal(t2) {
		t.Errorf("through = %v, want %v", got, t2)
	}

	// Per-tenant isolation.
	if got, _ := c.Through(ctx, "ten-2"); !got.IsZero() {
		t.Errorf("ten-2 cursor = %v, want zero (isolated)", got)
	}
}
