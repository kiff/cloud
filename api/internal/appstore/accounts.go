package appstore

import (
	"context"
	"crypto/rand"
	_ "embed"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kiff/cloud/api/internal/pgschema"
)

//go:embed sql/appstore.sql
var schemaSQL string

// ApplySchema creates cloud_meta.apple_accounts. Idempotent. Run after
// cloud_meta.tenants exists.
func ApplySchema(ctx context.Context, pool *pgxpool.Pool) error {
	if err := pgschema.Apply(ctx, pool, "appstore/appstore", schemaSQL); err != nil {
		return fmt.Errorf("apply appstore schema: %w", err)
	}
	return nil
}

// Accounts keeps each account's appAccountToken.
type Accounts interface {
	// Token is tenantID's token, made the first time it is asked for.
	Token(ctx context.Context, tenantID string) (string, error)
	// TenantFor is the account token is for; "" when none.
	TenantFor(ctx context.Context, token string) (string, error)
}

// newToken is a random (version 4) UUID, lowercase.
func newToken() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// normalToken is a token as KIFF keeps it: trimmed and lowercase (the
// app's UUID prints in capitals).
func normalToken(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// PostgresAccounts keeps the tokens in cloud_meta.apple_accounts.
type PostgresAccounts struct {
	pool *pgxpool.Pool
}

// NewPostgresAccounts keeps the tokens through pool; the caller applies
// the schema.
func NewPostgresAccounts(pool *pgxpool.Pool) *PostgresAccounts {
	return &PostgresAccounts{pool: pool}
}

// Token implements Accounts: of two first reads at once, both get the
// token the first one stored.
func (a *PostgresAccounts) Token(ctx context.Context, tenantID string) (string, error) {
	if _, err := a.pool.Exec(ctx, `INSERT INTO cloud_meta.apple_accounts (tenant_id, app_account_token) VALUES ($1, $2)
		ON CONFLICT (tenant_id) DO NOTHING`, tenantID, newToken()); err != nil {
		return "", fmt.Errorf("appstore: token: %w", err)
	}
	var token string
	if err := a.pool.QueryRow(ctx, `SELECT app_account_token::text FROM cloud_meta.apple_accounts WHERE tenant_id = $1`, tenantID).Scan(&token); err != nil {
		return "", fmt.Errorf("appstore: token: %w", err)
	}
	return token, nil
}

// TenantFor implements Accounts.
func (a *PostgresAccounts) TenantFor(ctx context.Context, token string) (string, error) {
	token = normalToken(token)
	if token == "" {
		return "", nil
	}
	var tenantID string
	err := a.pool.QueryRow(ctx, `SELECT tenant_id FROM cloud_meta.apple_accounts WHERE app_account_token::text = $1`, token).Scan(&tenantID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("appstore: account for token: %w", err)
	}
	return tenantID, nil
}

// MemoryAccounts keeps the tokens in process, for tests and an API without
// a database.
type MemoryAccounts struct {
	mu     sync.Mutex
	tokens map[string]string // tenant → token
}

// NewMemoryAccounts returns an empty store.
func NewMemoryAccounts() *MemoryAccounts {
	return &MemoryAccounts{tokens: map[string]string{}}
}

// Token implements Accounts.
func (m *MemoryAccounts) Token(_ context.Context, tenantID string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if t, ok := m.tokens[tenantID]; ok {
		return t, nil
	}
	m.tokens[tenantID] = newToken()
	return m.tokens[tenantID], nil
}

// TenantFor implements Accounts.
func (m *MemoryAccounts) TenantFor(_ context.Context, token string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	token = normalToken(token)
	for tenantID, t := range m.tokens {
		if t == token && token != "" {
			return tenantID, nil
		}
	}
	return "", nil
}

var (
	_ Accounts = (*PostgresAccounts)(nil)
	_ Accounts = (*MemoryAccounts)(nil)
)
