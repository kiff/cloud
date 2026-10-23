package auth

import (
	"context"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed sql/api_keys.sql
var apiKeysSQL string

// APIKeysSchemaSQL returns the DDL that creates cloud_meta.api_keys.
// Idempotent — every CREATE uses IF NOT EXISTS. Exposed so tests
// and the kiff-tenant CLI can apply the schema with a single call.
func APIKeysSchemaSQL() string { return apiKeysSQL }

// ApplyAPIKeysSchema runs the api_keys DDL against the given pool.
// Idempotent. Call once at API startup, after ApplyCloudMeta (the
// foreign key requires cloud_meta.tenants to exist).
func ApplyAPIKeysSchema(ctx context.Context, pool *pgxpool.Pool) error {
	if _, err := pool.Exec(ctx, apiKeysSQL); err != nil {
		return fmt.Errorf("apply api_keys schema: %w", err)
	}
	return nil
}

// BackfillSetupReader gives RoleSetupReader to the live keys claimed from a
// no-tools draft (an agent that holds no tools, kiff/agents#91) before it
// existed: the key a draft with no Cards names (agent_drafts.key_id) that
// carries no roles ([] or null, as Insert wrote a nil slice). They can then
// read a read-only tool on a Card of their own (kiff/tafs#138), as a key
// minted now does. A database without drafts has none. Idempotent; it
// answers how many it changed.
func BackfillSetupReader(ctx context.Context, pool *pgxpool.Pool) (int64, error) {
	var drafts bool
	if err := pool.QueryRow(ctx, `SELECT to_regclass('cloud_meta.agent_drafts') IS NOT NULL`).Scan(&drafts); err != nil {
		return 0, fmt.Errorf("backfill setup_reader: %w", err)
	}
	if !drafts {
		return 0, nil
	}
	tag, err := pool.Exec(ctx, `UPDATE cloud_meta.api_keys k SET roles = $1::jsonb
		WHERE k.roles IN ('[]'::jsonb, 'null'::jsonb) AND k.agent_id <> '' AND k.revoked_at IS NULL
		  AND EXISTS (SELECT 1 FROM cloud_meta.agent_drafts d
		              WHERE d.tenant_id = k.tenant_id AND d.key_id = k.id AND d.cards = '[]'::jsonb)`,
		`["`+RoleSetupReader+`"]`)
	if err != nil {
		return 0, fmt.Errorf("backfill setup_reader: %w", err)
	}
	return tag.RowsAffected(), nil
}

// APIKeyRecord is the persisted shape of an API key. The plaintext
// key is never on this struct — only the hash. Issued once at
// creation through GeneratedAPIKey.Plaintext, then forgotten.
type APIKeyRecord struct {
	ID      string
	Tenant  string
	Subject string
	Label   string
	Roles   []string
	// DomainID optionally binds the key to a single domain. Empty means tenant-wide (the default). When set, the
	// decide path treats it as an isolation boundary — a request that
	// names a different domain is refused, never redirected.
	DomainID string
	// AgentID optionally binds the key to one agent: the holder of a
	// card. Empty means unbound. When set, the decide path refuses a
	// proposal from this key whose actor_id names anyone else, and an
	// action that requires a card accepts only a key bound to the
	// card's holder. An opaque identifier, independent of the
	// customer's identity provider.
	AgentID string
	// Environment optionally binds the key to one environment.
	// Empty means any. When set, a request from this key is decided in
	// that environment, so it chooses which environment-scoped cards
	// apply no more than a domain-scoped key chooses its domain; a
	// request naming a different environment is refused.
	Environment string
	KeyHash     [APIKeyHashSize]byte
	CreatedAt   time.Time
	LastUsedAt  *time.Time
	RevokedAt   *time.Time
}

// IsRevoked reports whether the key has been revoked.
func (r APIKeyRecord) IsRevoked() bool { return r.RevokedAt != nil }

// IsDomainScoped reports whether the key is bound to a single domain
// A tenant-wide key returns false.
func (r APIKeyRecord) IsDomainScoped() bool { return r.DomainID != "" }

// IsAgentBound reports whether the key is bound to one agent.
func (r APIKeyRecord) IsAgentBound() bool { return r.AgentID != "" }

// IsEnvironmentBound reports whether the key is bound to one environment.
func (r APIKeyRecord) IsEnvironmentBound() bool { return r.Environment != "" }

// HexHash returns the hash in lowercase hex, the format used in
// storage and logs.
func (r APIKeyRecord) HexHash() string { return hex.EncodeToString(r.KeyHash[:]) }

// Errors surfaced by APIKeyStore implementations. Distinct from
// ErrAPIKeyMalformed (parse-side) so callers can branch on intent.
var (
	ErrAPIKeyNotFound  = errors.New("auth: api key not found")
	ErrAPIKeyDuplicate = errors.New("auth: api key already exists")
	ErrAPIKeyRevoked   = errors.New("auth: api key has been revoked")
)

// APIKeyStore is the persistence boundary for API keys. The
// middleware uses GetByHash + Touch on every API-key request; the
// CRUD endpoints use Insert / Revoke / List for the dashboard
// surface.
type APIKeyStore interface {
	// Insert persists a fresh record. Returns ErrAPIKeyDuplicate
	// when the hash collides (vanishingly unlikely with 160 bits
	// of entropy, but the check costs nothing).
	Insert(ctx context.Context, r APIKeyRecord) error

	// GetByHash looks up an active record by its hash. Returns
	// ErrAPIKeyNotFound when no row exists for the hash; returns
	// ErrAPIKeyRevoked when the row is found but revoked.
	GetByHash(ctx context.Context, hash [APIKeyHashSize]byte) (APIKeyRecord, error)

	// Touch records the most recent use of a key. Best-effort: the
	// middleware should not fail a request if the touch fails.
	Touch(ctx context.Context, id string, at time.Time) error

	// Revoke soft-deletes a key by stamping revoked_at. Returns
	// ErrAPIKeyNotFound when no row exists for the id; idempotent
	// otherwise (revoking an already-revoked key returns nil).
	Revoke(ctx context.Context, id string, at time.Time) error

	// ListActive returns the active (non-revoked) keys for a
	// tenant. Used by the dashboard. Order is by creation time,
	// newest first.
	ListActive(ctx context.Context, tenant string) ([]APIKeyRecord, error)

	// RevokeAllForTenant revokes every active key belonging to the
	// given tenant. Used by the tenant soft-delete primitive (RFC
	// 020) to ensure a deletion-pending tenant's keys cannot
	// authenticate new requests. Idempotent: already-revoked keys
	// are skipped silently.
	RevokeAllForTenant(ctx context.Context, tenant string, at time.Time) error

	// AddRoles adds roles to an active key of the tenant and returns
	// the updated record. Roles the key already has
	// are left as they are, and existing roles keep their order. It
	// only ever adds: authority is removed by revoking the key. Returns
	// ErrAPIKeyNotFound when the tenant has no active key with that id.
	AddRoles(ctx context.Context, tenant, id string, roles []string) (APIKeyRecord, error)
}

// MemoryAPIKeyStore is the in-memory implementation suited to
// tests and dev. Concurrent-safe.
type MemoryAPIKeyStore struct {
	mu     sync.RWMutex
	byID   map[string]APIKeyRecord
	byHash map[[APIKeyHashSize]byte]string // hash -> id index
}

// NewMemoryAPIKeyStore returns an empty MemoryAPIKeyStore.
func NewMemoryAPIKeyStore() *MemoryAPIKeyStore {
	return &MemoryAPIKeyStore{
		byID:   make(map[string]APIKeyRecord),
		byHash: make(map[[APIKeyHashSize]byte]string),
	}
}

// Insert implements APIKeyStore.
func (s *MemoryAPIKeyStore) Insert(_ context.Context, r APIKeyRecord) error {
	if err := validateRecordForInsert(r); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, dup := s.byID[r.ID]; dup {
		return fmt.Errorf("%w: id %q", ErrAPIKeyDuplicate, r.ID)
	}
	if _, dup := s.byHash[r.KeyHash]; dup {
		return fmt.Errorf("%w: hash collision", ErrAPIKeyDuplicate)
	}
	r.Roles = append([]string(nil), r.Roles...)
	s.byID[r.ID] = r
	s.byHash[r.KeyHash] = r.ID
	return nil
}

// GetByHash implements APIKeyStore.
func (s *MemoryAPIKeyStore) GetByHash(_ context.Context, hash [APIKeyHashSize]byte) (APIKeyRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	id, ok := s.byHash[hash]
	if !ok {
		return APIKeyRecord{}, ErrAPIKeyNotFound
	}
	rec := s.byID[id]
	if rec.IsRevoked() {
		return rec, ErrAPIKeyRevoked
	}
	rec.Roles = append([]string(nil), rec.Roles...)
	return rec, nil
}

// Touch implements APIKeyStore.
func (s *MemoryAPIKeyStore) Touch(_ context.Context, id string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.byID[id]
	if !ok {
		return ErrAPIKeyNotFound
	}
	at = at.UTC()
	rec.LastUsedAt = &at
	s.byID[id] = rec
	return nil
}

// Revoke implements APIKeyStore.
func (s *MemoryAPIKeyStore) Revoke(_ context.Context, id string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.byID[id]
	if !ok {
		return ErrAPIKeyNotFound
	}
	if rec.RevokedAt != nil {
		return nil // idempotent
	}
	at = at.UTC()
	rec.RevokedAt = &at
	s.byID[id] = rec
	return nil
}

// AddRoles implements APIKeyStore.
func (s *MemoryAPIKeyStore) AddRoles(_ context.Context, tenant, id string, roles []string) (APIKeyRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.byID[id]
	if !ok || rec.Tenant != tenant || rec.IsRevoked() {
		return APIKeyRecord{}, ErrAPIKeyNotFound
	}
	rec.Roles = mergeRoles(rec.Roles, roles)
	s.byID[id] = rec
	return rec, nil
}

// mergeRoles appends the roles current lacks, keeping current's order.
func mergeRoles(current, add []string) []string {
	out := append([]string(nil), current...)
	for _, r := range add {
		if !slices.Contains(out, r) {
			out = append(out, r)
		}
	}
	return out
}

// ListActive implements APIKeyStore.
func (s *MemoryAPIKeyStore) ListActive(_ context.Context, tenant string) ([]APIKeyRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]APIKeyRecord, 0, len(s.byID))
	for _, rec := range s.byID {
		if rec.Tenant != tenant || rec.IsRevoked() {
			continue
		}
		clone := rec
		clone.Roles = append([]string(nil), rec.Roles...)
		out = append(out, clone)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

// RevokeAllForTenant implements APIKeyStore.
func (s *MemoryAPIKeyStore) RevokeAllForTenant(_ context.Context, tenant string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	at = at.UTC()
	for id, rec := range s.byID {
		if rec.Tenant != tenant || rec.RevokedAt != nil {
			continue
		}
		rec.RevokedAt = &at
		s.byID[id] = rec
	}
	return nil
}

// PostgresAPIKeyStore writes to cloud_meta.api_keys via the given
// pool. All queries are schema-qualified so the pool's search_path
// does not need to point at cloud_meta.
type PostgresAPIKeyStore struct {
	pool *pgxpool.Pool
}

// NewPostgresAPIKeyStore wraps the given pool. The caller is
// responsible for the pool's lifetime and for ApplyAPIKeysSchema
// having been called.
func NewPostgresAPIKeyStore(pool *pgxpool.Pool) *PostgresAPIKeyStore {
	return &PostgresAPIKeyStore{pool: pool}
}

const sqlInsertAPIKey = `
INSERT INTO cloud_meta.api_keys (id, tenant_id, subject, label, roles, domain_id, agent_id, environment, key_hash, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
`

const sqlSelectAPIKeyByHash = `
SELECT id, tenant_id, subject, label, roles, domain_id, agent_id, environment, key_hash, created_at, last_used_at, revoked_at
FROM cloud_meta.api_keys
WHERE key_hash = $1
`

const sqlTouchAPIKey = `
UPDATE cloud_meta.api_keys SET last_used_at = $2 WHERE id = $1
`

const sqlRevokeAPIKey = `
UPDATE cloud_meta.api_keys SET revoked_at = $2
WHERE id = $1 AND revoked_at IS NULL
`

const sqlListActiveAPIKeys = `
SELECT id, tenant_id, subject, label, roles, domain_id, agent_id, environment, key_hash, created_at, last_used_at, revoked_at
FROM cloud_meta.api_keys
WHERE tenant_id = $1 AND revoked_at IS NULL
ORDER BY created_at DESC
`

// Insert implements APIKeyStore.
func (s *PostgresAPIKeyStore) Insert(ctx context.Context, r APIKeyRecord) error {
	if err := validateRecordForInsert(r); err != nil {
		return err
	}
	rolesJSON, err := json.Marshal(r.Roles)
	if err != nil {
		return fmt.Errorf("marshal roles: %w", err)
	}
	if _, err := s.pool.Exec(ctx, sqlInsertAPIKey,
		r.ID, r.Tenant, r.Subject, r.Label, rolesJSON, r.DomainID, r.AgentID, r.Environment, r.HexHash(), r.CreatedAt,
	); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" { // unique_violation
			return fmt.Errorf("%w: id=%q hash=%s", ErrAPIKeyDuplicate, r.ID, r.HexHash())
		}
		return fmt.Errorf("insert api key: %w", err)
	}
	return nil
}

