package usage_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	kiffruntime "github.com/kiff/kiff/pkg/kiff/runtime"

	"github.com/kiff/cloud/api/internal/usage"
)

func TestRecorderForwardsToAggregator(t *testing.T) {
	t.Parallel()

	store := usage.NewMemoryStore()
	agg := usage.NewAggregator(store, usage.Options{Interval: time.Hour})

	rec := agg.RecorderFor("ten-1")
	rec.Inc("kiff.events.ingested", 1, kiffruntime.EntityType("Order"))
	rec.Inc("kiff.events.ingested", 2, kiffruntime.EntityType("Order"))
	rec.Inc("kiff.actions.executed", 1, kiffruntime.EntityType("Ticket"))

	if err := agg.FlushNow(context.Background()); err != nil {
		t.Fatalf("FlushNow: %v", err)
	}

	got, err := store.Snapshot(context.Background(), usage.Filter{TenantID: "ten-1"})
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	want := map[string]uint64{
		"kiff.events.ingested|Order":   3,
		"kiff.actions.executed|Ticket": 1,
	}
	if got, want := len(got), len(want); got != want {
		t.Fatalf("snapshot rows: got %d, want %d", got, want)
	}
	for _, c := range got {
		key := c.CounterName + "|" + c.EntityType
		if c.Count != want[key] {
			t.Errorf("%s: got %d, want %d", key, c.Count, want[key])
		}
	}
}

func TestRecorderTagsByTenant(t *testing.T) {
	t.Parallel()

	store := usage.NewMemoryStore()
	agg := usage.NewAggregator(store, usage.Options{Interval: time.Hour})

	rA := agg.RecorderFor("ten-A")
	rB := agg.RecorderFor("ten-B")
	rA.Inc("kiff.events.ingested", 5, kiffruntime.EntityType("Order"))
	rB.Inc("kiff.events.ingested", 2, kiffruntime.EntityType("Order"))

	if err := agg.FlushNow(context.Background()); err != nil {
		t.Fatalf("FlushNow: %v", err)
	}

	a, _ := store.Snapshot(context.Background(), usage.Filter{TenantID: "ten-A"})
	b, _ := store.Snapshot(context.Background(), usage.Filter{TenantID: "ten-B"})
	if len(a) != 1 || a[0].Count != 5 {
		t.Fatalf("ten-A: got %+v, want one row count=5", a)
	}
	if len(b) != 1 || b[0].Count != 2 {
		t.Fatalf("ten-B: got %+v, want one row count=2", b)
	}
}

func TestRecorderUnknownEntityType(t *testing.T) {
	t.Parallel()

	store := usage.NewMemoryStore()
	agg := usage.NewAggregator(store, usage.Options{Interval: time.Hour})

	rec := agg.RecorderFor("ten-1")
	// No entity_type attribute; aggregator should bucket as
	// "unknown" rather than create a wildcard row.
	rec.Inc("kiff.events.ingested", 1)

	if err := agg.FlushNow(context.Background()); err != nil {
		t.Fatalf("FlushNow: %v", err)
	}
	rows, _ := store.Snapshot(context.Background(), usage.Filter{TenantID: "ten-1"})
	if len(rows) != 1 {
		t.Fatalf("rows: got %d, want 1", len(rows))
	}
	if rows[0].EntityType != "unknown" {
		t.Errorf("entity_type: got %q, want %q", rows[0].EntityType, "unknown")
	}
}

func TestHourBucketing(t *testing.T) {
	t.Parallel()

	store := usage.NewMemoryStore()

	now := time.Date(2026, 5, 24, 10, 0, 0, 0, time.UTC)
	clock := now
	agg := usage.NewAggregator(store, usage.Options{
		Interval: time.Hour,
		Now:      func() time.Time { return clock },
	})

	rec := agg.RecorderFor("ten-1")
	rec.Inc("kiff.events.ingested", 1, kiffruntime.EntityType("Order"))

	// 30 minutes later — same bucket.
	clock = now.Add(30 * time.Minute)
	rec.Inc("kiff.events.ingested", 1, kiffruntime.EntityType("Order"))

	// 65 minutes later — fresh bucket.
	clock = now.Add(65 * time.Minute)
	rec.Inc("kiff.events.ingested", 1, kiffruntime.EntityType("Order"))

	if err := agg.FlushNow(context.Background()); err != nil {
		t.Fatalf("FlushNow: %v", err)
	}

	rows, _ := store.Snapshot(context.Background(), usage.Filter{TenantID: "ten-1"})
	if len(rows) != 2 {
		t.Fatalf("rows: got %d, want 2 (hour-A=2, hour-B=1)", len(rows))
	}
	// Snapshot is sorted by hour; first row is the older hour.
	if rows[0].Count != 2 || rows[1].Count != 1 {
		t.Fatalf("counts: got [%d, %d], want [2, 1]", rows[0].Count, rows[1].Count)
	}
}

