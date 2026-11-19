package operatoraudit

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kiff/cloud/api/internal/pgschema"
)

//go:embed sql/operator_audit.sql
var operatorAuditSQL string

// ApplySchema runs the operator_audit DDL against pool. Idempotent.
// Call at API startup after tenant.ApplyCloudMeta (the FK references
// cloud_meta.tenants).
func ApplySchema(ctx context.Context, pool *pgxpool.Pool) error {
	if err := pgschema.Apply(ctx, pool, "operatoraudit/operator_audit", operatorAuditSQL); err != nil {
		return fmt.Errorf("apply operator_audit schema: %w", err)
	}
	return nil
}

// PostgresStore writes to cloud_meta.operator_audit. Append-only by
// contract — only Record (INSERT) and List (SELECT) exist.
type PostgresStore struct {
	pool *pgxpool.Pool
}

// NewPostgresStore wraps the cloud pool.
func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore {
	return &PostgresStore{pool: pool}
}

const sqlRecordAudit = `
INSERT INTO cloud_meta.operator_audit
    (id, operator_subject, action, target_tenant_id, reason, metadata, created_at)
VALUES ($1, $2, $3, $4, $5, $6::jsonb, $7)
`

// Record implements Store.
func (p *PostgresStore) Record(ctx context.Context, e Entry) error {
	if e.ID == "" {
		e.ID = newID()
	}
	if e.CreatedAt.IsZero() {
		e.CreatedAt = time.Now().UTC()
	}
	meta := e.Metadata
	if meta == nil {
		meta = map[string]any{}
	}
	metaJSON, err := json.Marshal(meta)
	if err != nil {
		return fmt.Errorf("marshal audit metadata: %w", err)
	}
	var tenantID any
	if e.TargetTenantID != "" {
		tenantID = e.TargetTenantID
	}
	if _, err := p.pool.Exec(ctx, sqlRecordAudit,
		e.ID, e.OperatorSubject, e.Action, tenantID, e.Reason, string(metaJSON), e.CreatedAt,
	); err != nil {
		return fmt.Errorf("record operator audit: %w", err)
	}
	return nil
}

const sqlListAudit = `
SELECT id, operator_subject, action, COALESCE(target_tenant_id, ''), reason, metadata, created_at
FROM cloud_meta.operator_audit
%s
ORDER BY created_at DESC
LIMIT $%d
`

// List implements Store.
func (p *PostgresStore) List(ctx context.Context, targetTenantID string, limit int) ([]Entry, error) {
	if limit <= 0 {
		limit = 100
	}
	var (
		where string
		args  []any
	)
	if targetTenantID != "" {
		where = "WHERE target_tenant_id = $1"
		args = append(args, targetTenantID)
	}
	stmt := fmt.Sprintf(sqlListAudit, where, len(args)+1)
	args = append(args, limit)

	rows, err := p.pool.Query(ctx, stmt, args...)
	if err != nil {
		return nil, fmt.Errorf("list operator audit: %w", err)
	}
	defer rows.Close()
	out := make([]Entry, 0, limit)
	for rows.Next() {
		var e Entry
		var metaJSON []byte
		if err := rows.Scan(&e.ID, &e.OperatorSubject, &e.Action, &e.TargetTenantID, &e.Reason, &metaJSON, &e.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan operator audit: %w", err)
		}
		if len(metaJSON) > 0 {
			_ = json.Unmarshal(metaJSON, &e.Metadata)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
