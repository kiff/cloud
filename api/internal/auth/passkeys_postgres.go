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

	"github.com/kiff/cloud/api/internal/pgschema"
)

//go:embed sql/passkeys.sql
var passkeysSQL string

// ApplyPasskeysSchema runs the passkeys DDL. Idempotent. Call at API
// startup after ApplyDevicesSchema.
func ApplyPasskeysSchema(ctx context.Context, pool *pgxpool.Pool) error {
	if err := pgschema.Apply(ctx, pool, "auth/passkeys", passkeysSQL); err != nil {
		return fmt.Errorf("apply passkeys schema: %w", err)
	}
	return nil
}

// PostgresPasskeyStore keeps passkeys in cloud_meta.passkeys.
type PostgresPasskeyStore struct {
	pool *pgxpool.Pool
}

// NewPostgresPasskeyStore wraps pool; the caller ran ApplyPasskeysSchema.
func NewPostgresPasskeyStore(pool *pgxpool.Pool) *PostgresPasskeyStore {
	return &PostgresPasskeyStore{pool: pool}
}

func (s *PostgresPasskeyStore) Insert(ctx context.Context, p StoredPasskey) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO cloud_meta.passkeys (tenant_id, credential_id, subject, device_id, public_key, sign_count, created_at, key_id, name, synced)
VALUES ($1, $2, $3, $4, $5, $6, $7, NULLIF($8, ''), $9, $10)`, p.Tenant, p.ID, p.Subject, p.DeviceID, p.PublicKey, int64(p.SignCount), p.CreatedAt.UTC(), p.KeyID, p.Name, p.Synced)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return ErrPasskeyDuplicate
	}
	if err != nil {
		return fmt.Errorf("insert passkey: %w", err)
	}
	return nil
}

const sqlPasskeyColumns = `tenant_id, credential_id, subject, device_id, public_key, sign_count, created_at, revoked_at, COALESCE(key_id, ''), name, synced`

func scanPasskey(row pgx.Row) (StoredPasskey, error) {
	var p StoredPasskey
	var count int64
	if err := row.Scan(&p.Tenant, &p.ID, &p.Subject, &p.DeviceID, &p.PublicKey, &count, &p.CreatedAt, &p.RevokedAt, &p.KeyID, &p.Name, &p.Synced); err != nil {
		return StoredPasskey{}, err
	}
	p.SignCount = uint32(count)
	return p, nil
}

func (s *PostgresPasskeyStore) Find(ctx context.Context, tenant string, credentialID []byte) (StoredPasskey, error) {
	p, err := scanPasskey(s.pool.QueryRow(ctx, `SELECT `+sqlPasskeyColumns+`
FROM cloud_meta.passkeys WHERE tenant_id = $1 AND credential_id = $2`, tenant, credentialID))
	if errors.Is(err, pgx.ErrNoRows) {
		return StoredPasskey{}, ErrPasskeyNotFound
	}
	if err != nil {
		return StoredPasskey{}, fmt.Errorf("read passkey: %w", err)
	}
	return p, nil
}

func (s *PostgresPasskeyStore) SetSignCount(ctx context.Context, tenant string, credentialID []byte, count uint32, synced bool) error {
	if _, err := s.pool.Exec(ctx, `UPDATE cloud_meta.passkeys SET sign_count = GREATEST(sign_count, $3), synced = $4
WHERE tenant_id = $1 AND credential_id = $2`, tenant, credentialID, int64(count), synced); err != nil {
		return fmt.Errorf("store passkey counter: %w", err)
	}
	return nil
}

func (s *PostgresPasskeyStore) ListForPerson(ctx context.Context, tenant, subject string) ([]StoredPasskey, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+sqlPasskeyColumns+` FROM cloud_meta.passkeys
WHERE tenant_id = $1 AND subject = $2 AND revoked_at IS NULL ORDER BY created_at, credential_id`, tenant, subject)
	if err != nil {
		return nil, fmt.Errorf("list passkeys: %w", err)
	}
	defer rows.Close()
	var out []StoredPasskey
	for rows.Next() {
		p, err := scanPasskey(rows)
		if err != nil {
			return nil, fmt.Errorf("read passkey: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *PostgresPasskeyStore) ListActiveInTenant(ctx context.Context, tenant string) ([]StoredPasskey, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+sqlPasskeyColumns+` FROM cloud_meta.passkeys
WHERE tenant_id = $1 AND revoked_at IS NULL ORDER BY created_at, credential_id`, tenant)
	if err != nil {
		return nil, fmt.Errorf("list passkeys: %w", err)
	}
	defer rows.Close()
	var out []StoredPasskey
	for rows.Next() {
		p, err := scanPasskey(rows)
		if err != nil {
			return nil, fmt.Errorf("read passkey: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *PostgresPasskeyStore) Revoke(ctx context.Context, tenant, subject, keyID string, at time.Time) error {
	tag, err := s.pool.Exec(ctx, `UPDATE cloud_meta.passkeys SET revoked_at = $4
WHERE tenant_id = $1 AND subject = $2 AND key_id = $3 AND revoked_at IS NULL`, tenant, subject, keyID, at.UTC())
	if err != nil {
		return fmt.Errorf("remove passkey: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrPasskeyNotFound
	}
	return nil
}

func (s *PostgresPasskeyStore) RevokeForDevice(ctx context.Context, deviceID string, at time.Time) error {
	if deviceID == "" {
		return nil
	}
	if _, err := s.pool.Exec(ctx, `UPDATE cloud_meta.passkeys SET revoked_at = $2
WHERE device_id = $1 AND revoked_at IS NULL`, deviceID, at.UTC()); err != nil {
		return fmt.Errorf("remove the device's passkeys: %w", err)
	}
	return nil
}
