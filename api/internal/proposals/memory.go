package proposals

import (
	"context"
	"sort"
	"sync"
	"time"
)

// MemoryReader is an in-memory Reader for unit tests of the GET
// /v1/proposals handler. Rows are stored in an internal map keyed
// by tenant id; ListDecisions returns the matching tenant's rows
// in DESC created_at order, capped at limit.
//
// MemoryReader is concurrent-safe so tests that drive the gate
// from multiple goroutines (e.g. cross-tenant isolation tests)
// don't race on the slice.
type MemoryReader struct {
	mu   sync.RWMutex
	rows map[string][]DecisionRow
}

// NewMemoryReader returns an empty in-memory reader. Callers seed
// rows via Append before driving the handler.
func NewMemoryReader() *MemoryReader {
	return &MemoryReader{rows: map[string][]DecisionRow{}}
}

// Append stores a pre-built row under tenantID. The row is taken
// by value; subsequent mutation of the caller's local variable
// does not affect the stored copy. Used by tests that want to
// pre-seed decisions without driving the proposal gate.
func (r *MemoryReader) Append(tenantID string, row DecisionRow) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rows[tenantID] = append(r.rows[tenantID], row)
}

// ListDecisions implements Reader. Returns the matching tenant's
// rows in DESC created_at order, capped at limit. Empty result
// is an empty slice, never nil.
func (r *MemoryReader) ListDecisions(_ context.Context, tenantID string, limit int) ([]DecisionRow, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if limit <= 0 {
		limit = DefaultLimit
	}
	src := r.rows[tenantID]
	out := make([]DecisionRow, len(src))
	copy(out, src)
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	if len(out) > limit {
		out = out[:limit]
	}
	if out == nil {
		out = []DecisionRow{}
	}
	return out, nil
}

// matchesWindow reports whether row satisfies q's action / control /
// environment / window predicates (limit is not a predicate).
func matchesWindow(row DecisionRow, q DecisionQuery) bool {
	if q.Action != "" && row.ActionName != q.Action {
		return false
	}
	if q.ControlID != "" && row.ControlID != q.ControlID {
		return false
	}
	if q.Environment != "" && row.Environment != q.Environment {
		return false
	}
	if q.Actor != "" && row.ActorID != q.Actor {
		return false
	}
	if !q.From.IsZero() && row.CreatedAt.Before(q.From) {
		return false
	}
	if !q.To.IsZero() && row.CreatedAt.After(q.To) {
		return false
	}
	if len(q.Outcomes) > 0 {
		match := false
		for _, o := range q.Outcomes {
			if row.Outcome == o {
				match = true
				break
			}
		}
		if !match {
			return false
		}
	}
	return true
}

// ListDecisionsInWindow implements Reader. Filters the tenant's
// rows by action / control / environment / window in memory (the
// store-layer equivalent the Postgres reader does in SQL), DESC
// created_at, capped at q.Limit.
func (r *MemoryReader) ListDecisionsInWindow(_ context.Context, tenantID string, q DecisionQuery) ([]DecisionRow, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	limit := q.Limit
	if limit <= 0 {
		limit = DefaultLimit
	}
	if limit > MaxLimit {
		limit = MaxLimit
	}
	out := make([]DecisionRow, 0)
	for _, row := range r.rows[tenantID] {
		if matchesWindow(row, q) {
			out = append(out, row)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	// Offset then limit (pagination). An offset past the end is an empty
	// page, not an error — the caller pages off the window-complete counts.
	if q.Offset > 0 {
		if q.Offset >= len(out) {
			return []DecisionRow{}, nil
		}
		out = out[q.Offset:]
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// CountDecisionsByOutcome implements Reader. Counts over the full
// window+control, ignoring q.Limit (the memory equivalent of a
// COUNT(*) ... GROUP BY outcome).
func (r *MemoryReader) CountDecisionsByOutcome(_ context.Context, tenantID string, q DecisionQuery) (map[string]int, int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	counts := map[string]int{}
	total := 0
	for _, row := range r.rows[tenantID] {
		if matchesWindow(row, q) {
			counts[row.Outcome]++
			total++
		}
	}
	return counts, total, nil
}

// CountDecisionsOverTime implements Reader. Buckets the matching
// rows into fixed-width intervals aligned to the bucket width (UTC),
// returning non-empty buckets in ascending time order — the in-memory
// equivalent of the Postgres to_timestamp(floor(...)) aggregation.
func (r *MemoryReader) CountDecisionsOverTime(_ context.Context, tenantID string, q DecisionQuery, bucket time.Duration) ([]TimeBucket, error) {
	secs := int64(bucket / time.Second)
	if secs <= 0 {
		secs = 1
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	byStart := map[int64]*TimeBucket{}
	order := []int64{}
	for _, row := range r.rows[tenantID] {
		if !matchesWindow(row, q) {
			continue
		}
		aligned := (row.CreatedAt.Unix() / secs) * secs
		tb, ok := byStart[aligned]
		if !ok {
			tb = &TimeBucket{Start: time.Unix(aligned, 0).UTC(), Counts: map[string]int{}}
			byStart[aligned] = tb
			order = append(order, aligned)
		}
		tb.Counts[row.Outcome]++
		tb.Total++
	}
	sort.Slice(order, func(i, j int) bool { return order[i] < order[j] })
	out := make([]TimeBucket, 0, len(order))
	for _, s := range order {
		out = append(out, *byStart[s])
	}
	return out, nil
}
