package agentdrafts

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kiff/cloud/api/internal/pgschema"
)

//go:embed sql/agent_drafts.sql
var schemaSQL string

// ApplySchema creates cloud_meta.agent_drafts. Idempotent; run after
// tenant.ApplyCloudMeta.
func ApplySchema(ctx context.Context, pool *pgxpool.Pool) error {
	if err := pgschema.Apply(ctx, pool, "agentdrafts/agent_drafts", schemaSQL); err != nil {
		return fmt.Errorf("apply agent_drafts schema: %w", err)
	}
	return nil
}

// PostgresStore keeps drafts in cloud_meta.agent_drafts.
type PostgresStore struct{ pool *pgxpool.Pool }

// NewPostgresStore wraps the cloud pool.
func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore { return &PostgresStore{pool: pool} }

const columns = `id, tenant_id, agent_id, label, cards, version, status, created_by, created_at, expires_at,
confirmed_by, confirmed_role, confirmed_at, card_ids, claim_hash, claimed_at, key_id, reason, revision, revises, retires, retire_rest, trial, from_hold`

func scan(row pgx.Row) (D, error) {
	var d D
	var status string
	var cardIDs, revises, retires, trial []byte
	err := row.Scan(&d.ID, &d.TenantID, &d.AgentID, &d.Label, &d.Cards, &d.Version, &status, &d.CreatedBy, &d.CreatedAt, &d.ExpiresAt,
		&d.ConfirmedBy, &d.ConfirmedRole, &d.ConfirmedAt, &cardIDs, &d.ClaimHash, &d.ClaimedAt, &d.KeyID, &d.Reason, &d.Revision, &revises, &retires, &d.RetireRest, &trial, &d.FromHold)
	if errors.Is(err, pgx.ErrNoRows) {
		return D{}, ErrNotFound
	}
	if err != nil {
		return D{}, err
	}
	d.Status = Status(status)
	if len(cardIDs) > 0 {
		if err := json.Unmarshal(cardIDs, &d.CardIDs); err != nil {
			return D{}, fmt.Errorf("agent draft card ids: %w", err)
		}
	}
	if len(revises) > 0 {
		if err := json.Unmarshal(revises, &d.Revises); err != nil {
			return D{}, fmt.Errorf("agent draft revises: %w", err)
		}
	}
	if len(retires) > 0 {
		if err := json.Unmarshal(retires, &d.Retires); err != nil {
			return D{}, fmt.Errorf("agent draft retires: %w", err)
		}
	}
	if len(trial) > 0 {
		if err := json.Unmarshal(trial, &d.Trial); err != nil {
			return D{}, fmt.Errorf("agent draft trial: %w", err)
		}
	}
	return d, nil
}

func (p *PostgresStore) Create(ctx context.Context, d D) error {
	revises, err := json.Marshal(d.Revises)
	if err != nil {
		return err
	}
	if d.Revises == nil {
		revises = []byte("[]")
	}
	retires, err := json.Marshal(d.Retires)
	if err != nil {
		return err
	}
	if d.Retires == nil {
		retires = []byte("[]")
	}
	_, err = p.pool.Exec(ctx, `INSERT INTO cloud_meta.agent_drafts
    (id, tenant_id, agent_id, label, cards, version, status, created_by, created_at, expires_at, claim_hash, revision, revises, retires, retire_rest, from_hold)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)`,
		d.ID, d.TenantID, d.AgentID, d.Label, []byte(d.Cards), d.Version, string(d.Status), d.CreatedBy, d.CreatedAt.UTC(), d.ExpiresAt.UTC(), d.ClaimHash,
		d.Revision, revises, retires, d.RetireRest, d.FromHold)
	if err != nil {
		return fmt.Errorf("create agent draft: %w", err)
	}
	return nil
}

func (p *PostgresStore) Get(ctx context.Context, tenantID, id string) (D, error) {
	return scan(p.pool.QueryRow(ctx, `SELECT `+columns+` FROM cloud_meta.agent_drafts WHERE tenant_id = $1 AND id = $2`, tenantID, id))
}