// GetByHash implements APIKeyStore.
func (s *PostgresAPIKeyStore) GetByHash(ctx context.Context, hash [APIKeyHashSize]byte) (APIKeyRecord, error) {
	hex := hex.EncodeToString(hash[:])
	row := s.pool.QueryRow(ctx, sqlSelectAPIKeyByHash, hex)
	rec, err := scanAPIKey(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return APIKeyRecord{}, ErrAPIKeyNotFound
		}
		return APIKeyRecord{}, err
	}
	if rec.IsRevoked() {
		return rec, ErrAPIKeyRevoked
	}
	return rec, nil
}

// Touch implements APIKeyStore.
func (s *PostgresAPIKeyStore) Touch(ctx context.Context, id string, at time.Time) error {
	tag, err := s.pool.Exec(ctx, sqlTouchAPIKey, id, at.UTC())
	if err != nil {
		return fmt.Errorf("touch api key: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrAPIKeyNotFound
	}
	return nil
}

// Revoke implements APIKeyStore.
func (s *PostgresAPIKeyStore) Revoke(ctx context.Context, id string, at time.Time) error {
	tag, err := s.pool.Exec(ctx, sqlRevokeAPIKey, id, at.UTC())
	if err != nil {
		return fmt.Errorf("revoke api key: %w", err)
	}
	if tag.RowsAffected() == 0 {
		// Either the row doesn't exist, or it's already revoked.
		// Distinguish by re-reading.
		var exists bool
		if err := s.pool.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM cloud_meta.api_keys WHERE id = $1)", id).Scan(&exists); err != nil {
			return fmt.Errorf("check api key existence: %w", err)
		}
		if !exists {
			return ErrAPIKeyNotFound
		}
		// Already revoked — idempotent.
	}
	return nil
}

