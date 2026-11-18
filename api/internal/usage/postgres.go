package usage

import (
	"context"
	_ "embed"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kiff/cloud/api/internal/pgschema"
)

//go:embed sql/usage.sql
var usageSQL string

// UsageSQL returns the DDL that creates cloud_meta.tenant_usage.
// Idempotent. Call once at startup alongside tenant.ApplyCloudMeta.
func UsageSQL() string { return usageSQL }

// ApplySchema runs the DDL against pool and migrates an existing
// tenant_usage table forward to include the domain_id dimension.
// Idempotent.
func ApplySchema(ctx context.Context, pool *pgxpool.Pool) error {
	if err := pgschema.Apply(ctx, pool, "usage/usage", usageSQL); err != nil {
		return fmt.Errorf("apply tenant_usage schema: %w", err)
	}
	if err := migrateTenantUsageDomain(ctx, pool); err != nil {
		return fmt.Errorf("migrate tenant_usage domain dimension: %w", err)
	}
	return nil
}

// migrateTenantUsageDomain adds the domain_id column and folds it
// into the primary key on an existing tenant_usage table. Idempotent:
// a no-op once domain_id is already part of the key (or on a fresh
// table created composite). Pre-existing rows default to domain_id ”
// (tenant-wide), which is the honest value for usage recorded before
// the dimension existed.
func migrateTenantUsageDomain(ctx context.Context, pool *pgxpool.Pool) error {
	const table = "cloud_meta.tenant_usage"
	if _, err := pool.Exec(ctx, `ALTER TABLE `+table+` ADD COLUMN IF NOT EXISTS domain_id TEXT NOT NULL DEFAULT ''`); err != nil {
		return fmt.Errorf("add domain_id column: %w", err)
	}
	// Swap the primary key to include domain_id when it isn't already
	// a 5-column key.
	var pkCols int
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*)
		FROM pg_index i
		JOIN pg_attribute a ON a.attrelid = i.indrelid AND a.attnum = ANY (i.indkey)
		WHERE i.indrelid = ($1)::regclass AND i.indisprimary
	`, table).Scan(&pkCols); err != nil {
		return fmt.Errorf("inspect primary key: %w", err)
	}
	if pkCols == 4 {
		if _, err := pool.Exec(ctx, `ALTER TABLE `+table+` DROP CONSTRAINT IF EXISTS tenant_usage_pkey`); err != nil {
			return fmt.Errorf("drop old pk: %w", err)
		}
		if _, err := pool.Exec(ctx, `ALTER TABLE `+table+` ADD CONSTRAINT tenant_usage_pkey PRIMARY KEY (tenant_id, counter_name, entity_type, domain_id, hour_bucket)`); err != nil {
			return fmt.Errorf("add composite pk: %w", err)
		}
	}
	return nil
}

// PostgresStore is the production Store. Backed by
// cloud_meta.tenant_usage. Flush uses an upsert that increments the
// existing row's count, so partial flushes (or duplicate flushes
// from a retry) are correct.
type PostgresStore struct {
	pool *pgxpool.Pool
}

// NewPostgresStore wraps the cloud pool. The pool is shared with
// the tenant Store and other cloud_meta consumers; it must have the
// usage schema applied.
func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore {
	return &PostgresStore{pool: pool}
}

// Flush implements Store. Each counter becomes one upsert. For v0.1
// this is fine — flush sizes are bounded by the number of distinct
// (tenant, counter, entity, hour) keys touched in the flush
// interval, which stays small in production. If volume ever grows,
// switch to a single multi-row INSERT with VALUES (..), (..), ..
// CONFLICT clause.
func (p *PostgresStore) Flush(ctx context.Context, counters []Counter) error {
	if len(counters) == 0 {
		return nil
	}

	const stmt = `
		INSERT INTO cloud_meta.tenant_usage
			(tenant_id, counter_name, entity_type, domain_id, hour_bucket, count, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, NOW())
		ON CONFLICT (tenant_id, counter_name, entity_type, domain_id, hour_bucket)
		DO UPDATE SET
			count = cloud_meta.tenant_usage.count + EXCLUDED.count,
			updated_at = NOW()
	`

	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	for _, c := range counters {
		if c.Count == 0 {
			continue
		}
		if _, err := tx.Exec(ctx, stmt,
			c.TenantID, c.CounterName, c.EntityType, c.DomainID, c.HourBucket, int64(c.Count),
		); err != nil {
			return fmt.Errorf("upsert tenant_usage row: %w", err)
		}
	}
	return tx.Commit(ctx)
}

// Snapshot implements Store.
func (p *PostgresStore) Snapshot(ctx context.Context, filter Filter) ([]Counter, error) {
	args := []any{}
	stmt := `
		SELECT tenant_id, counter_name, entity_type, domain_id, hour_bucket, count
		FROM cloud_meta.tenant_usage
	`
	cond := []string{}
	if filter.TenantID != "" {
		args = append(args, filter.TenantID)
		cond = append(cond, fmt.Sprintf("tenant_id = $%d", len(args)))
	}
	if filter.CounterName != "" {
		args = append(args, filter.CounterName)
		cond = append(cond, fmt.Sprintf("counter_name = $%d", len(args)))
	}
	if filter.DomainID != "" {
		args = append(args, filter.DomainID)
		cond = append(cond, fmt.Sprintf("domain_id = $%d", len(args)))
	}
	if !filter.HourFrom.IsZero() {
		args = append(args, filter.HourFrom)
		cond = append(cond, fmt.Sprintf("hour_bucket >= $%d", len(args)))
	}
	if !filter.HourTo.IsZero() {
		args = append(args, filter.HourTo)
		cond = append(cond, fmt.Sprintf("hour_bucket <= $%d", len(args)))
	}
	if len(cond) > 0 {
		stmt += " WHERE " + joinAnd(cond)
	}
	stmt += " ORDER BY tenant_id, counter_name, entity_type, domain_id, hour_bucket"

	rows, err := p.pool.Query(ctx, stmt, args...)
	if err != nil {
		return nil, fmt.Errorf("query tenant_usage: %w", err)
	}
	defer rows.Close()

	var out []Counter
	for rows.Next() {
		var c Counter
		var count int64
		var hour time.Time
		if err := rows.Scan(&c.TenantID, &c.CounterName, &c.EntityType, &c.DomainID, &hour, &count); err != nil {
			return nil, fmt.Errorf("scan tenant_usage: %w", err)
		}
		c.HourBucket = hour
		c.Count = uint64(count)
		out = append(out, c)
	}
	return out, rows.Err()
}

func joinAnd(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += " AND "
		}
		out += p
	}
	return out
}
