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

//go:embed sql/pairings.sql
var pairingsSQL string

// ApplyPairingsSchema runs the pairings DDL. Idempotent. Call at API
// startup after ApplyDevicesSchema.
func ApplyPairingsSchema(ctx context.Context, pool *pgxpool.Pool) error {
	if err := pgschema.Apply(ctx, pool, "auth/pairings", pairingsSQL); err != nil {
		return fmt.Errorf("apply pairings schema: %w", err)
	}
	return nil
}

// PostgresPairingStore keeps pairings in cloud_meta.pairings.
type PostgresPairingStore struct {
	pool *pgxpool.Pool
}

// NewPostgresPairingStore wraps pool; the caller ran ApplyPairingsSchema.
func NewPostgresPairingStore(pool *pgxpool.Pool) *PostgresPairingStore {
	return &PostgresPairingStore{pool: pool}
}

const sqlPairingColumns = `id, tenant_id, subject, approved_by, public_key, approval_public_key, name, platform, kind, nonce_hash, expires_at, created_at, claimed_at, credential_id, credential_public_key, device_kind, key_store`

func (s *PostgresPairingStore) Insert(ctx context.Context, p Pairing) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO cloud_meta.pairings (`+sqlPairingColumns+`)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, NULL, $13, $14, $15, $16)`,
		p.ID, p.Tenant, p.Subject, p.ApprovedBy, p.PublicKey, p.ApprovalKey, p.Name, p.Platform, p.Kind, p.NonceHash, p.Expires.UTC(), p.CreatedAt.UTC(),
		p.CredentialID, p.CredentialKey, p.DeviceKind, p.KeyStore)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return ErrPairingUsed
	}
	if err != nil {
		return fmt.Errorf("insert pairing: %w", err)
	}
	return nil
}

func (s *PostgresPairingStore) ByNonce(ctx context.Context, nonceHash []byte) (Pairing, error) {
	var p Pairing
	err := s.pool.QueryRow(ctx, `SELECT `+sqlPairingColumns+` FROM cloud_meta.pairings WHERE nonce_hash = $1`, nonceHash).Scan(
		&p.ID, &p.Tenant, &p.Subject, &p.ApprovedBy, &p.PublicKey, &p.ApprovalKey, &p.Name, &p.Platform, &p.Kind, &p.NonceHash, &p.Expires, &p.CreatedAt, &p.ClaimedAt, &p.CredentialID, &p.CredentialKey, &p.DeviceKind, &p.KeyStore)
	if errors.Is(err, pgx.ErrNoRows) {
		return Pairing{}, ErrPairingNotFound
	}
	if err != nil {
		return Pairing{}, fmt.Errorf("read pairing: %w", err)
	}
	return p, nil
}

func (s *PostgresPairingStore) Claim(ctx context.Context, id string, at time.Time) error {
	tag, err := s.pool.Exec(ctx, `UPDATE cloud_meta.pairings SET claimed_at = $2 WHERE id = $1 AND claimed_at IS NULL`, id, at.UTC())
	if err != nil {
		return fmt.Errorf("claim pairing: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrPairingClaimed
	}
	return nil
}

func (s *PostgresPairingStore) DropUnclaimedBy(ctx context.Context, deviceID string) error {
	if _, err := s.pool.Exec(ctx, `DELETE FROM cloud_meta.pairings WHERE approved_by = $1 AND claimed_at IS NULL`, deviceID); err != nil {
		return fmt.Errorf("drop unclaimed pairings: %w", err)
	}
	return nil
}

// DeleteExpired deletes, on every account, the unclaimed pairings whose exp is before
// before, and returns how many went. A claimed one stays (PairingKeptAfterExpiry).
// Refusals that expired before that time go too.
func (s *PostgresPairingStore) DeleteExpired(ctx context.Context, before time.Time) (int64, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM cloud_meta.pairings WHERE expires_at < $1 AND claimed_at IS NULL`, before.UTC())
	if err != nil {
		return 0, fmt.Errorf("delete expired pairings: %w", err)
	}
	if _, err := s.pool.Exec(ctx, `DELETE FROM cloud_meta.pairing_refusals WHERE expires_at < $1`, before.UTC()); err != nil {
		return tag.RowsAffected(), fmt.Errorf("delete expired pairing refusals: %w", err)
	}
	return tag.RowsAffected(), nil
}

func (s *PostgresPairingStore) Refuse(ctx context.Context, r PairingRefusal) error {
	if _, err := s.pool.Exec(ctx, `INSERT INTO cloud_meta.pairing_refusals (nonce_hash, pub_hash, reason, expires_at, created_at)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (nonce_hash) DO UPDATE SET pub_hash = EXCLUDED.pub_hash, reason = EXCLUDED.reason, expires_at = EXCLUDED.expires_at, created_at = EXCLUDED.created_at`,
		r.NonceHash, r.PubHash, r.Reason, r.Expires.UTC(), r.CreatedAt.UTC()); err != nil {
		return fmt.Errorf("keep pairing refusal: %w", err)
	}
	return nil
}

func (s *PostgresPairingStore) RefusalByNonce(ctx context.Context, nonceHash []byte, now time.Time) (PairingRefusal, error) {
	var r PairingRefusal
	err := s.pool.QueryRow(ctx, `SELECT nonce_hash, pub_hash, reason, expires_at, created_at FROM cloud_meta.pairing_refusals
WHERE nonce_hash = $1 AND expires_at >= $2`, nonceHash, now.UTC()).Scan(&r.NonceHash, &r.PubHash, &r.Reason, &r.Expires, &r.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return PairingRefusal{}, ErrPairingNotRefused
	}
	if err != nil {
		return PairingRefusal{}, fmt.Errorf("read pairing refusal: %w", err)
	}
	return r, nil
}

func (s *PostgresPairingStore) Unclaim(ctx context.Context, id string) error {
	if _, err := s.pool.Exec(ctx, `UPDATE cloud_meta.pairings SET claimed_at = NULL WHERE id = $1`, id); err != nil {
		return fmt.Errorf("unclaim pairing: %w", err)
	}
	return nil
}
