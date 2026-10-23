package tenant

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

//go:embed sql/subject_tenants.sql
var subjectTenantsSQL string

// SubjectTenantsSQL returns the DDL that creates
// cloud_meta.subject_tenants. Idempotent — exposed so integration
// tests can apply the schema directly.
func SubjectTenantsSQL() string { return subjectTenantsSQL }

// ApplySubjectTenantsSchema runs the subject-tenants DDL against
// the pool. Must be called after ApplyCloudMeta because of the
// foreign key on cloud_meta.tenants(id). Idempotent.
func ApplySubjectTenantsSchema(ctx context.Context, pool *pgxpool.Pool) error {
	if _, err := pool.Exec(ctx, subjectTenantsSQL); err != nil {
		return fmt.Errorf("apply subject_tenants schema: %w", err)
	}
	return nil
}

// ErrSubjectNotMapped is returned by SubjectStore.Get when no row
// exists for the given subject. The lazy resolver treats this as
// the "first sign-in, provision a tenant" trigger; tests can match
// on it explicitly.
var ErrSubjectNotMapped = errors.New("tenant: subject not mapped to any tenant")

// SubjectMapping is the (subject, tenantID, createdAt) row.
type SubjectMapping struct {
	Subject   string
	TenantID  string
	CreatedAt time.Time
}

// SubjectStore is the persistence boundary for the subject →
// tenant mapping. Two implementations:
//
//   - PostgresSubjectStore: production. Backed by
//     cloud_meta.subject_tenants. Foreign key on tenants(id)
//     means a tenant cannot be hard-deleted while subjects still
//     reference it.
//   - MemorySubjectStore: tests + in-memory smoke runs.
//
// Set is not idempotent at the row level — calling Set with a
// different tenant_id for the same subject returns
// ErrSubjectAlreadyMapped. The mapping is permanent for v0.1; if
// it ever becomes mutable, the resolver will need fresh thinking
// about cache invalidation.
type SubjectStore interface {
	// Get returns the mapping for subject. Returns
	// ErrSubjectNotMapped when no row exists.
	Get(ctx context.Context, subject string) (SubjectMapping, error)

	// Set creates a mapping. Returns ErrSubjectAlreadyMapped if
	// the subject already maps to a different tenant id; idempotent
	// when the mapping is identical (same tenant_id).
	Set(ctx context.Context, subject, tenantID string, now time.Time) error
}

// ErrSubjectAlreadyMapped is returned by SubjectStore.Set when the
// subject is already mapped to a *different* tenant. Set is
// idempotent for identical mappings.
var ErrSubjectAlreadyMapped = errors.New("tenant: subject already mapped to a different tenant")

// MemorySubjectStore is the in-memory SubjectStore. Concurrent-safe.
type MemorySubjectStore struct {
	mu   sync.RWMutex
	rows map[string]SubjectMapping
}

// NewMemorySubjectStore returns an empty in-memory store.
func NewMemorySubjectStore() *MemorySubjectStore {
	return &MemorySubjectStore{rows: make(map[string]SubjectMapping)}
}

// Get implements SubjectStore.
func (m *MemorySubjectStore) Get(_ context.Context, subject string) (SubjectMapping, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	row, ok := m.rows[subject]
	if !ok {
		return SubjectMapping{}, ErrSubjectNotMapped
	}
	return row, nil
}

