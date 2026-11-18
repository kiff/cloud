package billing

import (
	"context"
	"sort"
	"sync"
	"time"
)

// Governed-operation outcomes recorded in the usage ledger. All three
// are recorded (the outcome dimension is retained for analytics and
// future rate weighting), but they are not billed alike.
const (
	OutcomeAllowed          = "allowed"
	OutcomeBlocked          = "blocked"
	OutcomeApprovalRequired = "approval_required"
)

// MeterableOutcome reports whether a recorded governed-operation
// outcome is emitted to the Stripe usage meter as productive
// consumption. Per the permanent-usage rule (2026-07-02),
// only `allowed` — the productive-usage signal — is metered.
// `blocked` and `approval_required` are still recorded in the ledger
// (so blocked/approval volume stays queryable per tenant and can
// inform future weighting) but are folded into the platform fee, never
// emitted as full-rate meter events — a customer must not be billed
// more precisely when they are under attack.
func MeterableOutcome(outcome string) bool {
	return outcome == OutcomeAllowed
}

// UsageLedger is the durable, per-operation record of governed
// operations — the canonical source the Stripe meter-events reporter
// drains. It is deliberately separate from the in-memory usage
// counters (kiff.cloud.proposals.*): the counters are hourly
// aggregates for dashboards and the limiter; the ledger is one
// idempotent row per governed operation, so a meter event can be
// emitted exactly once per operation and a transport retry can never
// double-bill.
//
// A "governed operation" is one evaluated proposal that produced a
// terminal governance outcome (allowed / blocked / approval_required),
// and all three are recorded here so blocked/approval volume stays
// queryable per tenant. Only `allowed` is metered to Stripe as
// productive consumption, though: `blocked` / `approval_required` are
// recorded but folded into the platform fee (see MeterableOutcome and
// the permanent-usage rule). Malformed (invalid) and budget
// (limit_exceeded) outcomes are NOT governed operations and are never
// recorded here — they are not production work KIFF performed against
// the contract.
//
// Recording is best-effort and off the decision path: a ledger write
// failure must never change or block a runtime decision. The
// conservative direction on failure is to under-count, never to
// double-bill or to turn billing into an outage.
type UsageLedger interface {
	// Record persists one billable operation. It is idempotent on
	// OperationID: a repeat (e.g. the reporter or caller retrying)
	// is a no-op. Returns recorded=false when the operation was
	// already present.
	Record(ctx context.Context, ev UsageEvent) (recorded bool, err error)
}

// UsageLedgerDrainer is the read/mark side of the ledger, consumed by
// the Stripe meter-events reporter. Kept separate from
// UsageLedger so the proposal handler depends only on the write path.
type UsageLedgerDrainer interface {
	// Unreported returns up to limit billable operations that have
	// not yet been reported (reported_at IS NULL), oldest first, so
	// the reporter drains in occurrence order.
	Unreported(ctx context.Context, limit int) ([]UsageEvent, error)
	// MarkReported stamps reported_at on the given operations so they
	// are not drained again. Idempotent: already-stamped rows are
	// left as-is.
	MarkReported(ctx context.Context, operationIDs []string, at time.Time) error
}

// UsageEvent is one billable governed operation. OperationID is the
// decision's stable identity (the proposal's decision-record id,
// "<proposal id>:<outcome>"), so one recorded decision maps to one
// billable row and the same identity dedupes a re-record.
type UsageEvent struct {
	OperationID string
	TenantID    string
	Outcome     string
	OccurredAt  time.Time
}

// MemoryUsageLedger is an in-process UsageLedger for tests and
// single-node dev. Safe for concurrent use.
type MemoryUsageLedger struct {
	mu     sync.Mutex
	events map[string]*memUsageRow // keyed by OperationID
}

type memUsageRow struct {
	ev         UsageEvent
	reportedAt time.Time // zero = unreported
}

// NewMemoryUsageLedger returns an empty in-memory ledger.
func NewMemoryUsageLedger() *MemoryUsageLedger {
	return &MemoryUsageLedger{events: make(map[string]*memUsageRow)}
}

// Record implements UsageLedger.
func (m *MemoryUsageLedger) Record(_ context.Context, ev UsageEvent) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.events[ev.OperationID]; ok {
		return false, nil
	}
	m.events[ev.OperationID] = &memUsageRow{ev: ev}
	return true, nil
}

// Unreported implements UsageLedgerDrainer: unreported rows, oldest
// first, capped at limit (limit<=0 means no cap).
func (m *MemoryUsageLedger) Unreported(_ context.Context, limit int) ([]UsageEvent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []UsageEvent
	for _, row := range m.events {
		if row.reportedAt.IsZero() {
			out = append(out, row.ev)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].OccurredAt.Before(out[j].OccurredAt) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// MarkReported implements UsageLedgerDrainer.
func (m *MemoryUsageLedger) MarkReported(_ context.Context, operationIDs []string, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, id := range operationIDs {
		if row, ok := m.events[id]; ok && row.reportedAt.IsZero() {
			row.reportedAt = at
		}
	}
	return nil
}

// Len reports how many distinct operations are recorded (test helper).
func (m *MemoryUsageLedger) Len() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.events)
}