// sqlAddAPIKeyRoles appends, in one statement, the requested roles the
// key does not already hold. Done in SQL rather than read-merge-write so
// two grants racing on one key cannot drop either role.
const sqlAddAPIKeyRoles = `
UPDATE cloud_meta.api_keys k
SET roles = k.roles || COALESCE(
	(SELECT jsonb_agg(r ORDER BY ord)
	   FROM jsonb_array_elements_text($3::jsonb) WITH ORDINALITY AS a(r, ord)
	  WHERE NOT (k.roles ? r)),
	'[]'::jsonb)
WHERE k.id = $1 AND k.tenant_id = $2 AND k.revoked_at IS NULL
RETURNING id, tenant_id, subject, label, roles, domain_id, agent_id, environment, key_hash, created_at, last_used_at, revoked_at
`

// AddRoles implements APIKeyStore.
func (s *PostgresAPIKeyStore) AddRoles(ctx context.Context, tenant, id string, roles []string) (APIKeyRecord, error) {
	add, err := json.Marshal(mergeRoles(nil, roles))
	if err != nil {
		return APIKeyRecord{}, fmt.Errorf("marshal roles: %w", err)
	}
	rec, err := scanAPIKey(s.pool.QueryRow(ctx, sqlAddAPIKeyRoles, id, tenant, add))
	if errors.Is(err, pgx.ErrNoRows) {
		return APIKeyRecord{}, ErrAPIKeyNotFound
	}
	if err != nil {
		return APIKeyRecord{}, fmt.Errorf("add api key roles: %w", err)
	}
	return rec, nil
}

