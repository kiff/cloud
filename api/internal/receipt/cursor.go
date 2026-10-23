package receipt

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed sql/decision_cursors.sql
var decisionCursorsSQL string

// DecisionCursorSQL returns the DDL for cloud_meta.guard_decision_cursors.
func DecisionCursorSQL() string { return decisionCursorsSQL }

// ApplyDecisionCursorSchema runs the cursor DDL against pool. Idempotent.
// Call at startup after tenant.ApplyCloudMeta (the FK requires
// cloud_meta.tenants).
func ApplyDecisionCursorSchema(ctx context.Context, pool *pgxpool.Pool) error {
	if _, err := pool.Exec(ctx, decisionCursorsSQL); err != nil {
		return fmt.Errorf("apply decision cursor schema: %w", err)
	}
	return nil
}

// DecisionCursor persists the per-tenant high-water mark for the
// decision-receipt sweep. Through returns the CreatedAt of the
// newest decision reconciled in a clean sweep (zero when none recorded);
// the sweep skips decisions strictly before it (records exactly at it are
// re-checked, see ReconcileDecisions). Advance moves the mark forward. It
// is an optimization over the receipt-existence dedup, never the sole
// correctness mechanism — the sweep still self-heals if the cursor is
// stale or absent.
type DecisionCursor interface {
	Through(ctx context.Context, tenantID string) (time.Time, error)
	Advance(ctx context.Context, tenantID string, through time.Time) error
}

// MemoryDecisionCursor is the in-process cursor for tests.
type MemoryDecisionCursor struct {
	mu   sync.Mutex
	mark map[string]time.Time
}

// NewMemoryDecisionCursor returns an empty in-memory cursor.
func NewMemoryDecisionCursor() *MemoryDecisionCursor {
	return &MemoryDecisionCursor{mark: map[string]time.Time{}}
}

func (m *MemoryDecisionCursor) Through(_ context.Context, tenantID string) (time.Time, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.mark[tenantID], nil
}

func (m *MemoryDecisionCursor) Advance(_ context.Context, tenantID string, through time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	// Monotonic: never move the mark backwards.
	if through.After(m.mark[tenantID]) {
		m.mark[tenantID] = through
	}
	return nil
}

// PostgresDecisionCursor is the cloud_meta-backed cursor.
type PostgresDecisionCursor struct {
	pool *pgxpool.Pool
}

// NewPostgresDecisionCursor wraps the cloud pool. The caller owns the
// pool's lifetime and must have run ApplyDecisionCursorSchema.
func NewPostgresDecisionCursor(pool *pgxpool.Pool) *PostgresDecisionCursor {
	return &PostgresDecisionCursor{pool: pool}
}

const sqlGetDecisionCursor = `
SELECT reconciled_through
FROM cloud_meta.guard_decision_cursors
WHERE tenant_id = $1
`

// sqlAdvanceDecisionCursor upserts the mark, never moving it backwards
// (GREATEST), so a slow/late sweep can't regress a newer mark.
const sqlAdvanceDecisionCursor = `
INSERT INTO cloud_meta.guard_decision_cursors (tenant_id, reconciled_through, updated_at)
VALUES ($1, $2, NOW())
ON CONFLICT (tenant_id) DO UPDATE SET
    reconciled_through = GREATEST(cloud_meta.guard_decision_cursors.reconciled_through, EXCLUDED.reconciled_through),
    updated_at = NOW()
`

func (p *PostgresDecisionCursor) Through(ctx context.Context, tenantID string) (time.Time, error) {
	var t time.Time
	err := p.pool.QueryRow(ctx, sqlGetDecisionCursor, tenantID).Scan(&t)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return time.Time{}, nil
		}
		return time.Time{}, fmt.Errorf("get decision cursor: %w", err)
	}
	return t, nil
}

func (p *PostgresDecisionCursor) Advance(ctx context.Context, tenantID string, through time.Time) error {
	if _, err := p.pool.Exec(ctx, sqlAdvanceDecisionCursor, tenantID, through.UTC()); err != nil {
		return fmt.Errorf("advance decision cursor: %w", err)
	}
	return nil
}
