package auth

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed sql/approval_devices.sql
var approvalDevicesSQL string

// ApplyDevicesSchema runs the approval_devices DDL. Idempotent. Call at
// API startup after ApplyDeveloperSessionsSchema (it adds
// developer_sessions.device_id).
func ApplyDevicesSchema(ctx context.Context, pool *pgxpool.Pool) error {
	if _, err := pool.Exec(ctx, approvalDevicesSQL); err != nil {
		return fmt.Errorf("apply approval_devices schema: %w", err)
	}
	return nil
}

// PostgresDeviceStore keeps approval devices in cloud_meta.approval_devices.
type PostgresDeviceStore struct {
	pool *pgxpool.Pool
}

// NewPostgresDeviceStore wraps pool; the caller ran ApplyDevicesSchema.
func NewPostgresDeviceStore(pool *pgxpool.Pool) *PostgresDeviceStore {
	return &PostgresDeviceStore{pool: pool}
}

const sqlDeviceColumns = `id, tenant_id, subject, name, platform, public_key, attestation, created_at, last_used_at, revoked_at`

func (s *PostgresDeviceStore) Insert(ctx context.Context, d ApprovalDevice) error {
	if err := d.check(); err != nil {
		return err
	}
	_, err := s.pool.Exec(ctx, `INSERT INTO cloud_meta.approval_devices (`+sqlDeviceColumns+`)
VALUES ($1, $2, $3, $4, $5, $6, NULLIF($7, ''), $8, NULL, NULL)`,
		d.ID, d.Tenant, d.Subject, d.Name, d.Platform, d.PublicKey, d.Attestation, d.CreatedAt.UTC())
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return fmt.Errorf("%w: %q", ErrDeviceDuplicate, d.ID)
	}
	if err != nil {
		return fmt.Errorf("insert device: %w", err)
	}
	return nil
}

