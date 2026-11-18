package alert

import (
	"context"
	_ "embed"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kiff/cloud/api/internal/pgschema"
)

//go:embed sql/alert_deliveries.sql
var alertDeliveriesSQL string

// AlertDeliveriesSQL returns the DDL that creates
// cloud_meta.alert_deliveries. Idempotent. Call at startup
// after tenant.ApplyCloudMeta (the FK requires cloud_meta.tenants).
func AlertDeliveriesSQL() string { return alertDeliveriesSQL }

// ApplyDeliveriesSchema runs the alert_deliveries DDL. Idempotent.
func ApplyDeliveriesSchema(ctx context.Context, pool *pgxpool.Pool) error {
	if err := pgschema.Apply(ctx, pool, "alert/alert_deliveries", alertDeliveriesSQL); err != nil {
		return fmt.Errorf("apply alert_deliveries schema: %w", err)
	}
	return nil
}

// PostgresDeliveryStore persists alert delivery attempts in
// cloud_meta.alert_deliveries.
type PostgresDeliveryStore struct {
	pool *pgxpool.Pool
}

// NewPostgresDeliveryStore wraps the cloud pool.
func NewPostgresDeliveryStore(pool *pgxpool.Pool) *PostgresDeliveryStore {
	return &PostgresDeliveryStore{pool: pool}
}

const sqlRecordDelivery = `
INSERT INTO cloud_meta.alert_deliveries
    (id, tenant_id, proposal_id, target_id, target_type, action, outcome, status, detail, attempted_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
`

const sqlListDeliveries = `
SELECT id, tenant_id, proposal_id, target_id, target_type, action, outcome, status, detail, attempted_at
FROM cloud_meta.alert_deliveries
WHERE tenant_id = $1 AND proposal_id = $2
ORDER BY attempted_at DESC
`

// RecordDelivery implements DeliveryStore.
func (p *PostgresDeliveryStore) RecordDelivery(ctx context.Context, d Delivery) error {
	if _, err := p.pool.Exec(ctx, sqlRecordDelivery,
		d.ID, d.TenantID, d.ProposalID, d.TargetID, d.TargetType,
		d.Action, d.Outcome, d.Status, d.Detail, d.AttemptedAt,
	); err != nil {
		return fmt.Errorf("record alert delivery: %w", err)
	}
	return nil
}

// ListDeliveries implements DeliveryStore.
func (p *PostgresDeliveryStore) ListDeliveries(ctx context.Context, tenantID, proposalID string) ([]Delivery, error) {
	rows, err := p.pool.Query(ctx, sqlListDeliveries, tenantID, proposalID)
	if err != nil {
		return nil, fmt.Errorf("list alert deliveries: %w", err)
	}
	defer rows.Close()
	out := make([]Delivery, 0)
	for rows.Next() {
		var d Delivery
		if err := rows.Scan(
			&d.ID, &d.TenantID, &d.ProposalID, &d.TargetID, &d.TargetType,
			&d.Action, &d.Outcome, &d.Status, &d.Detail, &d.AttemptedAt,
		); err != nil {
			return nil, fmt.Errorf("scan alert delivery: %w", err)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
