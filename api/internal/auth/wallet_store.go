package auth

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed sql/wallets.sql
var walletsSQL string

// WalletsSchemaSQL returns the DDL that creates cloud_meta.wallet_nonces
// and cloud_meta.user_wallets. Idempotent.
func WalletsSchemaSQL() string { return walletsSQL }

// ApplyWalletsSchema runs the wallets DDL against the given pool.
// Idempotent. Call once at API startup, after ApplyCloudMeta.
func ApplyWalletsSchema(ctx context.Context, pool *pgxpool.Pool) error {
	if _, err := pool.Exec(ctx, walletsSQL); err != nil {
		return fmt.Errorf("apply wallets schema: %w", err)
	}
	return nil
}

// MemoryWalletNonceStore is the in-memory WalletNonceStore. Concurrent
// access is serialized via a mutex; MarkUsed is compare-and-swap so
// concurrent verifies cannot both succeed.
type MemoryWalletNonceStore struct {
	mu      sync.Mutex
	byNonce map[string]WalletNonce
}

// NewMemoryWalletNonceStore returns an empty store.
func NewMemoryWalletNonceStore() *MemoryWalletNonceStore {
	return &MemoryWalletNonceStore{byNonce: make(map[string]WalletNonce)}
}

// Insert implements WalletNonceStore.
func (s *MemoryWalletNonceStore) Insert(_ context.Context, n WalletNonce) error {
	if n.Nonce == "" || n.Subject == "" {
		return errors.New("auth: nonce record missing required fields")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, dup := s.byNonce[n.Nonce]; dup {
		return errors.New("auth: nonce already exists")
	}
	s.byNonce[n.Nonce] = n
	return nil
}

// Get implements WalletNonceStore.
func (s *MemoryWalletNonceStore) Get(_ context.Context, nonce string) (WalletNonce, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.byNonce[nonce]
	if !ok {
		return WalletNonce{}, ErrWalletNonceNotFound
	}
	return rec, nil
}

// MarkUsed implements WalletNonceStore. Compare-and-swap: returns
// ErrWalletNonceReplayed when the nonce is already used.
func (s *MemoryWalletNonceStore) MarkUsed(_ context.Context, nonce string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.byNonce[nonce]
	if !ok {
		return ErrWalletNonceNotFound
	}
	if rec.UsedAt != nil {
		return ErrWalletNonceReplayed
	}
	stamp := at.UTC()
	rec.UsedAt = &stamp
	s.byNonce[nonce] = rec
	return nil
}

// MemoryWalletStore is the in-memory WalletStore.
type MemoryWalletStore struct {
	mu        sync.RWMutex
	bySubject map[string]WalletLink
}

// NewMemoryWalletStore returns an empty store.
func NewMemoryWalletStore() *MemoryWalletStore {
	return &MemoryWalletStore{bySubject: make(map[string]WalletLink)}
}

// Upsert implements WalletStore.
func (s *MemoryWalletStore) Upsert(_ context.Context, subject string, link WalletLink) error {
	if subject == "" {
		return errors.New("auth: empty subject")
	}
	if err := link.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.bySubject[subject] = link
	return nil
}

// Get implements WalletStore.
func (s *MemoryWalletStore) Get(_ context.Context, subject string) (WalletLink, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	link, ok := s.bySubject[subject]
	if !ok {
		return WalletLink{}, ErrWalletNotLinked
	}
	return link, nil
}

// Delete implements WalletStore. Idempotent.
func (s *MemoryWalletStore) Delete(_ context.Context, subject string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.bySubject, subject)
	return nil
}

// PostgresWalletNonceStore writes to cloud_meta.wallet_nonces.
type PostgresWalletNonceStore struct {
	pool *pgxpool.Pool
}

// NewPostgresWalletNonceStore wraps the given pool. The caller is
// responsible for the pool's lifetime and for ApplyWalletsSchema
// having been called.
func NewPostgresWalletNonceStore(pool *pgxpool.Pool) *PostgresWalletNonceStore {
	return &PostgresWalletNonceStore{pool: pool}
}

const sqlInsertWalletNonce = `
INSERT INTO cloud_meta.wallet_nonces (nonce, subject, address, chain_id, issued_at, expires_at)
VALUES ($1, $2, $3, $4, $5, $6)
`

const sqlSelectWalletNonce = `
SELECT nonce, subject, address, chain_id, issued_at, expires_at, used_at
FROM cloud_meta.wallet_nonces
WHERE nonce = $1
`

