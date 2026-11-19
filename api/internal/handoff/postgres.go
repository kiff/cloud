package handoff

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ApplySchema creates the table. Idempotent.
func ApplySchema(ctx context.Context, pool *pgxpool.Pool) error {
	if _, err := pool.Exec(ctx, schemaSQL); err != nil {
		return fmt.Errorf("handoff: apply schema: %w", err)
	}
	return nil
}

// Postgres is the Store in cloud_meta.handoffs. Calls reads
// gateway.invocations, which the API reads and never writes.
type Postgres struct{ pool *pgxpool.Pool }

// NewPostgres returns the Store on pool.
func NewPostgres(pool *pgxpool.Pool) *Postgres { return &Postgres{pool: pool} }

const columns = `tenant_id, id, agent_id, operation, what, why, link_host, about, prepared_by, version, status,
	created_at, expires_at, answered_by, answered_by_id, answered_at, device, checked_by, checked_at, raised_receipt, settled_at`

func scan(row pgx.Row) (Handoff, error) {
	var h Handoff
	var device []byte
	var status string
	err := row.Scan(&h.TenantID, &h.ID, &h.Agent, &h.Operation, &h.What, &h.Why, &h.LinkHost, &h.About, &h.PreparedBy,
		&h.Version, &status, &h.CreatedAt, &h.ExpiresAt, &h.AnsweredBy, &h.AnsweredByID, &h.AnsweredAt, &device,
		&h.CheckedBy, &h.CheckedAt, &h.RaisedReceipt, &h.SettledAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Handoff{}, ErrNotFound
	}
	if err != nil {
		return Handoff{}, err
	}
	h.Status = Status(status)
	if h.PreparedBy == nil {
		h.PreparedBy = []string{}
	}
	if len(device) > 0 {
		var d Device
		if err := json.Unmarshal(device, &d); err != nil {
			return Handoff{}, fmt.Errorf("handoff %s: device: %w", h.ID, err)
		}
		h.Device = &d
	}
	h.CreatedAt, h.ExpiresAt = h.CreatedAt.UTC(), h.ExpiresAt.UTC()
	return h, nil
}

func scanAll(rows pgx.Rows) ([]Handoff, error) {
	defer rows.Close()
	var out []Handoff
	for rows.Next() {
		h, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

func (p *Postgres) Raise(ctx context.Context, h Handoff) (Handoff, bool, error) {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return Handoff{}, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// One agent's raises are taken one at a time, so two at once can't
	// both pass the cap.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('handoff:' || $1 || ':' || $2, 0))`, h.TenantID, h.Agent); err != nil {
		return Handoff{}, false, err
	}
	same, err := scan(tx.QueryRow(ctx, `SELECT `+columns+` FROM cloud_meta.handoffs WHERE tenant_id = $1 AND agent_id = $2 AND operation = $3`,
		h.TenantID, h.Agent, h.Operation))
	if err == nil {
		return same, false, tx.Commit(ctx)
	}
	if !errors.Is(err, ErrNotFound) {
		return Handoff{}, false, err
	}
	// Only a step that names its case (about) is the same step again: two
	// with the same words and no case may be two batches.
	waiting, err := Handoff{}, ErrNotFound
	if h.About != "" {
		waiting, err = scan(tx.QueryRow(ctx, `SELECT `+columns+` FROM cloud_meta.handoffs
			WHERE tenant_id = $1 AND agent_id = $2 AND status = 'waiting' AND expires_at > $3 AND what = $4 AND about = $5
			ORDER BY created_at LIMIT 1`, h.TenantID, h.Agent, h.CreatedAt, h.What, h.About))
	}
	if err == nil {
		waiting.AlreadyWaiting = true
		return waiting, false, tx.Commit(ctx)
	}
	if !errors.Is(err, ErrNotFound) {
		return Handoff{}, false, err
	}
	var open int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM cloud_meta.handoffs WHERE tenant_id = $1 AND agent_id = $2 AND status = 'waiting' AND expires_at > $3`,
		h.TenantID, h.Agent, h.CreatedAt).Scan(&open); err != nil {
		return Handoff{}, false, err
	}
	if open >= MaxWaitingPerAgent {
		return Handoff{}, false, ErrTooMany
	}
	if h.ID == "" {
		h.ID = NewID()
	}
	if h.PreparedBy == nil {
		h.PreparedBy = []string{}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO cloud_meta.handoffs
		(tenant_id, id, agent_id, operation, what, why, link_host, about, prepared_by, version, status, created_at, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, 'waiting', $11, $12)`,
		h.TenantID, h.ID, h.Agent, h.Operation, h.What, h.Why, h.LinkHost, h.About, h.PreparedBy, h.Version,
		h.CreatedAt.UTC(), h.ExpiresAt.UTC()); err != nil {
		return Handoff{}, false, err
	}
	h.Status = Waiting
	return h, true, tx.Commit(ctx)
}

func (p *Postgres) Get(ctx context.Context, tenantID, id string) (Handoff, error) {
	return scan(p.pool.QueryRow(ctx, `SELECT `+columns+` FROM cloud_meta.handoffs WHERE tenant_id = $1 AND id = $2`, tenantID, id))
}

