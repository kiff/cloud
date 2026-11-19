package posture

import (
	"context"
	"fmt"
	"sync"

	"github.com/jackc/pgx/v5"

	"github.com/kiff/cloud/api/internal/tenant"
)

// Reader reads a tenant's posture-change transitions, oldest-first.
// Tenant-scoped at the call boundary: the implementation guarantees no
// other tenant's transitions can be returned.
type Reader interface {
	// Transitions returns the posture changes recorded for tenantID,
	// oldest-first. Empty slice (not nil) when none.
	Transitions(ctx context.Context, tenantID string) ([]Transition, error)
}

// MemoryReader is the in-process Reader for tests and no-Postgres
// runs. Concurrent-safe.
type MemoryReader struct {
	mu       sync.RWMutex
	byTenant map[string][]Transition
}

// NewMemoryReader returns an empty in-memory reader.
func NewMemoryReader() *MemoryReader {
	return &MemoryReader{byTenant: map[string][]Transition{}}
}

// Append seeds a transition for tenantID (test helper / no-PG writer).
func (m *MemoryReader) Append(tenantID string, tr Transition) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.byTenant[tenantID] = append(m.byTenant[tenantID], tr)
}

// Transitions implements Reader.
func (m *MemoryReader) Transitions(_ context.Context, tenantID string) ([]Transition, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := append([]Transition(nil), m.byTenant[tenantID]...)
	sortTransitions(out)
	return out, nil
}

// PostgresReader reads posture_changed records from the per-tenant
// kiff_t_<id>.kiff_audit table.
//
// Tenant safety: the query checks out a connection from
// tenant.SchemaPool whose search_path is pinned to kiff_t_<tenantID>,
// so "FROM kiff_audit" (unqualified) resolves to the caller tenant's
// schema only. Cross-tenant reads are mechanically impossible here,
// the same property proposals.PostgresReader relies on.
type PostgresReader struct {
	pool *tenant.SchemaPool
}

// NewPostgresReader wraps a tenant.SchemaPool. Returns nil when pool is
// nil so callers can branch on "reader available."
func NewPostgresReader(pool *tenant.SchemaPool) *PostgresReader {
	if pool == nil {
		return nil
	}
	return &PostgresReader{pool: pool}
}

const selectTransitionsSQL = `
	SELECT data->>'` + dataFrom + `',
	       data->>'` + dataTo + `',
	       COALESCE(data->>'` + dataChangedBy + `', actor_id, ''),
	       created_at
	FROM kiff_audit
	WHERE kind = $1
	ORDER BY created_at, inserted_at, id
`

// Transitions implements Reader.
func (r *PostgresReader) Transitions(ctx context.Context, tenantID string) ([]Transition, error) {
	out := make([]Transition, 0)
	err := r.pool.WithTenantConn(ctx, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, selectTransitionsSQL, Kind)
		if err != nil {
			return fmt.Errorf("query posture transitions: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var tr Transition
			var from, to *string
			if err := rows.Scan(&from, &to, &tr.ChangedBy, &tr.ChangedAt); err != nil {
				return fmt.Errorf("scan posture transition: %w", err)
			}
			if from != nil {
				tr.From = *from
			}
			if to != nil {
				tr.To = *to
			}
			tr.ChangedAt = tr.ChangedAt.UTC()
			out = append(out, tr)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