// Set implements SubjectStore.
func (m *MemorySubjectStore) Set(_ context.Context, subject, tenantID string, now time.Time) error {
	if subject == "" {
		return errors.New("tenant: subject is required")
	}
	if tenantID == "" {
		return errors.New("tenant: tenantID is required")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if existing, ok := m.rows[subject]; ok {
		if existing.TenantID == tenantID {
			return nil // idempotent
		}
		return fmt.Errorf("%w: subject %q maps to %q", ErrSubjectAlreadyMapped, subject, existing.TenantID)
	}
	m.rows[subject] = SubjectMapping{
		Subject:   subject,
		TenantID:  tenantID,
		CreatedAt: now,
	}
	return nil
}

// SubjectForTenant returns the (oldest) subject mapped to tenantID —
// the workspace owner. Returns ErrSubjectNotMapped when none.
// Reverse of Get; used by the owner-email resolver
// (notifications). The mapping is one-subject-per-tenant in v0.1, so
// "oldest" is just "the owner."
func (m *MemorySubjectStore) SubjectForTenant(_ context.Context, tenantID string) (string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var best SubjectMapping
	found := false
	for _, row := range m.rows {
		if row.TenantID != tenantID {
			continue
		}
		if !found || row.CreatedAt.Before(best.CreatedAt) {
			best = row
			found = true
		}
	}
	if !found {
		return "", ErrSubjectNotMapped
	}
	return best.Subject, nil
}

// PostgresSubjectStore writes to cloud_meta.subject_tenants.
type PostgresSubjectStore struct {
	pool *pgxpool.Pool
}

// NewPostgresSubjectStore wraps the cloud pool. The caller is
// responsible for the pool's lifetime and for both ApplyCloudMeta
// and ApplySubjectTenantsSchema having been called.
func NewPostgresSubjectStore(pool *pgxpool.Pool) *PostgresSubjectStore {
	return &PostgresSubjectStore{pool: pool}
}

const sqlGetSubjectMapping = `
SELECT subject, tenant_id, created_at
FROM cloud_meta.subject_tenants
WHERE subject = $1
`

const sqlSubjectForTenant = `
SELECT subject
FROM cloud_meta.subject_tenants
WHERE tenant_id = $1
ORDER BY created_at ASC
LIMIT 1
`

// SubjectForTenant returns the (oldest) subject mapped to tenantID —
// the workspace owner. Returns ErrSubjectNotMapped when none. Reverse
// of Get; used by the owner-email resolver.
func (p *PostgresSubjectStore) SubjectForTenant(ctx context.Context, tenantID string) (string, error) {
	row := p.pool.QueryRow(ctx, sqlSubjectForTenant, tenantID)
	var subject string
	if err := row.Scan(&subject); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", ErrSubjectNotMapped
		}
		return "", fmt.Errorf("subject for tenant: %w", err)
	}
	return subject, nil
}

// Get implements SubjectStore.
func (p *PostgresSubjectStore) Get(ctx context.Context, subject string) (SubjectMapping, error) {
	row := p.pool.QueryRow(ctx, sqlGetSubjectMapping, subject)
	var m SubjectMapping
	if err := row.Scan(&m.Subject, &m.TenantID, &m.CreatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return SubjectMapping{}, ErrSubjectNotMapped
		}
		return SubjectMapping{}, fmt.Errorf("get subject mapping: %w", err)
	}
	return m, nil
}

const sqlInsertSubjectMapping = `
INSERT INTO cloud_meta.subject_tenants (subject, tenant_id, created_at)
VALUES ($1, $2, $3)
ON CONFLICT (subject) DO NOTHING
RETURNING tenant_id
`

// Set implements SubjectStore. Uses INSERT ... ON CONFLICT DO
// NOTHING so a concurrent first-sign-in race resolves to one
// winning row; the loser re-reads and returns ErrSubjectAlreadyMapped
// if the winner picked a different tenant id (rare but possible
// with two tabs).
func (p *PostgresSubjectStore) Set(ctx context.Context, subject, tenantID string, now time.Time) error {
	if subject == "" {
		return errors.New("tenant: subject is required")
	}
	if tenantID == "" {
		return errors.New("tenant: tenantID is required")
	}
	row := p.pool.QueryRow(ctx, sqlInsertSubjectMapping, subject, tenantID, now)
	var written string
	if err := row.Scan(&written); err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("insert subject mapping: %w", err)
		}
		// ON CONFLICT DO NOTHING fired. Re-read and check whether
		// the existing row matches.
		existing, getErr := p.Get(ctx, subject)
		if getErr != nil {
			return fmt.Errorf("re-read after insert conflict: %w", getErr)
		}
		if existing.TenantID == tenantID {
			return nil
		}
		return fmt.Errorf("%w: subject %q maps to %q", ErrSubjectAlreadyMapped, subject, existing.TenantID)
	}
	return nil
}