// MarkUsed uses an atomic UPDATE to ensure only the first verifier
// wins. RowsAffected = 0 means the nonce is either gone or already
// used; we re-read to disambiguate.
const sqlMarkWalletNonceUsed = `
UPDATE cloud_meta.wallet_nonces
SET used_at = $2
WHERE nonce = $1 AND used_at IS NULL
`

// Insert implements WalletNonceStore.
func (s *PostgresWalletNonceStore) Insert(ctx context.Context, n WalletNonce) error {
	if _, err := s.pool.Exec(ctx, sqlInsertWalletNonce,
		n.Nonce, n.Subject, n.Address, int64(n.ChainID), n.IssuedAt, n.ExpiresAt,
	); err != nil {
		return fmt.Errorf("insert wallet nonce: %w", err)
	}
	return nil
}

// Get implements WalletNonceStore.
func (s *PostgresWalletNonceStore) Get(ctx context.Context, nonce string) (WalletNonce, error) {
	var (
		rec    WalletNonce
		chain  int64
		usedAt *time.Time
	)
	err := s.pool.QueryRow(ctx, sqlSelectWalletNonce, nonce).Scan(
		&rec.Nonce, &rec.Subject, &rec.Address, &chain, &rec.IssuedAt, &rec.ExpiresAt, &usedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return WalletNonce{}, ErrWalletNonceNotFound
		}
		return WalletNonce{}, fmt.Errorf("select wallet nonce: %w", err)
	}
	rec.ChainID = uint64(chain)
	rec.UsedAt = usedAt
	return rec, nil
}

// MarkUsed implements WalletNonceStore.
func (s *PostgresWalletNonceStore) MarkUsed(ctx context.Context, nonce string, at time.Time) error {
	tag, err := s.pool.Exec(ctx, sqlMarkWalletNonceUsed, nonce, at.UTC())
	if err != nil {
		return fmt.Errorf("mark wallet nonce used: %w", err)
	}
	if tag.RowsAffected() == 0 {
		// Disambiguate "not found" vs "already used".
		_, err := s.Get(ctx, nonce)
		if errors.Is(err, ErrWalletNonceNotFound) {
			return ErrWalletNonceNotFound
		}
		return ErrWalletNonceReplayed
	}
	return nil
}

// PostgresWalletStore writes to cloud_meta.user_wallets.
type PostgresWalletStore struct {
	pool *pgxpool.Pool
}

// NewPostgresWalletStore wraps the given pool.
func NewPostgresWalletStore(pool *pgxpool.Pool) *PostgresWalletStore {
	return &PostgresWalletStore{pool: pool}
}

const sqlUpsertWallet = `
INSERT INTO cloud_meta.user_wallets (subject, address, chain_id, linked_at)
VALUES ($1, $2, $3, $4)
ON CONFLICT (subject) DO UPDATE SET
    address = EXCLUDED.address,
    chain_id = EXCLUDED.chain_id,
    linked_at = EXCLUDED.linked_at
`

const sqlSelectWallet = `
SELECT address, chain_id, linked_at
FROM cloud_meta.user_wallets
WHERE subject = $1
`

const sqlDeleteWallet = `
DELETE FROM cloud_meta.user_wallets
WHERE subject = $1
`

// Upsert implements WalletStore.
func (s *PostgresWalletStore) Upsert(ctx context.Context, subject string, link WalletLink) error {
	if subject == "" {
		return errors.New("auth: empty subject")
	}
	if err := link.Validate(); err != nil {
		return err
	}
	if _, err := s.pool.Exec(ctx, sqlUpsertWallet,
		subject, link.Address, int64(link.ChainID), link.LinkedAt.UTC(),
	); err != nil {
		return fmt.Errorf("upsert wallet: %w", err)
	}
	return nil
}

// Get implements WalletStore.
func (s *PostgresWalletStore) Get(ctx context.Context, subject string) (WalletLink, error) {
	var (
		link  WalletLink
		chain int64
	)
	err := s.pool.QueryRow(ctx, sqlSelectWallet, subject).Scan(&link.Address, &chain, &link.LinkedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return WalletLink{}, ErrWalletNotLinked
		}
		return WalletLink{}, fmt.Errorf("select wallet: %w", err)
	}
	link.ChainID = uint64(chain)
	return link, nil
}

// Delete implements WalletStore.
func (s *PostgresWalletStore) Delete(ctx context.Context, subject string) error {
	if _, err := s.pool.Exec(ctx, sqlDeleteWallet, subject); err != nil {
		return fmt.Errorf("delete wallet: %w", err)
	}
	return nil
}