func (p *Postgres) Open(ctx context.Context, tenantID string, now time.Time, limit int) ([]Handoff, error) {
	rows, err := p.pool.Query(ctx, `SELECT `+columns+` FROM cloud_meta.handoffs
		WHERE tenant_id = $1 AND status = 'waiting' AND expires_at > $2 ORDER BY created_at, id LIMIT $3`, tenantID, now.UTC(), limit)
	if err != nil {
		return nil, err
	}
	return scanAll(rows)
}

func (p *Postgres) Answer(ctx context.Context, tenantID, id string, answer Status, by, byID string, device *Device, at time.Time) (Handoff, error) {
	if answer != Done && answer != Skipped {
		return Handoff{}, fmt.Errorf("handoff: %q isn't an answer", answer)
	}
	var dev []byte
	if device != nil {
		var err error
		if dev, err = json.Marshal(device); err != nil {
			return Handoff{}, err
		}
	}
	h, err := scan(p.pool.QueryRow(ctx, `UPDATE cloud_meta.handoffs
		SET status = $3, answered_by = $4, answered_by_id = $5, answered_at = $6, device = $7
		WHERE tenant_id = $1 AND id = $2 AND status = 'waiting' AND expires_at > $6
		RETURNING `+columns, tenantID, id, string(answer), by, byID, at.UTC(), dev))
	if !errors.Is(err, ErrNotFound) {
		return h, err
	}
	cur, gerr := p.Get(ctx, tenantID, id)
	if gerr != nil {
		return Handoff{}, gerr
	}
	return cur, ErrAnswered
}

func (p *Postgres) Check(ctx context.Context, tenantID, id, proposal string, at time.Time) (Handoff, error) {
	h, err := scan(p.pool.QueryRow(ctx, `UPDATE cloud_meta.handoffs SET checked_by = $3, checked_at = $4
		WHERE tenant_id = $1 AND id = $2 AND status = 'done' AND checked_by = '' AND settled_at IS NULL
		RETURNING `+columns, tenantID, id, proposal, at.UTC()))
	if !errors.Is(err, ErrNotFound) {
		return h, err
	}
	cur, gerr := p.Get(ctx, tenantID, id)
	if gerr != nil {
		return Handoff{}, gerr
	}
	return cur, ErrNotChecked
}

func (p *Postgres) Expire(ctx context.Context, now time.Time, limit int) ([]Handoff, error) {
	rows, err := p.pool.Query(ctx, `UPDATE cloud_meta.handoffs SET status = 'no_answer'
		WHERE (tenant_id, id) IN (SELECT tenant_id, id FROM cloud_meta.handoffs
			WHERE status = 'waiting' AND expires_at <= $1 ORDER BY expires_at LIMIT $2 FOR UPDATE SKIP LOCKED)
		AND status = 'waiting'
		RETURNING `+columns, now.UTC(), limit)
	if err != nil {
		return nil, err
	}
	return scanAll(rows)
}

func (p *Postgres) Unreceipted(ctx context.Context, now time.Time, limit int) ([]Handoff, error) {
	rows, err := p.pool.Query(ctx, `SELECT `+columns+` FROM cloud_meta.handoffs
		WHERE settled_at IS NULL AND (NOT raised_receipt OR status IN ('skipped', 'no_answer')
			OR (status = 'done' AND (checked_at IS NOT NULL OR answered_at <= $1)))
		ORDER BY created_at LIMIT $2`, now.Add(-CheckGrace).UTC(), limit)
	if err != nil {
		return nil, err
	}
	return scanAll(rows)
}

func (p *Postgres) Receipted(ctx context.Context, tenantID, id string, settled *time.Time) error {
	var at any
	if settled != nil {
		at = settled.UTC()
	}
	_, err := p.pool.Exec(ctx, `UPDATE cloud_meta.handoffs SET raised_receipt = true, settled_at = COALESCE(settled_at, $3)
		WHERE tenant_id = $1 AND id = $2`, tenantID, id, at)
	return err
}

func (p *Postgres) Calls(ctx context.Context, tenantID string, ids []string) (map[string]Call, error) {
	out := map[string]Call{}
	if len(ids) == 0 {
		return out, nil
	}
	return p.calls(ctx, `SELECT key, operation_id, agent_id, state, tool_outcome, created_at FROM gateway.invocations
		WHERE tenant_id = $1 AND key = ANY($2)`, func(c Call) string { return c.Proposal }, tenantID, ids)
}

func (p *Postgres) Operations(ctx context.Context, tenantID string, agents, ops []string) (map[string]Call, error) {
	if len(ops) == 0 || len(agents) == 0 {
		return map[string]Call{}, nil
	}
	return p.calls(ctx, `SELECT key, operation_id, agent_id, state, tool_outcome, created_at FROM gateway.invocations
		WHERE tenant_id = $1 AND agent_id = ANY($2) AND operation_id = ANY($3) AND operation_id <> ''`,
		func(c Call) string { return c.Operation }, tenantID, agents, ops)
}

func (p *Postgres) calls(ctx context.Context, q string, by func(Call) string, args ...any) (map[string]Call, error) {
	out := map[string]Call{}
	rows, err := p.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var c Call
		if err := rows.Scan(&c.Proposal, &c.Operation, &c.Agent, &c.State, &c.ToolOutcome, &c.CreatedAt); err != nil {
			return nil, err
		}
		c.CreatedAt = c.CreatedAt.UTC()
		out[by(c)] = c
	}
	return out, rows.Err()
}
