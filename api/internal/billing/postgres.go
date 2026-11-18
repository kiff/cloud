package billing

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kiff/cloud/api/internal/pgschema"
)

//go:embed sql/billing.sql
var billingSQL string

//go:embed sql/usage_ledger.sql
var usageLedgerSQL string

// ApplySchema runs the billing DDL against pool. Idempotent. Call
// after tenant.ApplyCloudMeta — billing_customers and
// billing_usage_events reference cloud_meta.tenants.
func ApplySchema(ctx context.Context, pool *pgxpool.Pool) error {
	if err := pgschema.Apply(ctx, pool, "billing/billing", billingSQL); err != nil {
		return fmt.Errorf("apply billing schema: %w", err)
	}
	if err := pgschema.Apply(ctx, pool, "billing/usage_ledger", usageLedgerSQL); err != nil {
		return fmt.Errorf("apply billing usage-ledger schema: %w", err)
	}
	return nil
}

// PostgresStore persists billing idempotency + customer correlation
// in cloud_meta.
type PostgresStore struct {
	pool *pgxpool.Pool
}

// NewPostgresStore wraps the cloud pool.
func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore {
	return &PostgresStore{pool: pool}
}

// Seen implements Store: a read-only existence check.
func (p *PostgresStore) Seen(ctx context.Context, eventID string) (bool, error) {
	var exists bool
	err := p.pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM cloud_meta.billing_processed_events WHERE event_id = $1)`, eventID).
		Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("seen: %w", err)
	}
	return exists, nil
}

// MarkProcessed implements Store with an atomic insert: the primary
// key conflict on a duplicate delivery is detected via the affected
// row count, so two concurrent deliveries cannot both proceed.
func (p *PostgresStore) MarkProcessed(ctx context.Context, eventID string) (bool, error) {
	tag, err := p.pool.Exec(ctx,
		`INSERT INTO cloud_meta.billing_processed_events (event_id)
		 VALUES ($1) ON CONFLICT (event_id) DO NOTHING`, eventID)
	if err != nil {
		return false, fmt.Errorf("mark processed: %w", err)
	}
	// 0 rows affected => the row already existed => duplicate.
	return tag.RowsAffected() == 0, nil
}

// LinkCustomer implements Store. Upserts the customer->tenant link.
func (p *PostgresStore) LinkCustomer(ctx context.Context, customerID, tenantID string) error {
	_, err := p.pool.Exec(ctx,
		`INSERT INTO cloud_meta.billing_customers (customer_id, tenant_id)
		 VALUES ($1, $2)
		 ON CONFLICT (customer_id) DO UPDATE SET tenant_id = EXCLUDED.tenant_id, linked_at = now()`,
		customerID, tenantID)
	if err != nil {
		return fmt.Errorf("link customer: %w", err)
	}
	return nil
}

// TenantForCustomer implements Store. Returns ("", nil) when unmapped.
func (p *PostgresStore) TenantForCustomer(ctx context.Context, customerID string) (string, error) {
	var tenantID string
	err := p.pool.QueryRow(ctx,
		`SELECT tenant_id FROM cloud_meta.billing_customers WHERE customer_id = $1`, customerID).
		Scan(&tenantID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("tenant for customer: %w", err)
	}
	return tenantID, nil
}

// Record implements UsageLedger with an idempotent insert: a repeat
// operation id (reporter or caller retry) is a no-op via the primary
// key conflict, so a meter event is emitted exactly once per
// operation.
func (p *PostgresStore) Record(ctx context.Context, ev UsageEvent) (bool, error) {
	tag, err := p.pool.Exec(ctx,
		`INSERT INTO cloud_meta.billing_usage_events
		    (operation_id, tenant_id, outcome, occurred_at)
		 VALUES ($1, $2, $3, $4)
		 ON CONFLICT (operation_id) DO NOTHING`,
		ev.OperationID, ev.TenantID, ev.Outcome, ev.OccurredAt)
	if err != nil {
		return false, fmt.Errorf("record usage event: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// Unreported implements UsageLedgerDrainer: the oldest unreported
// billable operations, capped at limit (limit<=0 falls back to 1000
// so a runaway scan can't load an unbounded slice).
func (p *PostgresStore) Unreported(ctx context.Context, limit int) ([]UsageEvent, error) {
	if limit <= 0 {
		limit = 1000
	}
	rows, err := p.pool.Query(ctx,
		`SELECT operation_id, tenant_id, outcome, occurred_at
		   FROM cloud_meta.billing_usage_events
		  WHERE reported_at IS NULL
		  ORDER BY occurred_at ASC
		  LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("unreported usage events: %w", err)
	}
	defer rows.Close()
	out := make([]UsageEvent, 0)
	for rows.Next() {
		var ev UsageEvent
		if err := rows.Scan(&ev.OperationID, &ev.TenantID, &ev.Outcome, &ev.OccurredAt); err != nil {
			return nil, fmt.Errorf("scan usage event: %w", err)
		}
		out = append(out, ev)
	}
	return out, rows.Err()
}

// MarkReported implements UsageLedgerDrainer. Stamps reported_at only
// on rows that are still unreported, so a concurrent reporter can't
// move the timestamp twice.
func (p *PostgresStore) MarkReported(ctx context.Context, operationIDs []string, at time.Time) error {
	if len(operationIDs) == 0 {
		return nil
	}
	_, err := p.pool.Exec(ctx,
		`UPDATE cloud_meta.billing_usage_events
		    SET reported_at = $2
		  WHERE operation_id = ANY($1) AND reported_at IS NULL`,
		operationIDs, at)
	if err != nil {
		return fmt.Errorf("mark reported: %w", err)
	}
	return nil
}