// ListActive implements APIKeyStore.
func (s *PostgresAPIKeyStore) ListActive(ctx context.Context, tenant string) ([]APIKeyRecord, error) {
	rows, err := s.pool.Query(ctx, sqlListActiveAPIKeys, tenant)
	if err != nil {
		return nil, fmt.Errorf("list api keys: %w", err)
	}
	defer rows.Close()
	out := make([]APIKeyRecord, 0)
	for rows.Next() {
		rec, err := scanAPIKey(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

const sqlRevokeAllForTenant = `
UPDATE cloud_meta.api_keys
SET revoked_at = $2
WHERE tenant_id = $1 AND revoked_at IS NULL
`

// RevokeAllForTenant implements APIKeyStore.
func (s *PostgresAPIKeyStore) RevokeAllForTenant(ctx context.Context, tenant string, at time.Time) error {
	if _, err := s.pool.Exec(ctx, sqlRevokeAllForTenant, tenant, at.UTC()); err != nil {
		return fmt.Errorf("revoke all keys for tenant: %w", err)
	}
	return nil
}

// scanAPIKey reads one row into an APIKeyRecord. Works for both
// pgx.Row and pgx.Rows because both expose Scan with the same
// signature.
type apiKeyRowScanner interface {
	Scan(dest ...any) error
}

func scanAPIKey(row apiKeyRowScanner) (APIKeyRecord, error) {
	var (
		rec      APIKeyRecord
		rolesRaw []byte
		hashHex  string
		lastUsed *time.Time
		revoked  *time.Time
	)
	if err := row.Scan(&rec.ID, &rec.Tenant, &rec.Subject, &rec.Label, &rolesRaw, &rec.DomainID, &rec.AgentID, &rec.Environment, &hashHex, &rec.CreatedAt, &lastUsed, &revoked); err != nil {
		return APIKeyRecord{}, err
	}
	if len(rolesRaw) > 0 {
		if err := json.Unmarshal(rolesRaw, &rec.Roles); err != nil {
			return APIKeyRecord{}, fmt.Errorf("unmarshal roles: %w", err)
		}
	}
	hashBytes, err := hex.DecodeString(strings.TrimSpace(hashHex))
	if err != nil {
		return APIKeyRecord{}, fmt.Errorf("decode key_hash: %w", err)
	}
	if len(hashBytes) != APIKeyHashSize {
		return APIKeyRecord{}, fmt.Errorf("key_hash length: got %d, want %d", len(hashBytes), APIKeyHashSize)
	}
	copy(rec.KeyHash[:], hashBytes)
	rec.LastUsedAt = lastUsed
	rec.RevokedAt = revoked
	return rec, nil
}

func validateRecordForInsert(r APIKeyRecord) error {
	switch {
	case r.ID == "":
		return errors.New("auth: api key record missing ID")
	case r.Tenant == "":
		return errors.New("auth: api key record missing Tenant")
	case r.Subject == "":
		return errors.New("auth: api key record missing Subject")
	case r.CreatedAt.IsZero():
		return errors.New("auth: api key record missing CreatedAt")
	case r.KeyHash == [APIKeyHashSize]byte{}:
		return errors.New("auth: api key record has zero KeyHash")
	}
	return nil
}
