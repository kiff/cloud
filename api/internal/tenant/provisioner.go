package tenant

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	kiffpostgres "github.com/kiff/kiff/pkg/kiff/store/postgres"

	"github.com/kiff/cloud/api/internal/approvals"
	"github.com/kiff/cloud/api/internal/attention"
	"github.com/kiff/cloud/api/internal/events"
)

// Provisioner creates and tears down tenant-isolated Postgres
// schemas. The framework's `schema.sql` is applied
// inside the per-tenant schema so the tenant's events, decisions,
// audits, and approvals are physically isolated from other tenants.
//
// Provisioner is intentionally narrow. It does not own the cloud
// bookkeeping row (the Store does); it only owns the schema
// creation/deletion side. Provision and Deprovision are idempotent.
type Provisioner struct {
	pool  *pgxpool.Pool
	store Store
}

// NewProvisioner wraps a pool plus the cloud's tenant Store.
// Callers must have applied cloud_meta first via ApplyCloudMeta.
func NewProvisioner(pool *pgxpool.Pool, store Store) *Provisioner {
	return &Provisioner{pool: pool, store: store}
}

// Provision creates the tenant's schema if missing, applies the
// framework's `schema.sql` inside it, and stamps the tenant row as
// active. Idempotent: calling it twice is a no-op once active.
func (p *Provisioner) Provision(ctx context.Context, tenantID string) error {
	t, err := p.store.Get(ctx, tenantID)
	if err != nil {
		return fmt.Errorf("provision: %w", err)
	}
	if t.Status == StatusActive {
		return nil
	}

	schema := SchemaNameFor(tenantID)
	if !validSchemaName(schema) {
		return fmt.Errorf("%w: derived schema name failed validation: %s", ErrInvalidTenant, schema)
	}

	// Step 1: create the schema.
	if _, err := p.pool.Exec(ctx, "CREATE SCHEMA IF NOT EXISTS "+schema); err != nil {
		return fmt.Errorf("create schema %s: %w", schema, err)
	}

	// Step 2: apply the framework's schema.sql inside it. We
	// acquire a dedicated connection, set search_path locally, run
	// the DDL, then release. Using a transaction-bounded
	// search_path (SET LOCAL) means the path does not stick to
	// the connection after release.
	conn, err := p.pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire conn for schema apply: %w", err)
	}
	defer func() {
		_, _ = conn.Exec(context.Background(), "RESET search_path")
		conn.Release()
	}()
	tx, err := conn.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin schema-apply tx: %w", err)
	}
	if _, err := tx.Exec(ctx, "SET LOCAL search_path TO "+schema); err != nil {
		_ = tx.Rollback(ctx)
		return fmt.Errorf("set search_path: %w", err)
	}
	if _, err := tx.Exec(ctx, kiffpostgres.SchemaSQL()); err != nil {
		_ = tx.Rollback(ctx)
		return fmt.Errorf("apply framework schema in %s: %w", schema, err)
	}
	// Cloud extensions to the framework's tables, in the same
	// transaction so a tenant is never left with the framework schema
	// but not the cloud's additions. Domain-scoped approvals add the approval domain
	// dimension, which the resolver's domain-scoped approval store
	// requires — a tenant provisioned without it would fail on the
	// first approval write.
	if _, err := tx.Exec(ctx, approvals.SchemaSQL()); err != nil {
		_ = tx.Rollback(ctx)
		return fmt.Errorf("apply cloud approval schema in %s: %w", schema, err)
	}
	// Each event's domain, so runtimes can rebuild entity state
	// from their own domain's history.
	if _, err := tx.Exec(ctx, events.SchemaSQL()); err != nil {
		_ = tx.Rollback(ctx)
		return fmt.Errorf("apply cloud event schema in %s: %w", schema, err)
	}
	if _, err := tx.Exec(ctx, attention.SchemaSQL()); err != nil {
		_ = tx.Rollback(ctx)
		return fmt.Errorf("apply attention schema: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit schema apply: %w", err)
	}

	// Step 3: stamp the tenant row.
	now := time.Now().UTC()
	if err := p.store.SetStatus(ctx, tenantID, StatusActive, &now); err != nil {
		return fmt.Errorf("activate tenant: %w", err)
	}
	return nil
}

