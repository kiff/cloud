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

const sqlPairingColumns = `id, tenant_id, subject, approved_by, public_key, approval_public_key, name, platform, kind, nonce_hash, expires_at, created_at, claimed_at`

func (s *PostgresPairingStore) Insert(ctx context.Context, p Pairing) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO cloud_meta.pairings (`+sqlPairingColumns+`)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, NULL)`,
		p.ID, p.Tenant, p.Subject, p.ApprovedBy, p.PublicKey, p.ApprovalKey, p.Name, p.Platform, p.Kind, p.NonceHash, p.Expires.UTC(), p.CreatedAt.UTC())
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
		&p.ID, &p.Tenant, &p.Subject, &p.ApprovedBy, &p.PublicKey, &p.ApprovalKey, &p.Name, &p.Platform, &p.Kind, &p.NonceHash, &p.Expires, &p.CreatedAt, &p.ClaimedAt)
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

func (s *PostgresPairingStore) Unclaim(ctx context.Context, id string) error {
	if _, err := s.pool.Exec(ctx, `UPDATE cloud_meta.pairings SET claimed_at = NULL WHERE id = $1`, id); err != nil {
		return fmt.Errorf("unclaim pairing: %w", err)
	}
	return nil
}
