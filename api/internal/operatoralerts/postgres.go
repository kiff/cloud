package operatoralerts

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/oklog/ulid/v2"

	"github.com/kiff/cloud/api/internal/pgschema"
)

//go:embed sql/operator_alerts.sql
var operatorAlertsSQL string

// ApplySchema runs the operator_alerts DDL. Idempotent. Call after
// tenant.ApplyCloudMeta (FK references cloud_meta.tenants).
func ApplySchema(ctx context.Context, pool *pgxpool.Pool) error {
	if err := pgschema.Apply(ctx, pool, "operatoralerts/operator_alerts", operatorAlertsSQL); err != nil {
		return fmt.Errorf("apply operator_alerts schema: %w", err)
	}
	return nil
}

// PostgresStore writes to cloud_meta.operator_alerts.
type PostgresStore struct{ pool *pgxpool.Pool }

// NewPostgresStore wraps the cloud pool.
func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore { return &PostgresStore{pool: pool} }

const sqlUpsertAlert = `
INSERT INTO cloud_meta.operator_alerts
    (id, tenant_id, tenant_slug, kind, severity, detail, status, count, first_seen_at, last_seen_at)
VALUES ($1, $2, $3, $4, $5, $6, 'open', 1, $7, $7)
ON CONFLICT (tenant_id, kind) DO UPDATE SET
    count        = cloud_meta.operator_alerts.count + 1,
    last_seen_at = EXCLUDED.last_seen_at,
    detail       = EXCLUDED.detail,
    severity     = EXCLUDED.severity,
    tenant_slug  = EXCLUDED.tenant_slug,
    status       = CASE WHEN cloud_meta.operator_alerts.status = 'resolved'
                        THEN 'open' ELSE cloud_meta.operator_alerts.status END,
    acked_by     = CASE WHEN cloud_meta.operator_alerts.status = 'resolved'
                        THEN '' ELSE cloud_meta.operator_alerts.acked_by END,
    note         = CASE WHEN cloud_meta.operator_alerts.status = 'resolved'
                        THEN '' ELSE cloud_meta.operator_alerts.note END
RETURNING id, tenant_id, tenant_slug, kind, severity, detail, status, count, first_seen_at, last_seen_at, acked_by, note
`

// Upsert implements Store.
func (p *PostgresStore) Upsert(ctx context.Context, u Upsert, now time.Time) (Alert, error) {
	id := "opal_" + ulid.Make().String()
	row := p.pool.QueryRow(ctx, sqlUpsertAlert, id, u.TenantID, u.TenantSlug, u.Kind, u.Severity, u.Detail, now.UTC())
	return scanAlert(row)
}

const sqlListAlerts = `
SELECT id, tenant_id, tenant_slug, kind, severity, detail, status, count, first_seen_at, last_seen_at, acked_by, note
FROM cloud_meta.operator_alerts
%s
ORDER BY last_seen_at DESC
LIMIT $1
`

// List implements Store.
func (p *PostgresStore) List(ctx context.Context, openOnly bool, limit int) ([]Alert, error) {
	if limit <= 0 {
		limit = 200
	}
	where := ""
	if openOnly {
		where = "WHERE status <> 'resolved'"
	}
	rows, err := p.pool.Query(ctx, fmt.Sprintf(sqlListAlerts, where), limit)
	if err != nil {
		return nil, fmt.Errorf("list operator alerts: %w", err)
	}
	defer rows.Close()
	out := make([]Alert, 0, limit)
	for rows.Next() {
		a, err := scanAlert(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

const sqlSetAlertStatus = `
UPDATE cloud_meta.operator_alerts
SET status = $2, acked_by = $3, note = $4
WHERE id = $1
RETURNING id, tenant_id, tenant_slug, kind, severity, detail, status, count, first_seen_at, last_seen_at, acked_by, note
`

// SetStatus implements Store.
func (p *PostgresStore) SetStatus(ctx context.Context, id, status, by, note string) (Alert, error) {
	row := p.pool.QueryRow(ctx, sqlSetAlertStatus, id, status, by, note)
	a, err := scanAlert(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Alert{}, ErrNotFound
	}
	return a, err
}

type scanner interface {
	Scan(dest ...any) error
}

func scanAlert(s scanner) (Alert, error) {
	var a Alert
	if err := s.Scan(&a.ID, &a.TenantID, &a.TenantSlug, &a.Kind, &a.Severity, &a.Detail,
		&a.Status, &a.Count, &a.FirstSeen, &a.LastSeen, &a.AckedBy, &a.Note); err != nil {
		return Alert{}, err
	}
	return a, nil
}
