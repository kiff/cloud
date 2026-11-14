package auth

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kiff/cloud/api/internal/pgschema"
)

//go:embed sql/device_sign_ins.sql
var deviceSignInsSQL string

// ApplyDeviceSignInsSchema runs the device_sign_ins DDL. Idempotent.
func ApplyDeviceSignInsSchema(ctx context.Context, pool *pgxpool.Pool) error {
	if err := pgschema.Apply(ctx, pool, "auth/device_sign_ins", deviceSignInsSQL); err != nil {
		return fmt.Errorf("apply device_sign_ins schema: %w", err)
	}
	return nil
}

// WaitingSignIn is a device sign-in waiting for approval, as kept across
// restarts. It holds no secret: the device code as its hash, and nothing
// an approval mints.
type WaitingSignIn struct {
	DeviceHash    string
	UserCode      string
	Subject       string // a re-link's person; "" otherwise
	Tenant        string // a re-link's account; "" otherwise
	RelinkSession string
	DeviceKey     []byte
	ApprovalKey   []byte
	DeviceName    string
	Platform      string
	PlatformSent  bool
	Kind          string // the device's kind (DeviceKindOf)
	CreatedAt     time.Time
	ExpiresAt     time.Time
}

// ErrUserCodeTaken is a user code already waiting under another sign-in.
var ErrUserCodeTaken = errors.New("auth: user code already waiting")

// WaitingSignIns keeps device sign-ins waiting for approval. Expired ones
// are never returned.
type WaitingSignIns interface {
	Put(ctx context.Context, w WaitingSignIn) error
	// ByHash and ByUserCode return nil, nil when none waits.
	ByHash(ctx context.Context, hash string, now time.Time) (*WaitingSignIn, error)
	ByUserCode(ctx context.Context, code string, now time.Time) (*WaitingSignIn, error)
	Delete(ctx context.Context, hash string) error
	// Claim deletes the sign-in for approval, and says whether it was
	// still waiting: of two processes approving it at once, one wins.
	Claim(ctx context.Context, hash string, now time.Time) (bool, error)
	// DropRelinks deletes the re-links of session: one at a time.
	DropRelinks(ctx context.Context, session string) error
	// Sweep deletes the expired ones and counts those still waiting.
	Sweep(ctx context.Context, now time.Time) (int, error)
}

// PostgresWaitingSignIns keeps them in cloud_meta.device_sign_ins.
type PostgresWaitingSignIns struct{ pool *pgxpool.Pool }

// NewPostgresWaitingSignIns wraps pool; the caller ran ApplyDeviceSignInsSchema.
func NewPostgresWaitingSignIns(pool *pgxpool.Pool) *PostgresWaitingSignIns {
	return &PostgresWaitingSignIns{pool: pool}
}

const sqlWaitingColumns = `device_hash, user_code, subject, tenant_id, relink_session, device_key, approval_key, device_name, platform, platform_sent, created_at, expires_at, kind`

func (s *PostgresWaitingSignIns) Put(ctx context.Context, w WaitingSignIn) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO cloud_meta.device_sign_ins (`+sqlWaitingColumns+`)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)`,
		w.DeviceHash, w.UserCode, w.Subject, w.Tenant, w.RelinkSession, w.DeviceKey, w.ApprovalKey, w.DeviceName, w.Platform,
		w.PlatformSent, w.CreatedAt.UTC(), w.ExpiresAt.UTC(), w.Kind)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return ErrUserCodeTaken
	}
	if err != nil {
		return fmt.Errorf("keep device sign-in: %w", err)
	}
	return nil
}

func (s *PostgresWaitingSignIns) ByHash(ctx context.Context, hash string, now time.Time) (*WaitingSignIn, error) {
	return scanWaiting(s.pool.QueryRow(ctx, `SELECT `+sqlWaitingColumns+` FROM cloud_meta.device_sign_ins
WHERE device_hash = $1 AND expires_at >= $2`, hash, now.UTC()))
}

func (s *PostgresWaitingSignIns) ByUserCode(ctx context.Context, code string, now time.Time) (*WaitingSignIn, error) {
	return scanWaiting(s.pool.QueryRow(ctx, `SELECT `+sqlWaitingColumns+` FROM cloud_meta.device_sign_ins