func (s *PostgresDeviceStore) Get(ctx context.Context, id string) (ApprovalDevice, error) {
	d, err := scanDevice(s.pool.QueryRow(ctx, `SELECT `+sqlDeviceColumns+` FROM cloud_meta.approval_devices WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return ApprovalDevice{}, ErrDeviceNotFound
	}
	return d, err
}

func (s *PostgresDeviceStore) ListActive(ctx context.Context, tenant, subject string) ([]ApprovalDevice, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+sqlDeviceColumns+` FROM cloud_meta.approval_devices
WHERE tenant_id = $1 AND subject = $2 AND revoked_at IS NULL ORDER BY created_at DESC`, tenant, subject)
	if err != nil {
		return nil, fmt.Errorf("list devices: %w", err)
	}
	defer rows.Close()
	out := []ApprovalDevice{}
	for rows.Next() {
		d, err := scanDevice(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *PostgresDeviceStore) Revoke(ctx context.Context, id string, at time.Time) error {
	tag, err := s.pool.Exec(ctx, `UPDATE cloud_meta.approval_devices SET revoked_at = COALESCE(revoked_at, $2) WHERE id = $1`, id, at.UTC())
	if err != nil {
		return fmt.Errorf("revoke device: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrDeviceNotFound
	}
	return nil
}

func (s *PostgresDeviceStore) Touch(ctx context.Context, id string, at time.Time) error {
	// At most once a minute: a phone polls, and "last used" needs no finer.
	// Nothing updated is fine: touched within the minute, or (best-effort,
	// the middleware has already read the device) gone meanwhile.
	_, err := s.pool.Exec(ctx, `UPDATE cloud_meta.approval_devices SET last_used_at = $2::timestamptz
WHERE id = $1 AND (last_used_at IS NULL OR last_used_at < $2::timestamptz - interval '1 minute')`, id, at.UTC())
	if err != nil {
		return fmt.Errorf("touch device: %w", err)
	}
	return nil
}

func (s *PostgresDeviceStore) Relabel(ctx context.Context, id, name, platform string) error {
	if !ValidDevicePlatform(platform) {
		return fmt.Errorf("auth: unknown device platform %q", platform)
	}
	tag, err := s.pool.Exec(ctx, `UPDATE cloud_meta.approval_devices SET name = $2, platform = $3 WHERE id = $1`, id, name, platform)
	if err != nil {
		return fmt.Errorf("relabel device: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrDeviceNotFound
	}
	return nil
}

func (s *PostgresDeviceStore) FindByKey(ctx context.Context, tenant, subject string, key []byte) (ApprovalDevice, error) {
	d, err := scanDevice(s.pool.QueryRow(ctx, `SELECT `+sqlDeviceColumns+` FROM cloud_meta.approval_devices
WHERE tenant_id = $1 AND subject = $2 AND public_key = $3 AND revoked_at IS NULL ORDER BY created_at LIMIT 1`, tenant, subject, key))
	if errors.Is(err, pgx.ErrNoRows) {
		return ApprovalDevice{}, ErrDeviceNotFound
	}
	return d, err
}

func scanDevice(row devSessionRowScanner) (ApprovalDevice, error) {
	var d ApprovalDevice
	var attestation *string
	if err := row.Scan(&d.ID, &d.Tenant, &d.Subject, &d.Name, &d.Platform, &d.PublicKey, &attestation, &d.CreatedAt, &d.LastUsedAt, &d.RevokedAt); err != nil {
		return ApprovalDevice{}, err
	}
	if attestation != nil {
		d.Attestation = *attestation
	}
	return d, nil
}

// LinkLegacyDevices gives each developer session that carries a phone's key
// but names no device (signed in before approval devices) a device of its own, and
// names it on the session. Idempotent: run at startup, after the schema.
// The device takes the session's tenant, person, label and key; its
// platform is ios, the only kind that signed in with a key before.
func LinkLegacyDevices(ctx context.Context, pool *pgxpool.Pool, now time.Time) (int, error) {
	rows, err := pool.Query(ctx, `SELECT id, tenant_id, subject, label, device_public_key, created_at
FROM cloud_meta.developer_sessions
WHERE device_public_key IS NOT NULL AND device_id IS NULL AND revoked_at IS NULL AND expires_at > $1`, now.UTC())
	if err != nil {
		return 0, fmt.Errorf("list legacy phone sessions: %w", err)
	}
	type legacy struct {
		session string
		device  ApprovalDevice
	}
	var found []legacy
	for rows.Next() {
		var l legacy
		if err := rows.Scan(&l.session, &l.device.Tenant, &l.device.Subject, &l.device.Name, &l.device.PublicKey, &l.device.CreatedAt); err != nil {
			rows.Close()
			return 0, err
		}
		found = append(found, l)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	linked := 0
	for _, l := range found {
		d := l.device
		d.ID, d.Platform = NewDeviceID(), "ios"
		if err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
			// The same phone signed in more than once has one device: its
			// key's, if one is already kept.
			var existing string
			err := tx.QueryRow(ctx, `SELECT id FROM cloud_meta.approval_devices
WHERE tenant_id = $1 AND subject = $2 AND public_key = $3 AND revoked_at IS NULL ORDER BY created_at LIMIT 1`, d.Tenant, d.Subject, d.PublicKey).Scan(&existing)
			switch {
			case err == nil:
				d.ID = existing
			case errors.Is(err, pgx.ErrNoRows):
				// Another instance starting may insert the same key's device at
				// once: then that one is the device (one per key, by the index).
				tag, err := tx.Exec(ctx, `INSERT INTO cloud_meta.approval_devices (`+sqlDeviceColumns+`)
VALUES ($1, $2, $3, $4, $5, $6, NULL, $7, NULL, NULL)
ON CONFLICT (tenant_id, subject, public_key) WHERE revoked_at IS NULL DO NOTHING`, d.ID, d.Tenant, d.Subject, d.Name, d.Platform, d.PublicKey, d.CreatedAt.UTC())
				if err != nil {
					return err
				}
				if tag.RowsAffected() == 0 {
					if err := tx.QueryRow(ctx, `SELECT id FROM cloud_meta.approval_devices
WHERE tenant_id = $1 AND subject = $2 AND public_key = $3 AND revoked_at IS NULL`, d.Tenant, d.Subject, d.PublicKey).Scan(&d.ID); err != nil {
						return err
					}
				}
			default:
				return err
			}
			// Only if nothing linked it meanwhile (another instance starting).
			tag, err := tx.Exec(ctx, `UPDATE cloud_meta.developer_sessions SET device_id = $2 WHERE id = $1 AND device_id IS NULL`, l.session, d.ID)
			if err != nil {
				return err
			}
			if tag.RowsAffected() == 0 {
				return errAlreadyLinked
			}
			return nil
		}); errors.Is(err, errAlreadyLinked) {
			continue
		} else if err != nil {
			return linked, fmt.Errorf("link session %s to a device: %w", l.session, err)
		}
		linked++
	}
	return linked, nil
}

var errAlreadyLinked = errors.New("auth: session already linked")
