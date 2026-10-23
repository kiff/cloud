package halts

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed sql/halts.sql
var haltsSQL string

// ApplySchema creates the halts table if absent. Idempotent. Run after
// cloud_meta.tenants exists.
func ApplySchema(ctx context.Context, pool *pgxpool.Pool) error {
	if _, err := pool.Exec(ctx, haltsSQL); err != nil {
		return fmt.Errorf("apply halts schema: %w", err)
	}
	return nil
}

// PostgresStore is the production Store.
type PostgresStore struct{ pool *pgxpool.Pool }

// NewPostgresStore wraps pool; the caller applies the schema.
func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore { return &PostgresStore{pool: pool} }

const columns = `tenant_id, agent_id, halted, halted_at, halted_by, resumed_at, resumed_by, planque_ack`

func scan(row pgx.Row) (Halt, error) {
	var h Halt
	err := row.Scan(&h.TenantID, &h.AgentID, &h.On, &h.HaltedAt, &h.HaltedBy, &h.ResumedAt, &h.ResumedBy, &h.PlanqueAck)
	return h, err
}

// Halt begins a halt only over none or a resumed one, in one statement, so
// a second halt keeps the first one's time.
func (s *PostgresStore) Halt(ctx context.Context, tenant, agent string, at time.Time, by string) (Halt, bool, error) {
	if err := checkAgent(agent); err != nil {
		return Halt{}, false, err
	}
	h, err := scan(s.pool.QueryRow(ctx, `
		INSERT INTO cloud_meta.halts (tenant_id, agent_id, halted, halted_at, halted_by)
		VALUES ($1, $2, TRUE, $3, $4)
		ON CONFLICT (tenant_id, agent_id) DO UPDATE
		   SET halted = TRUE, halted_at = EXCLUDED.halted_at, halted_by = EXCLUDED.halted_by, planque_ack = NULL
		 WHERE NOT cloud_meta.halts.halted
		RETURNING `+columns, tenant, agent, at.UTC(), by))
	if errors.Is(err, pgx.ErrNoRows) { // halted already
		h, err = s.Get(ctx, tenant, agent)
		return h, false, err
	}
	if err != nil {
		return Halt{}, false, fmt.Errorf("halt: %w", err)
	}
	return h, true, nil
}

func (s *PostgresStore) Resume(ctx context.Context, tenant, agent string, at time.Time, by string) (Halt, bool, error) {
	if err := checkAgent(agent); err != nil {
		return Halt{}, false, err
	}
	h, err := scan(s.pool.QueryRow(ctx, `
		UPDATE cloud_meta.halts SET halted = FALSE, resumed_at = $3, resumed_by = $4
		 WHERE tenant_id = $1 AND agent_id = $2 AND halted
		RETURNING `+columns, tenant, agent, at.UTC(), by))
	if errors.Is(err, pgx.ErrNoRows) { // not halted
		h, err = s.Get(ctx, tenant, agent)
		return h, false, err
	}
	if err != nil {
		return Halt{}, false, fmt.Errorf("resume: %w", err)
	}
	return h, true, nil
}

func (s *PostgresStore) Get(ctx context.Context, tenant, agent string) (Halt, error) {
	h, err := scan(s.pool.QueryRow(ctx, `SELECT `+columns+` FROM cloud_meta.halts WHERE tenant_id = $1 AND agent_id = $2`, tenant, agent))
	if errors.Is(err, pgx.ErrNoRows) {
		return Halt{TenantID: tenant, AgentID: agent}, nil
	}
	if err != nil {
		return Halt{}, fmt.Errorf("read halt: %w", err)
	}
	return h, nil
}

func (s *PostgresStore) AckPlanque(ctx context.Context, tenant, agent string, haltedAt, at time.Time) error {
	if _, err := s.pool.Exec(ctx, `
		UPDATE cloud_meta.halts SET planque_ack = $4
		 WHERE tenant_id = $1 AND agent_id = $2 AND halted AND halted_at = $3 AND planque_ack IS NULL`,
		tenant, agent, haltedAt.UTC(), at.UTC()); err != nil {
		return fmt.Errorf("ack halt: %w", err)
	}
	return nil
}

func (s *PostgresStore) Log(ctx context.Context, e Event) error {
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO cloud_meta.halt_events (tenant_id, id, agent_id, kind, at, by_subject, receipt)
		VALUES ($1, $2, $3, $4, $5, $6, $7) ON CONFLICT DO NOTHING`,
		e.TenantID, e.ID, e.AgentID, e.Kind, e.At.UTC(), e.By, e.Receipt); err != nil {
		return fmt.Errorf("log halt event: %w", err)
	}
	return nil
}

func (s *PostgresStore) Events(ctx context.Context, tenant string, limit int) ([]Event, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, agent_id, kind, at, by_subject, receipt FROM cloud_meta.halt_events
		 WHERE tenant_id = $1 ORDER BY at DESC, id DESC LIMIT $2`, tenant, limit)
	if err != nil {
		return nil, fmt.Errorf("list halt events: %w", err)
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		e := Event{TenantID: tenant}
		if err := rows.Scan(&e.ID, &e.AgentID, &e.Kind, &e.At, &e.By, &e.Receipt); err != nil {
			return nil, fmt.Errorf("list halt events: scan: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
