package appsignin

import (
	"context"
	_ "embed"
	"fmt"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kiff/cloud/api/internal/pgschema"
)

// DefaultTermsVersion is the tafs terms in force: the date their page says it
// was last updated. A change to the terms changes it (KIFF_APP_TERMS_VERSION),
// so an app that ticked older ones is asked again (terms_outdated).
const DefaultTermsVersion = "2026-11-14"

// Consent is one person agreeing to the terms as they signed in.
type Consent struct {
	Tenant       string
	Subject      string
	TermsVersion string
	Client       string
	DeviceID     string
	SessionID    string
	AcceptedAt   time.Time
}

// ConsentStore keeps consents.
type ConsentStore interface {
	Record(ctx context.Context, c Consent) error
}

//go:embed sql/app_terms_consents.sql
var consentsSQL string

// ApplyConsentsSchema runs the app_terms_consents DDL. Idempotent. Call at
// API startup after the tenants table exists.
func ApplyConsentsSchema(ctx context.Context, pool *pgxpool.Pool) error {
	if err := pgschema.Apply(ctx, pool, "appsignin/app_terms_consents", consentsSQL); err != nil {
		return fmt.Errorf("apply app_terms_consents schema: %w", err)
	}
	return nil
}

// PostgresConsents keeps consents in cloud_meta.app_terms_consents.
type PostgresConsents struct{ pool *pgxpool.Pool }

// NewPostgresConsents wraps pool; the caller ran ApplyConsentsSchema.
func NewPostgresConsents(pool *pgxpool.Pool) *PostgresConsents { return &PostgresConsents{pool: pool} }

// Record inserts c.
func (s *PostgresConsents) Record(ctx context.Context, c Consent) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO cloud_meta.app_terms_consents
		(tenant_id, subject, terms_version, client, device_id, session_id, accepted_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		c.Tenant, c.Subject, c.TermsVersion, c.Client, c.DeviceID, c.SessionID, c.AcceptedAt)
	if err != nil {
		return fmt.Errorf("appsignin: record consent: %w", err)
	}
	return nil
}

// MemoryConsents keeps consents in memory: local runs and tests.
type MemoryConsents struct {
	mu   sync.Mutex
	list []Consent
	// Fail, when set, is returned by Record (tests).
	Fail error
}

// Record appends c, or returns Fail.
func (m *MemoryConsents) Record(_ context.Context, c Consent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Fail != nil {
		return m.Fail
	}
	m.list = append(m.list, c)
	return nil
}

// All is every consent recorded, oldest first.
func (m *MemoryConsents) All() []Consent {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]Consent(nil), m.list...)
}
