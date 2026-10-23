package auth

import (
	"context"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed sql/developer_sessions.sql
var developerSessionsSQL string

// DeveloperSessionsSchemaSQL returns the DDL that creates
// cloud_meta.developer_sessions. Idempotent.
func DeveloperSessionsSchemaSQL() string { return developerSessionsSQL }

// ApplyDeveloperSessionsSchema runs the developer_sessions DDL against
// pool. Idempotent. Call once at API startup, after ApplyCloudMeta
// (the foreign key requires cloud_meta.tenants).
func ApplyDeveloperSessionsSchema(ctx context.Context, pool *pgxpool.Pool) error {
	if _, err := pool.Exec(ctx, developerSessionsSQL); err != nil {
		return fmt.Errorf("apply developer_sessions schema: %w", err)
	}
	return nil
}

// PostgresDeveloperSessionStore persists developer sessions in
// cloud_meta.developer_sessions. Queries are schema-qualified so the
// pool's search_path need not point at cloud_meta.
type PostgresDeveloperSessionStore struct {
	pool *pgxpool.Pool
}

// NewPostgresDeveloperSessionStore wraps pool. The caller owns the
// pool's lifetime and must have run ApplyDeveloperSessionsSchema.
func NewPostgresDeveloperSessionStore(pool *pgxpool.Pool) *PostgresDeveloperSessionStore {
	return &PostgresDeveloperSessionStore{pool: pool}
}

const sqlInsertDevSession = `
INSERT INTO cloud_meta.developer_sessions (id, tenant_id, subject, label, token_hash, created_at, expires_at, device_public_key, device_id)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, NULLIF($9, ''))
`

const sqlSelectDevSessionByHash = `
SELECT id, tenant_id, subject, label, token_hash, created_at, expires_at, last_seen_at, revoked_at, device_public_key, COALESCE(device_id, '')
FROM cloud_meta.developer_sessions
WHERE token_hash = $1
`

const sqlTouchDevSession = `
UPDATE cloud_meta.developer_sessions SET last_seen_at = $2 WHERE id = $1
`

const sqlRevokeDevSession = `
UPDATE cloud_meta.developer_sessions SET revoked_at = $2 WHERE id = $1 AND revoked_at IS NULL
`

const sqlRevokeDevSessionByHash = `
UPDATE cloud_meta.developer_sessions SET revoked_at = $2 WHERE token_hash = $1 AND revoked_at IS NULL
`

const sqlListActiveDevSessions = `
SELECT id, tenant_id, subject, label, token_hash, created_at, expires_at, last_seen_at, revoked_at, device_public_key, COALESCE(device_id, '')
FROM cloud_meta.developer_sessions
WHERE subject = $1 AND revoked_at IS NULL AND expires_at > $2
ORDER BY created_at DESC
`

const sqlRevokeAllDevSessionsForTenant = `
UPDATE cloud_meta.developer_sessions SET revoked_at = $2 WHERE tenant_id = $1 AND revoked_at IS NULL
`

const sqlRevokeAllDevSessionsForSubject = `
UPDATE cloud_meta.developer_sessions SET revoked_at = $2 WHERE subject = $1 AND revoked_at IS NULL
`

func (s *PostgresDeveloperSessionStore) Insert(ctx context.Context, sess DeveloperSession) error {
	if strings.TrimSpace(sess.ID) == "" {
		return errors.New("auth: developer session id required")
	}
	if strings.TrimSpace(sess.Subject) == "" || strings.TrimSpace(sess.Tenant) == "" {
		return errors.New("auth: developer session subject and tenant required")
	}
	if sess.ExpiresAt.IsZero() {
		return errors.New("auth: developer session expires_at required")
	}
	_, err := s.pool.Exec(ctx, sqlInsertDevSession,
		sess.ID, sess.Tenant, sess.Subject, sess.Label, hex.EncodeToString(sess.TokenHash[:]), sess.CreatedAt.UTC(), sess.ExpiresAt.UTC(),
		deviceKeyColumn(sess.DevicePublicKey), sess.DeviceID,
	)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" { // unique_violation
			return fmt.Errorf("%w: id=%q", ErrDevSessionDuplicate, sess.ID)
		}
		return fmt.Errorf("insert developer session: %w", err)
	}
	return nil
}

func (s *PostgresDeveloperSessionStore) GetByHash(ctx context.Context, hash [32]byte, now time.Time) (DeveloperSession, error) {
	row := s.pool.QueryRow(ctx, sqlSelectDevSessionByHash, hex.EncodeToString(hash[:]))
	sess, err := scanDevSession(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return DeveloperSession{}, ErrDevSessionNotFound
		}
		return DeveloperSession{}, err
	}
	if sess.IsRevoked() {
		return sess, ErrDevSessionRevoked
	}
	if sess.IsExpired(now) {
		return sess, ErrDevSessionExpired
	}
	return sess, nil
}

