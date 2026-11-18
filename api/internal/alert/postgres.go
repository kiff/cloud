package alert

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kiff/cloud/api/internal/pgschema"
)

//go:embed sql/alert_targets.sql
var alertTargetsSQL string

// AlertTargetsSQL returns the DDL that creates cloud_meta.alert_targets.
// Idempotent. Call once at startup after tenant.ApplyCloudMeta (the
// foreign key requires cloud_meta.tenants).
func AlertTargetsSQL() string { return alertTargetsSQL }

// ApplySchema runs the alert_targets DDL against pool. Idempotent.
func ApplySchema(ctx context.Context, pool *pgxpool.Pool) error {
	if err := pgschema.Apply(ctx, pool, "alert/alert_targets", alertTargetsSQL); err != nil {
		return fmt.Errorf("apply alert_targets schema: %w", err)
	}
	return nil
}

// PostgresStore persists alert targets in cloud_meta.alert_targets.
type PostgresStore struct {
	pool *pgxpool.Pool
}

// NewPostgresStore wraps the cloud pool.
func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore {
	return &PostgresStore{pool: pool}
}

const sqlListTargets = `
SELECT id, tenant_id, action, url, outcomes, enabled, created_at, signing_secret, disabled_reason, disabled_at
FROM cloud_meta.alert_targets
WHERE tenant_id = $1
ORDER BY created_at DESC
`

const sqlCreateTarget = `
INSERT INTO cloud_meta.alert_targets
    (id, tenant_id, action, url, outcomes, enabled, created_at, signing_secret)
VALUES ($1, $2, $3, $4, $5::jsonb, $6, $7, $8)
`

const sqlRotateSecret = `
UPDATE cloud_meta.alert_targets SET signing_secret = $3 WHERE tenant_id = $1 AND id = $2
`

const sqlDeleteTarget = `
DELETE FROM cloud_meta.alert_targets WHERE tenant_id = $1 AND id = $2
`

// ListTargets implements Store.
func (p *PostgresStore) ListTargets(ctx context.Context, tenantID string) ([]Target, error) {
	rows, err := p.pool.Query(ctx, sqlListTargets, tenantID)
	if err != nil {
		return nil, fmt.Errorf("list alert targets: %w", err)
	}
	defer rows.Close()
	out := make([]Target, 0)
	for rows.Next() {
		var (
			t            Target
			outcomesJSON []byte
		)
		if err := rows.Scan(&t.ID, &t.TenantID, &t.Action, &t.URL, &outcomesJSON, &t.Enabled, &t.CreatedAt, &t.Secret, &t.DisabledReason, &t.DisabledAt); err != nil {
			return nil, fmt.Errorf("scan alert target: %w", err)
		}
		if len(outcomesJSON) > 0 {
			if err := json.Unmarshal(outcomesJSON, &t.Outcomes); err != nil {
				return nil, fmt.Errorf("unmarshal outcomes: %w", err)
			}
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// CreateTarget implements Store.
func (p *PostgresStore) CreateTarget(ctx context.Context, t Target) error {
	outcomesJSON, err := json.Marshal(t.Outcomes)
	if err != nil {
		return fmt.Errorf("marshal outcomes: %w", err)
	}
	if _, err := p.pool.Exec(ctx, sqlCreateTarget,
		t.ID, t.TenantID, t.Action, t.URL, string(outcomesJSON), t.Enabled, t.CreatedAt, t.Secret,
	); err != nil {
		return fmt.Errorf("create alert target: %w", err)
	}
	return nil
}

// ReplaceTarget implements Store. Replacements at one tenant and URL take
// turns on a transaction-scoped advisory lock, so each one's delete sees
// the target the one before it inserted, and one target is left.
func (p *PostgresStore) ReplaceTarget(ctx context.Context, t Target) error {
	outcomesJSON, err := json.Marshal(t.Outcomes)
	if err != nil {
		return fmt.Errorf("marshal outcomes: %w", err)
	}
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("replace alert target: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('alert_targets' || chr(10) || $1 || chr(10) || $2, 0))`,
		t.TenantID, t.URL); err != nil {
		return fmt.Errorf("replace alert target: lock: %w", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM cloud_meta.alert_targets WHERE tenant_id = $1 AND url = $2`, t.TenantID, t.URL); err != nil {
		return fmt.Errorf("replace alert target: %w", err)
	}
	if _, err := tx.Exec(ctx, sqlCreateTarget,
		t.ID, t.TenantID, t.Action, t.URL, string(outcomesJSON), t.Enabled, t.CreatedAt, t.Secret,
	); err != nil {
		return fmt.Errorf("replace alert target: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("replace alert target: %w", err)
	}
	return nil
}

// SetEnabled implements Store.
func (p *PostgresStore) SetEnabled(ctx context.Context, tenantID, id string, enabled bool, reason string, at time.Time) (bool, error) {
	var when any
	if !enabled && reason != "" {
		when = at.UTC()
	} else {
		reason = ""
	}
	tag, err := p.pool.Exec(ctx, `UPDATE cloud_meta.alert_targets SET enabled = $3, disabled_reason = $4, disabled_at = $5
WHERE tenant_id = $1 AND id = $2`, tenantID, id, enabled, reason, when)
	if err != nil {
		return false, fmt.Errorf("switch alert target: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// RotateSecret implements Store.
func (p *PostgresStore) RotateSecret(ctx context.Context, tenantID, id, secret string) (bool, error) {
	tag, err := p.pool.Exec(ctx, sqlRotateSecret, tenantID, id, secret)
	if err != nil {
		return false, fmt.Errorf("rotate alert target secret: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// AddPhoneOutcomes gives every target at one of urls (the app backends a
// phone registers) the outcomes it lacks from add, when it receives held
// calls (owner_required): a target an earlier app registered has only the
// outcomes phones took then. Idempotent; it reports how many it changed.
func (p *PostgresStore) AddPhoneOutcomes(ctx context.Context, urls, add []string) (int64, error) {
	if len(urls) == 0 || len(add) == 0 {
		return 0, nil
	}
	tag, err := p.pool.Exec(ctx, `
		UPDATE cloud_meta.alert_targets t
		   SET outcomes = t.outcomes || (
		         SELECT COALESCE(jsonb_agg(o), '[]'::jsonb) FROM unnest($2::text[]) AS o
		          WHERE NOT t.outcomes ? o)
		 WHERE t.url = ANY($1::text[])
		   AND t.outcomes ? 'owner_required'
		   AND EXISTS (SELECT 1 FROM unnest($2::text[]) AS o WHERE NOT t.outcomes ? o)`, urls, add)
	if err != nil {
		return 0, fmt.Errorf("add phone alert outcomes: %w", err)
	}
	return tag.RowsAffected(), nil
}

// DeleteTarget implements Store.
func (p *PostgresStore) DeleteTarget(ctx context.Context, tenantID, id string) error {
	if _, err := p.pool.Exec(ctx, sqlDeleteTarget, tenantID, id); err != nil {
		return fmt.Errorf("delete alert target: %w", err)
	}
	return nil
}