func TestConcurrentInc(t *testing.T) {
	t.Parallel()

	store := usage.NewMemoryStore()
	agg := usage.NewAggregator(store, usage.Options{Interval: time.Hour})
	rec := agg.RecorderFor("ten-1")

	const goroutines = 8
	const each = 100
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func() {
			defer wg.Done()
			for i := 0; i < each; i++ {
				rec.Inc("kiff.events.ingested", 1, kiffruntime.EntityType("Order"))
			}
		}()
	}
	wg.Wait()

	if err := agg.FlushNow(context.Background()); err != nil {
		t.Fatalf("FlushNow: %v", err)
	}
	rows, _ := store.Snapshot(context.Background(), usage.Filter{TenantID: "ten-1"})
	if len(rows) != 1 {
		t.Fatalf("rows: got %d, want 1", len(rows))
	}
	if want := uint64(goroutines * each); rows[0].Count != want {
		t.Fatalf("count: got %d, want %d", rows[0].Count, want)
	}
}

// flippingStore is a Store that fails Flush until Heal() is called.
// It composes a working MemoryStore so a healed Flush actually
// persists, which is what the restore test needs.
type flippingStore struct {
	failing atomic.Bool
	real    *usage.MemoryStore
}

func newFlippingStore() *flippingStore {
	s := &flippingStore{real: usage.NewMemoryStore()}
	s.failing.Store(true)
	return s
}

func (s *flippingStore) Heal() { s.failing.Store(false) }

func (s *flippingStore) Flush(ctx context.Context, c []usage.Counter) error {
	if s.failing.Load() {
		return context.DeadlineExceeded
	}
	return s.real.Flush(ctx, c)
}

func (s *flippingStore) Snapshot(ctx context.Context, f usage.Filter) ([]usage.Counter, error) {
	return s.real.Snapshot(ctx, f)
}

func TestFlushFailureRestoresState(t *testing.T) {
	t.Parallel()

	store := newFlippingStore()
	agg := usage.NewAggregator(store, usage.Options{Interval: time.Hour})
	rec := agg.RecorderFor("ten-1")
	rec.Inc("kiff.events.ingested", 3, kiffruntime.EntityType("Order"))

	// First flush hits the failing store. Aggregator must restore
	// the in-memory state so a retry sees the same numbers.
	if err := agg.FlushNow(context.Background()); err == nil {
		t.Fatal("expected failing-store error from FlushNow")
	}

	// Heal the store and flush again. The restored 3 must drain.
	store.Heal()
	if err := agg.FlushNow(context.Background()); err != nil {
		t.Fatalf("FlushNow after heal: %v", err)
	}

	rows, _ := store.Snapshot(context.Background(), usage.Filter{TenantID: "ten-1"})
	if len(rows) != 1 || rows[0].Count != 3 {
		t.Fatalf("restored count not drained: got %+v, want one row count=3", rows)
	}
}

func TestFlushFailureMergesConcurrentInc(t *testing.T) {
	t.Parallel()

	// Tighter property: an Inc that arrives between a failed flush
	// and the restore must not be lost.
	store := newFlippingStore()
	agg := usage.NewAggregator(store, usage.Options{Interval: time.Hour})
	rec := agg.RecorderFor("ten-1")
	rec.Inc("kiff.events.ingested", 3, kiffruntime.EntityType("Order"))

	// Flush fails — restore puts the 3 back.
	if err := agg.FlushNow(context.Background()); err == nil {
		t.Fatal("expected failing-store error from FlushNow")
	}

	// New increments after the failure.
	rec.Inc("kiff.events.ingested", 2, kiffruntime.EntityType("Order"))

	// Heal, flush, expect 5 (3 restored + 2 new).
	store.Heal()
	if err := agg.FlushNow(context.Background()); err != nil {
		t.Fatalf("FlushNow after heal: %v", err)
	}
	rows, _ := store.Snapshot(context.Background(), usage.Filter{TenantID: "ten-1"})
	if len(rows) != 1 || rows[0].Count != 5 {
		t.Fatalf("restore lost a concurrent inc: got %+v, want one row count=5", rows)
	}
}

func TestStopFlushesPending(t *testing.T) {
	t.Parallel()

	store := usage.NewMemoryStore()
	agg := usage.NewAggregator(store, usage.Options{Interval: time.Hour})
	rec := agg.RecorderFor("ten-1")
	rec.Inc("kiff.events.ingested", 7, kiffruntime.EntityType("Order"))

	// Start, then immediately stop. Stop must drain the pending
	// counter rather than letting it sit forever.
	agg.Start(context.Background())
	if err := agg.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	rows, _ := store.Snapshot(context.Background(), usage.Filter{TenantID: "ten-1"})
	if len(rows) != 1 || rows[0].Count != 7 {
		t.Fatalf("Stop did not flush: %+v", rows)
	}
}
