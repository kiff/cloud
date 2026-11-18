package newdevice

import (
	"context"
	_ "embed"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kiff/cloud/api/internal/pgschema"
)

//go:embed sql/new_device.sql
var schemaSQL string

// ApplySchema runs the known_devices and new_device_emails DDL against
// pool. Idempotent. Call once at API startup, after ApplyCloudMeta.
func ApplySchema(ctx context.Context, pool *pgxpool.Pool) error {
	if err := pgschema.Apply(ctx, pool, "newdevice/new_device", schemaSQL); err != nil {
		return fmt.Errorf("apply new_device schema: %w", err)
	}
	return nil
}

// Postgres is Store on cloud_meta.known_devices and new_device_emails.
type Postgres struct {
	pool *pgxpool.Pool
}

// NewPostgres wraps pool. The caller must have run ApplySchema.
func NewPostgres(pool *pgxpool.Pool) *Postgres { return &Postgres{pool: pool} }

// Whether this device is new to the person (xmax = 0 on the upserted row:
// inserted, not updated), and whether they had another.
const sqlSeenDevice = `
WITH had AS (
    SELECT EXISTS (SELECT 1 FROM cloud_meta.known_devices WHERE subject = $1 AND client_hash <> $2) AS other
), up AS (
    INSERT INTO cloud_meta.known_devices (subject, client_hash, first_seen, last_seen)
    VALUES ($1, $2, $3, $3)
    ON CONFLICT (subject, client_hash) DO UPDATE SET last_seen = GREATEST(cloud_meta.known_devices.last_seen, EXCLUDED.last_seen)
    RETURNING (xmax = 0) AS inserted
)
SELECT up.inserted, had.other FROM up, had
`

// A device approval may have written the row first (tafs): it keeps its
// mark, and the row takes the sign-in's time and its due time.
const sqlHoldEmail = `
INSERT INTO cloud_meta.new_device_emails (session_id, subject, new_device, signed_in_at, due_at, expires_at)
VALUES ($1, $2, TRUE, $3, $4, $5)
ON CONFLICT (session_id) DO UPDATE SET new_device = TRUE, signed_in_at = EXCLUDED.signed_in_at, due_at = EXCLUDED.due_at
WHERE cloud_meta.new_device_emails.subject = EXCLUDED.subject
`

func (p *Postgres) Seen(ctx context.Context, s SignIn, now time.Time) (bool, error) {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("newdevice: %w", err)
	}
	defer tx.Rollback(ctx)
	var inserted, other bool
	if err := tx.QueryRow(ctx, sqlSeenDevice, s.Subject, s.ClientHash, now).Scan(&inserted, &other); err != nil {
		return false, fmt.Errorf("newdevice: record device: %w", err)
	}
	isNew := inserted && (other || !s.NewAccount)
	if isNew {
		if _, err := tx.Exec(ctx, sqlHoldEmail, s.SessionID, s.Subject, s.At, now.Add(Hold), now.Add(Window)); err != nil {
			return false, fmt.Errorf("newdevice: hold email: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("newdevice: %w", err)
	}
	return isNew, nil
}

const sqlThrough = `
INSERT INTO cloud_meta.new_device_emails (session_id, subject, tafs, expires_at)
VALUES ($1, $2, TRUE, $3)
ON CONFLICT (session_id) DO UPDATE SET tafs = TRUE
WHERE cloud_meta.new_device_emails.subject = EXCLUDED.subject
`

func (p *Postgres) Through(ctx context.Context, sessionID, subject string, now time.Time) error {
	if _, err := p.pool.Exec(ctx, sqlThrough, sessionID, subject, now.Add(Window)); err != nil {
		return fmt.Errorf("newdevice: mark tafs: %w", err)
	}
	return nil
}

// Taken off the table as they're read, so two API processes never send one
// twice; SKIP LOCKED lets them share the work.
const sqlTakeDue = `
DELETE FROM cloud_meta.new_device_emails
WHERE session_id IN (
    SELECT session_id FROM cloud_meta.new_device_emails
    WHERE new_device AND due_at <= $1 AND expires_at >= $1
    ORDER BY due_at LIMIT 100
    FOR UPDATE SKIP LOCKED
)
RETURNING session_id, subject, COALESCE(signed_in_at, due_at), tafs, expires_at
`

func (p *Postgres) TakeDue(ctx context.Context, now time.Time) ([]Due, error) {
	if _, err := p.pool.Exec(ctx, `DELETE FROM cloud_meta.new_device_emails WHERE expires_at < $1`, now); err != nil {
		return nil, fmt.Errorf("newdevice: drop expired: %w", err)
	}
	if _, err := p.pool.Exec(ctx, `DELETE FROM cloud_meta.known_devices WHERE last_seen < $1`, now.Add(-Unseen)); err != nil {
		return nil, fmt.Errorf("newdevice: prune devices: %w", err)
	}
	rows, err := p.pool.Query(ctx, sqlTakeDue, now)
	if err != nil {
		return nil, fmt.Errorf("newdevice: take due: %w", err)
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Due, error) {
		var d Due
		err := r.Scan(&d.SessionID, &d.Subject, &d.At, &d.Tafs, &d.Until)
		return d, err
	})
	if err != nil {
		return nil, fmt.Errorf("newdevice: take due: %w", err)
	}
	return out, nil
}

// The row as it was taken, due again; its expiry is the original one, so a
// failing send stops being tried once the window has passed.
const sqlAgain = `
INSERT INTO cloud_meta.new_device_emails (session_id, subject, new_device, signed_in_at, tafs, due_at, expires_at)
VALUES ($1, $2, TRUE, $3, $4, $5, $6)
ON CONFLICT (session_id) DO NOTHING
`

func (p *Postgres) Again(ctx context.Context, d Due, at time.Time) error {
	if at.After(d.Until) {
		return nil
	}
	if _, err := p.pool.Exec(ctx, sqlAgain, d.SessionID, d.Subject, d.At, d.Tafs, at, d.Until); err != nil {
		return fmt.Errorf("newdevice: put back: %w", err)
	}
	return nil
}