WHERE user_code = $1 AND expires_at >= $2`, code, now.UTC()))
}

func (s *PostgresWaitingSignIns) Delete(ctx context.Context, hash string) error {
	if _, err := s.pool.Exec(ctx, `DELETE FROM cloud_meta.device_sign_ins WHERE device_hash = $1`, hash); err != nil {
		return fmt.Errorf("drop device sign-in: %w", err)
	}
	return nil
}

func (s *PostgresWaitingSignIns) Claim(ctx context.Context, hash string, now time.Time) (bool, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM cloud_meta.device_sign_ins WHERE device_hash = $1 AND expires_at >= $2`, hash, now.UTC())
	if err != nil {
		return false, fmt.Errorf("claim device sign-in: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

func (s *PostgresWaitingSignIns) DropRelinks(ctx context.Context, session string) error {
	if _, err := s.pool.Exec(ctx, `DELETE FROM cloud_meta.device_sign_ins WHERE relink_session = $1 AND relink_session <> ''`, session); err != nil {
		return fmt.Errorf("drop re-links: %w", err)
	}
	return nil
}

func (s *PostgresWaitingSignIns) Sweep(ctx context.Context, now time.Time) (int, error) {
	if _, err := s.pool.Exec(ctx, `DELETE FROM cloud_meta.device_sign_ins WHERE expires_at < $1`, now.UTC()); err != nil {
		return 0, fmt.Errorf("sweep device sign-ins: %w", err)
	}
	var n int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM cloud_meta.device_sign_ins`).Scan(&n); err != nil {
		return 0, fmt.Errorf("count device sign-ins: %w", err)
	}
	return n, nil
}

func scanWaiting(row pgx.Row) (*WaitingSignIn, error) {
	var w WaitingSignIn
	err := row.Scan(&w.DeviceHash, &w.UserCode, &w.Subject, &w.Tenant, &w.RelinkSession, &w.DeviceKey, &w.ApprovalKey, &w.DeviceName,
		&w.Platform, &w.PlatformSent, &w.CreatedAt, &w.ExpiresAt, &w.Kind)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read device sign-in: %w", err)
	}
	return &w, nil
}

// MemoryWaitingSignIns is WaitingSignIns in memory, for tests and an API
// without Postgres. Two handlers sharing one stand for a restart.
type MemoryWaitingSignIns struct {
	mu   sync.Mutex
	rows map[string]WaitingSignIn
}

// NewMemoryWaitingSignIns returns an empty store.
func NewMemoryWaitingSignIns() *MemoryWaitingSignIns {
	return &MemoryWaitingSignIns{rows: map[string]WaitingSignIn{}}
}

func (m *MemoryWaitingSignIns) Put(_ context.Context, w WaitingSignIn) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for h, r := range m.rows {
		if r.UserCode == w.UserCode && h != w.DeviceHash {
			return ErrUserCodeTaken
		}
	}
	m.rows[w.DeviceHash] = w
	return nil
}

func (m *MemoryWaitingSignIns) ByHash(_ context.Context, hash string, now time.Time) (*WaitingSignIn, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r, ok := m.rows[hash]; ok && !now.After(r.ExpiresAt) {
		return &r, nil
	}
	return nil, nil
}

func (m *MemoryWaitingSignIns) ByUserCode(_ context.Context, code string, now time.Time) (*WaitingSignIn, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range m.rows {
		if r.UserCode == code && !now.After(r.ExpiresAt) {
			return &r, nil
		}
	}
	return nil, nil
}

func (m *MemoryWaitingSignIns) Delete(_ context.Context, hash string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.rows, hash)
	return nil
}

func (m *MemoryWaitingSignIns) Claim(_ context.Context, hash string, now time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.rows[hash]
	if !ok || now.After(r.ExpiresAt) {
		return false, nil
	}
	delete(m.rows, hash)
	return true, nil
}

func (m *MemoryWaitingSignIns) DropRelinks(_ context.Context, session string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for h, r := range m.rows {
		if session != "" && r.RelinkSession == session {
			delete(m.rows, h)
		}
	}
	return nil
}

func (m *MemoryWaitingSignIns) Sweep(_ context.Context, now time.Time) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for h, r := range m.rows {
		if now.After(r.ExpiresAt) {
			delete(m.rows, h)
		}
	}
	return len(m.rows), nil
}
