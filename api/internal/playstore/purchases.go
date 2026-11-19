package playstore

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kiff/cloud/api/internal/pgschema"
)

//go:embed sql/playstore.sql
var schemaSQL string

// ApplySchema creates cloud_meta.google_purchases. Idempotent. Run after
// cloud_meta.tenants exists.
func ApplySchema(ctx context.Context, pool *pgxpool.Pool) error {
	if err := pgschema.Apply(ctx, pool, "playstore/playstore", schemaSQL); err != nil {
		return fmt.Errorf("apply playstore schema: %w", err)
	}
	return nil
}

// Purchase kinds.
const (
	KindSubscription = "subscription"
	KindProduct      = "product"
)

// Purchase is one Google Play purchase KIFF applied.
type Purchase struct {
	Token          string
	TenantID       string
	Product        string
	Kind           string
	OrderID        string
	State          string
	Test           bool
	AcknowledgedAt *time.Time
}

// Purchases records the Play purchases KIFF applied, once per token.
type Purchases interface {
	// Record keeps p, or updates the one with its token; created reports a
	// new one. AcknowledgedAt is kept once set.
	Record(ctx context.Context, p Purchase) (created bool, err error)
	// Acknowledged records that KIFF acknowledged the purchase at at.
	Acknowledged(ctx context.Context, token string, at time.Time) error
	// Get is the purchase with token; false when none.
	Get(ctx context.Context, token string) (Purchase, bool, error)
}

// PostgresPurchases keeps them in cloud_meta.google_purchases.
type PostgresPurchases struct{ pool *pgxpool.Pool }

// NewPostgresPurchases keeps them through pool; the caller applies the
// schema.
func NewPostgresPurchases(pool *pgxpool.Pool) *PostgresPurchases {
	return &PostgresPurchases{pool: pool}
}

// Record implements Purchases.
func (s *PostgresPurchases) Record(ctx context.Context, p Purchase) (bool, error) {
	var created bool
	err := s.pool.QueryRow(ctx, `INSERT INTO cloud_meta.google_purchases
		(purchase_token, tenant_id, product, kind, order_id, state, test, acknowledged_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (purchase_token) DO UPDATE SET
			order_id = CASE WHEN EXCLUDED.order_id = '' THEN google_purchases.order_id ELSE EXCLUDED.order_id END,
			state = EXCLUDED.state, test = EXCLUDED.test,
			acknowledged_at = COALESCE(google_purchases.acknowledged_at, EXCLUDED.acknowledged_at),
			updated_at = now()
		RETURNING (xmax = 0)`,
		p.Token, p.TenantID, p.Product, p.Kind, p.OrderID, p.State, p.Test, p.AcknowledgedAt).Scan(&created)
	if err != nil {
		return false, fmt.Errorf("playstore: record purchase: %w", err)
	}
	return created, nil
}

// Acknowledged implements Purchases.
func (s *PostgresPurchases) Acknowledged(ctx context.Context, token string, at time.Time) error {
	if _, err := s.pool.Exec(ctx, `UPDATE cloud_meta.google_purchases SET acknowledged_at = COALESCE(acknowledged_at, $2), updated_at = now()
		WHERE purchase_token = $1`, token, at); err != nil {
		return fmt.Errorf("playstore: acknowledged: %w", err)
	}
	return nil
}

// Get implements Purchases.
func (s *PostgresPurchases) Get(ctx context.Context, token string) (Purchase, bool, error) {
	var p Purchase
	err := s.pool.QueryRow(ctx, `SELECT purchase_token, tenant_id, product, kind, order_id, state, test, acknowledged_at
		FROM cloud_meta.google_purchases WHERE purchase_token = $1`, token).
		Scan(&p.Token, &p.TenantID, &p.Product, &p.Kind, &p.OrderID, &p.State, &p.Test, &p.AcknowledgedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Purchase{}, false, nil
	}
	if err != nil {
		return Purchase{}, false, fmt.Errorf("playstore: get purchase: %w", err)
	}
	return p, true, nil
}

// MemoryPurchases keeps them in process, for tests and an API without a
// database.
type MemoryPurchases struct {
	mu   sync.Mutex
	rows map[string]Purchase
}

// NewMemoryPurchases returns an empty store.
func NewMemoryPurchases() *MemoryPurchases { return &MemoryPurchases{rows: map[string]Purchase{}} }

// Record implements Purchases.
func (m *MemoryPurchases) Record(_ context.Context, p Purchase) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	old, ok := m.rows[p.Token]
	if ok {
		p.TenantID, p.Product, p.Kind = old.TenantID, old.Product, old.Kind
		if p.OrderID == "" {
			p.OrderID = old.OrderID
		}
		if old.AcknowledgedAt != nil {
			p.AcknowledgedAt = old.AcknowledgedAt
		}
	}
	m.rows[p.Token] = p
	return !ok, nil
}

// Acknowledged implements Purchases.
func (m *MemoryPurchases) Acknowledged(_ context.Context, token string, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if p, ok := m.rows[token]; ok && p.AcknowledgedAt == nil {
		p.AcknowledgedAt = &at
		m.rows[token] = p
	}
	return nil
}

// Get implements Purchases.
func (m *MemoryPurchases) Get(_ context.Context, token string) (Purchase, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.rows[token]
	return p, ok, nil
}

var (
	_ Purchases = (*PostgresPurchases)(nil)
	_ Purchases = (*MemoryPurchases)(nil)
)
