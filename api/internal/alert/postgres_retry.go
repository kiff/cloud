package alert

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PostgresRetryStore is the durable retry queue in cloud_meta.alert_retries
// (created by ApplyDeliveriesSchema).
type PostgresRetryStore struct {
	pool *pgxpool.Pool
}

// NewPostgresRetryStore wraps the cloud pool.
func NewPostgresRetryStore(pool *pgxpool.Pool) *PostgresRetryStore {
	return &PostgresRetryStore{pool: pool}
}

func (p *PostgresRetryStore) ScheduleRetry(ctx context.Context, r Retry) error {
	created := r.CreatedAt
	if created.IsZero() {
		created = time.Now().UTC()
	}
	_, err := p.pool.Exec(ctx, `
INSERT INTO cloud_meta.alert_retries
    (id, tenant_id, target_id, event_id, proposal_id, action, outcome, body, attempts, next_attempt_at, last_error, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
ON CONFLICT (tenant_id, id) DO NOTHING`,
		r.ID, r.TenantID, r.TargetID, r.EventID, r.ProposalID, r.Action, r.Outcome, r.Body, r.Attempts, r.NextAt.UTC(), r.LastError, created.UTC())
	if err != nil {
		return fmt.Errorf("schedule alert retry: %w", err)
	}
	return nil
}

const retryColumns = `id, tenant_id, target_id, event_id, proposal_id, action, outcome, body, attempts, next_attempt_at, last_error, created_at, claim_token`

// ClaimDueRetry leases the row due soonest with FOR UPDATE SKIP LOCKED
// under a new claim token, so two API instances never claim the same
// retry while its lease holds.
func (p *PostgresRetryStore) ClaimDueRetry(ctx context.Context, now time.Time, lease time.Duration) (Retry, bool, error) {
	var r Retry
	err := p.pool.QueryRow(ctx, `
UPDATE cloud_meta.alert_retries SET next_attempt_at = $2, claim_token = $3
WHERE (tenant_id, id) IN (
    SELECT tenant_id, id FROM cloud_meta.alert_retries
    WHERE next_attempt_at <= $1 ORDER BY next_attempt_at LIMIT 1 FOR UPDATE SKIP LOCKED)
RETURNING `+retryColumns, now.UTC(), now.Add(lease).UTC(), newClaim()).Scan(&r.ID, &r.TenantID, &r.TargetID, &r.EventID, &r.ProposalID,
		&r.Action, &r.Outcome, &r.Body, &r.Attempts, &r.NextAt, &r.LastError, &r.CreatedAt, &r.Claim)
	if errors.Is(err, pgx.ErrNoRows) {
		return Retry{}, false, nil
	}
	if err != nil {
		return Retry{}, false, fmt.Errorf("claim alert retry: %w", err)
	}
	return r, true, nil
}

func (p *PostgresRetryStore) RescheduleRetry(ctx context.Context, r Retry) error {
	tag, err := p.pool.Exec(ctx, `UPDATE cloud_meta.alert_retries SET attempts = $4, next_attempt_at = $5, last_error = $6
WHERE tenant_id = $1 AND id = $2 AND claim_token = $3`, r.TenantID, r.ID, r.Claim, r.Attempts, r.NextAt.UTC(), r.LastError)
	if err != nil {
		return fmt.Errorf("reschedule alert retry: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return ErrLeaseLost
	}
	return nil
}

// GiveUp locks the retry row under r's claim, switches its target off and
// deletes the row in one transaction. While the row is locked no other
// worker can claim it (SKIP LOCKED), and a claim that lapsed finds no row.
func (p *PostgresRetryStore) GiveUp(ctx context.Context, r Retry, reason string, at time.Time) error {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("give up alert retry: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var one int
	err = tx.QueryRow(ctx, `SELECT 1 FROM cloud_meta.alert_retries WHERE tenant_id = $1 AND id = $2 AND claim_token = $3 FOR UPDATE`,
		r.TenantID, r.ID, r.Claim).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrLeaseLost
	}
	if err != nil {
		return fmt.Errorf("give up alert retry: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE cloud_meta.alert_targets SET enabled = false, disabled_reason = $3, disabled_at = $4
WHERE tenant_id = $1 AND id = $2`, r.TenantID, r.TargetID, reason, at.UTC()); err != nil {
		return fmt.Errorf("give up alert retry: switch the target off: %w", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM cloud_meta.alert_retries WHERE tenant_id = $1 AND id = $2`, r.TenantID, r.ID); err != nil {
		return fmt.Errorf("give up alert retry: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("give up alert retry: %w", err)
	}
	return nil
}

func (p *PostgresRetryStore) FinishRetry(ctx context.Context, r Retry) error {
	tag, err := p.pool.Exec(ctx, `DELETE FROM cloud_meta.alert_retries WHERE tenant_id = $1 AND id = $2 AND claim_token = $3`, r.TenantID, r.ID, r.Claim)
	if err != nil {
		return fmt.Errorf("finish alert retry: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return ErrLeaseLost
	}
	return nil
}