// Deprovision drops the tenant's schema and marks the tenant row
// deleted. Tenant data is not recoverable after this. Idempotent
// for the schema drop; if the row is already deleted, the call
// returns nil.
func (p *Provisioner) Deprovision(ctx context.Context, tenantID string) error {
	t, err := p.store.Get(ctx, tenantID)
	if err != nil {
		// If the row is gone already, the schema may still exist.
		// Try to drop the schema anyway (best effort) using the
		// canonical name so a half-deleted state can be cleaned.
		if err == ErrNotFound {
			schema := SchemaNameFor(tenantID)
			if validSchemaName(schema) {
				_, _ = p.pool.Exec(ctx, "DROP SCHEMA IF EXISTS "+schema+" CASCADE")
			}
			if err := p.eraseCloudMeta(ctx, tenantID); err != nil {
				return err
			}
			return p.eraseGatewayRows(ctx, tenantID)
		}
		return err
	}

	schema := t.SchemaName()
	if !validSchemaName(schema) {
		return fmt.Errorf("%w: derived schema name failed validation: %s", ErrInvalidTenant, schema)
	}

	if _, err := p.pool.Exec(ctx, "DROP SCHEMA IF EXISTS "+schema+" CASCADE"); err != nil {
		return fmt.Errorf("drop schema %s: %w", schema, err)
	}
	// Before the row is marked deleted, so a failure leaves the tenant
	// visibly un-erased and a re-run finishes the job.
	if err := p.eraseCloudMeta(ctx, tenantID); err != nil {
		return err
	}
	if err := p.eraseGatewayRows(ctx, tenantID); err != nil {
		return err
	}
	if err := p.store.SetStatus(ctx, tenantID, StatusDeleted, nil); err != nil {
		return fmt.Errorf("mark tenant deleted: %w", err)
	}
	// Destroy the redaction salt so the /security claim holds:
	// "the salt is destroyed when a tenant's data is deleted."
	// Once nulled, historical on-chain hashes for this tenant are
	// permanently unreconstructable.
	//
	// The schema is already dropped at this point, so a failure here
	// leaves erasure incomplete (salt still on the row). Return the
	// error so the operator notices and can re-run; the audit row is
	// retained per design.
	if err := p.store.NullRedactionSalt(ctx, tenantID); err != nil {
		return fmt.Errorf("null redaction salt after schema drop: %w", err)
	}
	return p.reduceTenantRow(ctx, tenantID)
}

// gatewaySchema holds kiff-gateway's tables: tool connections
// with their encrypted credentials, and every tool call an agent made.
// They live outside the tenant's schema, so dropping that schema does
// not erase them.
const gatewaySchema = "gateway"

// eraseGatewayRows deletes the tenant's rows from every table in the
// gateway schema that has a tenant_id column. The rule is by column, not
// by a list of tables, so a table the gateway adds later is erased too;
// the gateway's tests require every table to carry tenant_id. A database
// without the gateway schema has nothing to erase. Idempotent.
func (p *Provisioner) eraseGatewayRows(ctx context.Context, tenantID string) error {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("erase gateway data: %w", err)
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `
		SELECT c.table_name FROM information_schema.columns c
		  JOIN information_schema.tables t ON t.table_schema = c.table_schema AND t.table_name = c.table_name
		 WHERE c.table_schema = $1 AND c.column_name = 'tenant_id' AND t.table_type = 'BASE TABLE'
		 ORDER BY c.table_name`, gatewaySchema)
	if err != nil {
		return fmt.Errorf("erase gateway data: list tables: %w", err)
	}
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return fmt.Errorf("erase gateway data: %w", err)
		}
		tables = append(tables, name)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("erase gateway data: %w", err)
	}
	for _, name := range tables {
		ident := pgx.Identifier{gatewaySchema, name}.Sanitize()
		if _, err := tx.Exec(ctx, "DELETE FROM "+ident+" WHERE tenant_id = $1", tenantID); err != nil {
			return fmt.Errorf("erase gateway data: %s: %w", ident, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("erase gateway data: %w", err)
	}
	return nil
}
