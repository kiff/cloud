package billing_test

import (
	"context"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/billing"
)

func TestMemoryUsageLedger_RecordIsIdempotent(t *testing.T) {
	t.Parallel()
	l := billing.NewMemoryUsageLedger()
	ev := billing.UsageEvent{
		OperationID: "prop-1:allowed",
		TenantID:    "ten-1",
		Outcome:     "allowed",
		OccurredAt:  time.Unix(1_700_000_000, 0),
	}

	recorded, err := l.Record(context.Background(), ev)
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	if !recorded {
		t.Fatal("first Record should report recorded=true")
	}

	// Same operation id again — idempotent no-op.
	recorded, err = l.Record(context.Background(), ev)
	if err != nil {
		t.Fatalf("Record (dup): %v", err)
	}
	if recorded {
		t.Error("duplicate Record should report recorded=false")
	}
	if l.Len() != 1 {
		t.Errorf("ledger len = %d, want 1 (duplicate must not add a row)", l.Len())
	}
}

func TestMemoryUsageLedger_DistinctOperationsAccumulate(t *testing.T) {
	t.Parallel()
	l := billing.NewMemoryUsageLedger()
	for _, id := range []string{"p1:allowed", "p2:blocked", "p3:approval_required"} {
		if _, err := l.Record(context.Background(), billing.UsageEvent{OperationID: id, TenantID: "ten-1", Outcome: "x"}); err != nil {
			t.Fatalf("Record %s: %v", id, err)
		}
	}
	if l.Len() != 3 {
		t.Errorf("ledger len = %d, want 3", l.Len())
	}
}

func TestMemoryUsageLedger_UnreportedOrderingAndMark(t *testing.T) {
	t.Parallel()
	l := billing.NewMemoryUsageLedger()
	// Insert out of time order; Unreported must return oldest first.
	_, _ = l.Record(context.Background(), billing.UsageEvent{OperationID: "b", TenantID: "t", OccurredAt: time.Unix(200, 0)})
	_, _ = l.Record(context.Background(), billing.UsageEvent{OperationID: "a", TenantID: "t", OccurredAt: time.Unix(100, 0)})

	got, err := l.Unreported(context.Background(), 0)
	if err != nil {
		t.Fatalf("Unreported: %v", err)
	}
	if len(got) != 2 || got[0].OperationID != "a" || got[1].OperationID != "b" {
		t.Fatalf("unreported order = %+v, want [a b] oldest-first", got)
	}

	// Mark one reported → it drops out of the unreported set.
	if err := l.MarkReported(context.Background(), []string{"a"}, time.Unix(300, 0)); err != nil {
		t.Fatalf("MarkReported: %v", err)
	}
	got, _ = l.Unreported(context.Background(), 0)
	if len(got) != 1 || got[0].OperationID != "b" {
		t.Errorf("after mark, unreported = %+v, want [b]", got)
	}

	// limit caps the result.
	_, _ = l.Record(context.Background(), billing.UsageEvent{OperationID: "c", TenantID: "t", OccurredAt: time.Unix(150, 0)})
	got, _ = l.Unreported(context.Background(), 1)
	if len(got) != 1 || got[0].OperationID != "c" {
		t.Errorf("limit=1 = %+v, want [c] (oldest unreported)", got)
	}
}
