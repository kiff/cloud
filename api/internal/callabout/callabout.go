// Package callabout keeps what each call is about, in the taf's own words:
// a business reference such as "Order 1042", from the gateway's kiff_about.
// It is kept beside the call's decision, never in it: the decision's record
// is what a receipt's chain hashes, and a reference that slipped personal
// data past the cleaning and the review must be erasable without breaking
// any receipt's verification. A row goes when it's erased, and with the
// account (tenant.cloudMetaErased).
package callabout

import (
	"context"
	_ "embed"
	"fmt"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed sql/call_about.sql
var schemaSQL string

// ApplySchema creates the table. Idempotent.
func ApplySchema(ctx context.Context, pool *pgxpool.Pool) error {
	if _, err := pool.Exec(ctx, schemaSQL); err != nil {
		return fmt.Errorf("callabout: apply schema: %w", err)
	}
	return nil
}

// Store keeps, reads and erases what calls are about.
type Store interface {
	// Keep records what proposalID is about; asked again, the latest words stand.
	Keep(ctx context.Context, tenantID, proposalID, about string, at time.Time) error
	// About is what each of proposalIDs is about, for those that have it.
	About(ctx context.Context, tenantID string, proposalIDs []string) (map[string]string, error)
	// Erase forgets what proposalID is about, and says whether it had it.
	Erase(ctx context.Context, tenantID, proposalID string) (bool, error)
}

// Postgres is the Store in cloud_meta.call_about.
type Postgres struct{ pool *pgxpool.Pool }

// NewPostgres returns the Store on pool.
func NewPostgres(pool *pgxpool.Pool) *Postgres { return &Postgres{pool: pool} }

func (p *Postgres) Keep(ctx context.Context, tenantID, proposalID, about string, at time.Time) error {
	_, err := p.pool.Exec(ctx, `
		INSERT INTO cloud_meta.call_about (tenant_id, proposal_id, about, created_at) VALUES ($1, $2, $3, $4)
		ON CONFLICT (tenant_id, proposal_id) DO UPDATE SET about = EXCLUDED.about`, tenantID, proposalID, about, at.UTC())
	return err
}

func (p *Postgres) About(ctx context.Context, tenantID string, proposalIDs []string) (map[string]string, error) {
	out := map[string]string{}
	if len(proposalIDs) == 0 {
		return out, nil
	}
	rows, err := p.pool.Query(ctx, `SELECT proposal_id, about FROM cloud_meta.call_about WHERE tenant_id = $1 AND proposal_id = ANY($2)`,
		tenantID, proposalIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, about string
		if err := rows.Scan(&id, &about); err != nil {
			return nil, err
		}
		out[id] = about
	}
	return out, rows.Err()
}

func (p *Postgres) Erase(ctx context.Context, tenantID, proposalID string) (bool, error) {
	tag, err := p.pool.Exec(ctx, `DELETE FROM cloud_meta.call_about WHERE tenant_id = $1 AND proposal_id = $2`, tenantID, proposalID)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// Memory is the Store in memory, for tests and a database-less run.
type Memory struct {
	mu   sync.Mutex
	rows map[[2]string]string
}

// NewMemory returns an empty Memory.
func NewMemory() *Memory { return &Memory{rows: map[[2]string]string{}} }

func (m *Memory) Keep(_ context.Context, tenantID, proposalID, about string, _ time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rows[[2]string{tenantID, proposalID}] = about
	return nil
}

func (m *Memory) About(_ context.Context, tenantID string, proposalIDs []string) (map[string]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]string{}
	for _, id := range proposalIDs {
		if a, ok := m.rows[[2]string{tenantID, id}]; ok {
			out[id] = a
		}
	}
	return out, nil
}

func (m *Memory) Erase(_ context.Context, tenantID, proposalID string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := [2]string{tenantID, proposalID}
	_, ok := m.rows[k]
	delete(m.rows, k)
	return ok, nil
}