func (p *PostgresStore) Waiting(ctx context.Context, tenantID string, at time.Time) ([]D, error) {
	rows, err := p.pool.Query(ctx, `SELECT `+columns+` FROM cloud_meta.agent_drafts
WHERE tenant_id = $1 AND status = 'draft' AND expires_at > $2
ORDER BY created_at DESC, id DESC LIMIT $3`, tenantID, at.UTC(), MaxWaiting)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []D
	for rows.Next() {
		d, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// why explains a failed compare-and-set from the row as it stands.
func (p *PostgresStore) why(ctx context.Context, tenantID, id, version string, at time.Time) error {
	d, err := p.Get(ctx, tenantID, id)
	switch {
	case err != nil:
		return err
	case d.Status != Draft && d.Status != Trying:
		return ErrState
	case !at.Before(d.ExpiresAt):
		return ErrExpired
	case version != "" && d.Version != version:
		return ErrVersion
	}
	return ErrState
}

func (p *PostgresStore) Confirm(ctx context.Context, tenantID, id, version, by, role string, cardIDs []string, at time.Time) (D, error) {
	ids, err := json.Marshal(append([]string{}, cardIDs...))
	if err != nil {
		return D{}, err
	}
	// Installing a version being tried ends its trial in the same update.
	d, err := scan(p.pool.QueryRow(ctx, `UPDATE cloud_meta.agent_drafts SET status = 'confirming', confirmed_by = $4, confirmed_at = $5, confirmed_role = $6, card_ids = $7,
    trial = CASE WHEN status = 'trying' THEN trial || jsonb_build_object('ended_at', $5::timestamptz, 'ended_by', $8::text) ELSE trial END
WHERE tenant_id = $1 AND id = $2 AND version = $3 AND status IN ('draft', 'trying') AND expires_at > $5
RETURNING `+columns, tenantID, id, version, by, at.UTC(), role, ids, EndedInstall))
	if errors.Is(err, ErrNotFound) {
		return D{}, p.why(ctx, tenantID, id, version, at)
	}
	return d, err
}

func (p *PostgresStore) StartTrial(ctx context.Context, tenantID, id, version string, t Trial, at time.Time) (D, error) {
	if t.CardIDs == nil {
		t.CardIDs = []string{}
	}
	raw, err := json.Marshal(t)
	if err != nil {
		return D{}, err
	}
	d, err := scan(p.pool.QueryRow(ctx, `UPDATE cloud_meta.agent_drafts SET status = 'trying', trial = $5, expires_at = $6
WHERE tenant_id = $1 AND id = $2 AND version = $3 AND status = 'draft' AND revision AND expires_at > $4
RETURNING `+columns, tenantID, id, version, at.UTC(), raw, t.ExpiresAt.UTC()))
	if errors.Is(err, ErrNotFound) {
		if cur, gerr := p.Get(ctx, tenantID, id); gerr == nil && cur.Status == Draft && !cur.Revision {
			return D{}, ErrState
		}
		return D{}, p.why(ctx, tenantID, id, version, at)
	}
	return d, err
}

func (p *PostgresStore) EndTrial(ctx context.Context, tenantID, id, by string, at time.Time) (D, error) {
	d, err := scan(p.pool.QueryRow(ctx, `UPDATE cloud_meta.agent_drafts SET status = 'draft',
    trial = trial || jsonb_build_object('ended_at', $3::timestamptz, 'ended_by', $4::text),
    expires_at = LEAST(expires_at, $3::timestamptz + make_interval(secs => $5))
WHERE tenant_id = $1 AND id = $2 AND status = 'trying'
RETURNING `+columns, tenantID, id, at.UTC(), by, DefaultTTL.Seconds()))
	if errors.Is(err, ErrNotFound) {
		if _, gerr := p.Get(ctx, tenantID, id); gerr != nil {
			return D{}, gerr
		}
		return D{}, ErrState
	}
	return d, err
}

func (p *PostgresStore) TryingFor(ctx context.Context, tenantID, agentID string) ([]D, error) {
	rows, err := p.pool.Query(ctx, `SELECT `+columns+` FROM cloud_meta.agent_drafts
WHERE tenant_id = $1 AND agent_id = $2 AND status = 'trying' ORDER BY created_at`, tenantID, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []D
	for rows.Next() {
		d, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// move changes a draft's status from one of `from`, setting reason when
// it isn't empty.
func (p *PostgresStore) move(ctx context.Context, tenantID, id string, to Status, reason string, from ...Status) (D, error) {
	froms := make([]string, len(from))
	for i, f := range from {
		froms[i] = string(f)
	}
	d, err := scan(p.pool.QueryRow(ctx, `UPDATE cloud_meta.agent_drafts SET status = $3, reason = CASE WHEN $4 = '' THEN reason ELSE $4 END
WHERE tenant_id = $1 AND id = $2 AND status = ANY($5)
RETURNING `+columns, tenantID, id, string(to), reason, froms))
	if errors.Is(err, ErrNotFound) {
		if _, gerr := p.Get(ctx, tenantID, id); gerr != nil {
			return D{}, gerr
		}
		return D{}, ErrState
	}
	return d, err
}

func (p *PostgresStore) Finish(ctx context.Context, tenantID, id string) (D, error) {
	return p.move(ctx, tenantID, id, Confirmed, "", Confirming)
}

func (p *PostgresStore) Fail(ctx context.Context, tenantID, id, reason string) (D, error) {
	return p.move(ctx, tenantID, id, RollingBack, reason, Confirming)
}

func (p *PostgresStore) Settle(ctx context.Context, tenantID, id string) error {
	tag, err := p.pool.Exec(ctx, `UPDATE cloud_meta.agent_drafts
SET status = CASE status WHEN 'rolling_back' THEN 'failed' ELSE 'discarded' END
WHERE tenant_id = $1 AND id = $2 AND status IN ('rolling_back', 'discarding')`, tenantID, id)
	if err == nil && tag.RowsAffected() != 1 {
		return ErrState
	}
	return err
}

func (p *PostgresStore) Unsettled(ctx context.Context, before time.Time) ([]D, error) {
	rows, err := p.pool.Query(ctx, `SELECT `+columns+` FROM cloud_meta.agent_drafts
WHERE status IN ('rolling_back', 'discarding') OR (status = 'confirming' AND confirmed_at < $1)
ORDER BY created_at LIMIT 100`, before.UTC())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []D
	for rows.Next() {
		d, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (p *PostgresStore) Claim(ctx context.Context, id, claimHash string, at time.Time) (D, error) {
	d, err := scan(p.pool.QueryRow(ctx, `UPDATE cloud_meta.agent_drafts SET status = 'claimed', claimed_at = $3
WHERE id = $1 AND claim_hash = $2 AND status = 'confirmed' AND NOT revision AND expires_at > $3
RETURNING `+columns, id, claimHash, at.UTC()))
	if !errors.Is(err, ErrNotFound) {
		return d, err
	}
	cur, gerr := scan(p.pool.QueryRow(ctx, `SELECT `+columns+` FROM cloud_meta.agent_drafts WHERE id = $1 AND claim_hash = $2`, id, claimHash))
	switch {
	case gerr != nil:
		return D{}, ErrNotFound
	case cur.Status != Confirmed || cur.Revision:
		return D{}, ErrState
	default:
		return D{}, ErrExpired
	}
}

func (p *PostgresStore) SetKey(ctx context.Context, tenantID, id, keyID string) error {
	_, err := p.pool.Exec(ctx, `UPDATE cloud_meta.agent_drafts SET key_id = $3 WHERE tenant_id = $1 AND id = $2`, tenantID, id, keyID)
	return err
}

func (p *PostgresStore) Unclaim(ctx context.Context, tenantID, id string) error {
	tag, err := p.pool.Exec(ctx, `UPDATE cloud_meta.agent_drafts SET status = 'confirmed', claimed_at = NULL
WHERE tenant_id = $1 AND id = $2 AND status = 'claimed' AND key_id = ''`, tenantID, id)
	if err == nil && tag.RowsAffected() != 1 {
		return ErrState
	}
	return err
}

func (p *PostgresStore) Discard(ctx context.Context, tenantID, id string) (D, error) {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return D{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	d, err := scan(tx.QueryRow(ctx, `SELECT `+columns+` FROM cloud_meta.agent_drafts WHERE tenant_id = $1 AND id = $2 FOR UPDATE`, tenantID, id))
	if err != nil {
		return D{}, err
	}
	to := Discarding
	switch {
	case d.Status == Draft:
		to = Discarded
	case d.Revision:
		// Its Cards are the agent's own, revised: going back is another
		// revision, never revoking them.
		return D{}, ErrState
	case d.Status == Confirmed || d.Status == Discarding:
	default:
		return D{}, ErrState
	}
	if _, err := tx.Exec(ctx, `UPDATE cloud_meta.agent_drafts SET status = $3 WHERE tenant_id = $1 AND id = $2`, tenantID, id, string(to)); err != nil {
		return D{}, err
	}
	return d, tx.Commit(ctx)
}

var _ Store = (*PostgresStore)(nil)
var _ Store = (*MemoryStore)(nil)
