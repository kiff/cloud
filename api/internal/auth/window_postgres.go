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

//go:embed sql/approval_windows.sql
var approvalWindowsSQL string

// ApplyWindowsSchema runs the approval_windows DDL. Idempotent. Call at
// API startup after ApplyDevicesSchema.
func ApplyWindowsSchema(ctx context.Context, pool *pgxpool.Pool) error {
	if err := pgschema.Apply(ctx, pool, "auth/approval_windows", approvalWindowsSQL); err != nil {
		return fmt.Errorf("apply approval_windows schema: %w", err)
	}
	return nil
}

// PostgresWindowStore keeps windows in cloud_meta.approval_windows.
type PostgresWindowStore struct {
	pool *pgxpool.Pool
}

// NewPostgresWindowStore wraps pool; the caller ran ApplyWindowsSchema.
func NewPostgresWindowStore(pool *pgxpool.Pool) *PostgresWindowStore {
	return &PostgresWindowStore{pool: pool}
}

const sqlWindowColumns = `id, tenant_id, subject, device_id, session_id, opened_at, expires_at, cap_at, opened_by, closed_at, closed_by`

// windowsKept is how long a closed or lapsed window's row stays, for the
// Record; older rows go with the next opening.
const windowsKept = 30 * 24 * time.Hour

func (s *PostgresWindowStore) Open(ctx context.Context, w ApprovalWindow) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("open window: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx, `UPDATE cloud_meta.approval_windows SET closed_at = $2, closed_by = $3
WHERE device_id = $1 AND closed_at IS NULL`, w.DeviceID, w.OpenedAt.UTC(), WindowClosedByReplaced); err != nil {
		return fmt.Errorf("open window: close the device's others: %w", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO cloud_meta.approval_windows (`+sqlWindowColumns+`)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, NULL, '')`,
		w.ID, w.Tenant, w.Subject, w.DeviceID, w.SessionID, w.OpenedAt.UTC(), w.ExpiresAt.UTC(), w.CapAt.UTC(), w.OpenedBy); err != nil {
		return fmt.Errorf("open window: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("open window: %w", err)
	}
	// Openings are a few a day per phone, so pruning on each keeps the table
	// small without a sweeper.
	_, _ = s.pool.Exec(ctx, `DELETE FROM cloud_meta.approval_windows WHERE cap_at < $1`, w.OpenedAt.Add(-windowsKept).UTC())
	return nil
}

func (s *PostgresWindowStore) Get(ctx context.Context, id string) (ApprovalWindow, error) {
	w, err := scanWindow(s.pool.QueryRow(ctx, `SELECT `+sqlWindowColumns+` FROM cloud_meta.approval_windows WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return ApprovalWindow{}, ErrWindowNotFound
	}
	if err != nil {
		return ApprovalWindow{}, fmt.Errorf("read window: %w", err)
	}
	return w, nil
}

func (s *PostgresWindowStore) OpenFor(ctx context.Context, device string, now time.Time) (ApprovalWindow, error) {
	w, err := scanWindow(s.pool.QueryRow(ctx, `SELECT `+sqlWindowColumns+` FROM cloud_meta.approval_windows
WHERE device_id = $1 AND closed_at IS NULL AND expires_at > $2 AND cap_at > $2 ORDER BY opened_at DESC LIMIT 1`, device, now.UTC()))
	if errors.Is(err, pgx.ErrNoRows) {
		return ApprovalWindow{}, ErrWindowNotFound
	}
	if err != nil {
		return ApprovalWindow{}, fmt.Errorf("read the device's window: %w", err)
	}
	return w, nil
}

func (s *PostgresWindowStore) Restart(ctx context.Context, id string, expires time.Time) error {
	tag, err := s.pool.Exec(ctx, `UPDATE cloud_meta.approval_windows SET expires_at = $2 WHERE id = $1 AND closed_at IS NULL`, id, expires.UTC())
	if err != nil {
		return fmt.Errorf("restart window: %w", err)
	}
	if tag.RowsAffected() == 0 {
		var n int
		if err := s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM cloud_meta.approval_windows WHERE id = $1`, id).Scan(&n); err != nil {
			return fmt.Errorf("restart window: %w", err)
		}
		if n == 0 {
			return ErrWindowNotFound
		}
	}
	return nil
}

func (s *PostgresWindowStore) Close(ctx context.Context, id string, at time.Time, why string) error {
	tag, err := s.pool.Exec(ctx, `UPDATE cloud_meta.approval_windows SET closed_at = $2, closed_by = $3 WHERE id = $1 AND closed_at IS NULL`, id, at.UTC(), why)
	if err != nil {
		return fmt.Errorf("close window: %w", err)
	}
	if tag.RowsAffected() == 0 {
		var n int
		if err := s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM cloud_meta.approval_windows WHERE id = $1`, id).Scan(&n); err != nil {
			return fmt.Errorf("close window: %w", err)
		}
		if n == 0 {
			return ErrWindowNotFound
		}
	}
	return nil
}

func (s *PostgresWindowStore) CloseAll(ctx context.Context, match WindowMatch, at time.Time, why string) (int, error) {
	tag, err := s.pool.Exec(ctx, `UPDATE cloud_meta.approval_windows SET closed_at = $1, closed_by = $2
WHERE closed_at IS NULL AND ($3 = '' OR tenant_id = $3) AND ($4 = '' OR subject = $4) AND ($5 = '' OR device_id = $5) AND ($6 = '' OR session_id = $6)`,
		at.UTC(), why, match.Tenant, match.Subject, match.Device, match.Session)
	if err != nil {
		return 0, fmt.Errorf("close windows: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

func scanWindow(row pgx.Row) (ApprovalWindow, error) {
	var w ApprovalWindow
	var closed *time.Time
	if err := row.Scan(&w.ID, &w.Tenant, &w.Subject, &w.DeviceID, &w.SessionID, &w.OpenedAt, &w.ExpiresAt, &w.CapAt, &w.OpenedBy, &closed, &w.ClosedBy); err != nil {
		return ApprovalWindow{}, err
	}
	if closed != nil {
		t := closed.UTC()
		w.ClosedAt = &t
	}
	w.OpenedAt, w.ExpiresAt, w.CapAt = w.OpenedAt.UTC(), w.ExpiresAt.UTC(), w.CapAt.UTC()
	return w, nil
}
