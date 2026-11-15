package auth

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kiff/cloud/api/internal/pgschema"
)

//go:embed sql/approval_checks.sql
var approvalChecksSQL string

// ApplyApprovalChecksSchema runs the approval_checks DDL. Idempotent. Call
// at API startup after ApplyWindowsSchema.
func ApplyApprovalChecksSchema(ctx context.Context, pool *pgxpool.Pool) error {
	if err := pgschema.Apply(ctx, pool, "auth/approval_checks", approvalChecksSQL); err != nil {
		return fmt.Errorf("apply approval_checks schema: %w", err)
	}
	return nil
}

// PostgresApprovalCheckStore keeps the records in cloud_meta.approval_checks.
type PostgresApprovalCheckStore struct {
	pool *pgxpool.Pool
}

// NewPostgresApprovalCheckStore wraps pool; the caller ran
// ApplyApprovalChecksSchema.
func NewPostgresApprovalCheckStore(pool *pgxpool.Pool) *PostgresApprovalCheckStore {
	return &PostgresApprovalCheckStore{pool: pool}
}

func (s *PostgresApprovalCheckStore) Record(ctx context.Context, c ApprovalCheck) error {
	if _, err := s.pool.Exec(ctx, `DELETE FROM cloud_meta.approval_checks WHERE checked_at < $1`,
		c.CheckedAt.Add(-approvalChecksKept).UTC()); err != nil {
		return fmt.Errorf("record approval check: delete old: %w", err)
	}
	// An existing record stays as it was: the same approval verified again
	// never moves its check later.
	if _, err := s.pool.Exec(ctx, `INSERT INTO cloud_meta.approval_checks
(id, tenant_id, device_id, grant_id, window_id, checked_at, used_at)
VALUES ($1, $2, $3, $4, $5, $6, NULL) ON CONFLICT (id) DO NOTHING`,
		c.ID, c.Tenant, c.DeviceID, c.Grant, c.Window, c.CheckedAt.UTC()); err != nil {
		return fmt.Errorf("record approval check: %w", err)
	}
	return nil
}

func (s *PostgresApprovalCheckStore) Get(ctx context.Context, id string) (ApprovalCheck, error) {
	var c ApprovalCheck
	err := s.pool.QueryRow(ctx, `SELECT id, tenant_id, device_id, grant_id, window_id, checked_at, used_at
FROM cloud_meta.approval_checks WHERE id = $1`, id).Scan(&c.ID, &c.Tenant, &c.DeviceID, &c.Grant, &c.Window, &c.CheckedAt, &c.UsedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return ApprovalCheck{}, ErrApprovalCheckNotFound
	}
	if err != nil {
		return ApprovalCheck{}, fmt.Errorf("get approval check: %w", err)
	}
	return c, nil
}

func (s *PostgresApprovalCheckStore) Use(ctx context.Context, id string, at time.Time) (bool, error) {
	tag, err := s.pool.Exec(ctx, `UPDATE cloud_meta.approval_checks SET used_at = $2
WHERE id = $1 AND used_at IS NULL`, id, at.UTC())
	if err != nil {
		return false, fmt.Errorf("use approval check: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}