func (s *PostgresDeveloperSessionStore) Touch(ctx context.Context, id string, at time.Time) error {
	tag, err := s.pool.Exec(ctx, sqlTouchDevSession, id, at.UTC())
	if err != nil {
		return fmt.Errorf("touch developer session: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrDevSessionNotFound
	}
	return nil
}

func (s *PostgresDeveloperSessionStore) Revoke(ctx context.Context, id string, at time.Time) error {
	tag, err := s.pool.Exec(ctx, sqlRevokeDevSession, id, at.UTC())
	if err != nil {
		return fmt.Errorf("revoke developer session: %w", err)
	}
	if tag.RowsAffected() == 0 {
		var exists bool
		if err := s.pool.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM cloud_meta.developer_sessions WHERE id = $1)", id).Scan(&exists); err != nil {
			return fmt.Errorf("check developer session existence: %w", err)
		}
		if !exists {
			return ErrDevSessionNotFound
		}
		// Already revoked — idempotent.
	}
	return nil
}

func (s *PostgresDeveloperSessionStore) RevokeByHash(ctx context.Context, hash [32]byte, at time.Time) error {
	tag, err := s.pool.Exec(ctx, sqlRevokeDevSessionByHash, hex.EncodeToString(hash[:]), at.UTC())
	if err != nil {
		return fmt.Errorf("revoke developer session by hash: %w", err)
	}
	if tag.RowsAffected() == 0 {
		var exists bool
		if err := s.pool.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM cloud_meta.developer_sessions WHERE token_hash = $1)", hex.EncodeToString(hash[:])).Scan(&exists); err != nil {
			return fmt.Errorf("check developer session existence: %w", err)
		}
		if !exists {
			return ErrDevSessionNotFound
		}
		// Already revoked — idempotent.
	}
	return nil
}

func (s *PostgresDeveloperSessionStore) ListActiveForSubject(ctx context.Context, subject string, now time.Time) ([]DeveloperSession, error) {
	rows, err := s.pool.Query(ctx, sqlListActiveDevSessions, subject, now.UTC())
	if err != nil {
		return nil, fmt.Errorf("list developer sessions: %w", err)
	}
	defer rows.Close()
	out := make([]DeveloperSession, 0)
	for rows.Next() {
		sess, err := scanDevSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sess)
	}
	return out, rows.Err()
}

func (s *PostgresDeveloperSessionStore) RevokeAllForSubject(ctx context.Context, subject string, at time.Time) (int, error) {
	tag, err := s.pool.Exec(ctx, sqlRevokeAllDevSessionsForSubject, subject, at.UTC())
	if err != nil {
		return 0, fmt.Errorf("revoke all developer sessions for subject: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

func (s *PostgresDeveloperSessionStore) RevokeAllForTenant(ctx context.Context, tenant string, at time.Time) error {
	if _, err := s.pool.Exec(ctx, sqlRevokeAllDevSessionsForTenant, tenant, at.UTC()); err != nil {
		return fmt.Errorf("revoke all developer sessions for tenant: %w", err)
	}
	return nil
}

// devSessionRowScanner is satisfied by both pgx.Row and pgx.Rows.
type devSessionRowScanner interface {
	Scan(dest ...any) error
}

func scanDevSession(row devSessionRowScanner) (DeveloperSession, error) {
	var (
		sess     DeveloperSession
		hashHex  string
		lastSeen *time.Time
		revoked  *time.Time
	)
	if err := row.Scan(&sess.ID, &sess.Tenant, &sess.Subject, &sess.Label, &hashHex, &sess.CreatedAt, &sess.ExpiresAt, &lastSeen, &revoked, &sess.DevicePublicKey, &sess.DeviceID); err != nil {
		return DeveloperSession{}, err
	}
	hashBytes, err := hex.DecodeString(strings.TrimSpace(hashHex))
	if err != nil {
		return DeveloperSession{}, fmt.Errorf("decode token_hash: %w", err)
	}
	if len(hashBytes) != 32 {
		return DeveloperSession{}, fmt.Errorf("token_hash length: got %d, want 32", len(hashBytes))
	}
	copy(sess.TokenHash[:], hashBytes)
	sess.LastSeenAt = lastSeen
	sess.RevokedAt = revoked
	return sess, nil
}

// deviceKeyColumn stores a missing key as NULL, not an empty value.
func deviceKeyColumn(k []byte) any {
	if len(k) == 0 {
		return nil
	}
	return k
}

// Compile-time assertion that the Postgres store satisfies the interface.
var _ DeveloperSessionStore = (*PostgresDeveloperSessionStore)(nil)
