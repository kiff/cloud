package domain

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kiff/cloud/api/internal/tenant"
)

//go:embed sql/tenant_controls.sql
var tenantControlsSQL string

// TenantControlsSQL returns the DDL that creates the
// cloud_meta.tenant_controls table. Idempotent.
func TenantControlsSQL() string { return tenantControlsSQL }

// ApplyTenantControlsSchema runs the DDL against pool. Idempotent.
// Call at API startup after ApplyTenantDomainsSchema.
func ApplyTenantControlsSchema(ctx context.Context, pool *pgxpool.Pool) error {
	if _, err := pool.Exec(ctx, tenantControlsSQL); err != nil {
		return fmt.Errorf("apply tenant_controls schema: %w", err)
	}
	return nil
}

// PostgresControlStore is the production ControlStore.
type PostgresControlStore struct {
	pool *pgxpool.Pool
}

// NewPostgresControlStore wraps the cloud pool.
func NewPostgresControlStore(pool *pgxpool.Pool) *PostgresControlStore {
	return &PostgresControlStore{pool: pool}
}

// Create implements ControlStore. Runs in a transaction:
//  1. UPDATE any existing active control to "superseded".
//  2. INSERT the new control with status "active".
//
// Retries once on a unique-violation against the one-active-per-env
// index: two racing FIRST activations in an
// environment both supersede zero rows, take no lock, and both try to
// insert version=1. The index rejects the loser; the retry re-runs,
// now sees the winner's active row, supersedes it, and inserts the
// correct next version. Sequential activations never hit this path.
func (s *PostgresControlStore) Create(ctx context.Context, c Control) error {
	err := s.createOnce(ctx, c)
	if isUniqueViolation(err) {
		// Racing first-activation lost the one-active-per-env race;
		// re-run so it supersedes the winner and increments.
		err = s.createOnce(ctx, c)
	}
	return err
}

// isUniqueViolation reports whether err is a Postgres unique-constraint
// violation (SQLSTATE 23505).
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func (s *PostgresControlStore) createOnce(ctx context.Context, c Control) error {
	actionsJSON, err := json.Marshal(c.Actions)
	if err != nil {
		return fmt.Errorf("marshal actions: %w", err)
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Supersede the current active control for this tenant in the SAME
	// (domain, environment). A control's identity is per-(domain,
	// environment) (domain added for multi-domain), so
	// activating one domain never supersedes another domain's control,
	// and activating prod never supersedes an active staging control.
	c = c.WithIdentityDefaults()
	const supersede = `
		UPDATE ` + tenant.MetaSchema + `.tenant_controls
		SET status = 'superseded'
		WHERE tenant_id = $1 AND status = 'active' AND domain = $2 AND environment = $3
	`
	if _, err := tx.Exec(ctx, supersede, c.TenantID, c.Domain, c.Environment); err != nil {
		return fmt.Errorf("supersede: %w", err)
	}

	// Monotonic version per (tenant, domain, environment) lineage
	// (domain-scoped): one past the highest version
	// seen in this domain+environment (including the just-superseded
	// rows). Computed inside the tx so concurrent activations serialize
	// on the row locks the supersede took.
	var nextVersion int
	const versionQ = `
		SELECT COALESCE(MAX(version), 0) + 1
		FROM ` + tenant.MetaSchema + `.tenant_controls
		WHERE tenant_id = $1 AND domain = $2 AND environment = $3
	`
	if err := tx.QueryRow(ctx, versionQ, c.TenantID, c.Domain, c.Environment).Scan(&nextVersion); err != nil {
		return fmt.Errorf("compute version: %w", err)
	}

	const insert = `
		INSERT INTO ` + tenant.MetaSchema + `.tenant_controls
			(id, tenant_id, domain, entity, actions, environment, workflow, version, activated_at, activated_by, status, mode)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, 'active', $11)
	`
	activatedAt := c.ActivatedAt
	if activatedAt.IsZero() {
		activatedAt = time.Now().UTC()
	}
	if _, err := tx.Exec(ctx, insert,
		c.ID, c.TenantID, c.Domain, c.Entity,
		actionsJSON, c.Environment, c.Workflow, nextVersion, activatedAt, c.ActivatedBy, c.Mode,
	); err != nil {
		return fmt.Errorf("insert: %w", err)
	}

	return tx.Commit(ctx)
}

// SetMode implements ControlStore: flips the governance posture of one
// active control for a tenant. Only the active row is updated, so a
// superseded historical control keeps the mode it governed under.
func (s *PostgresControlStore) SetMode(ctx context.Context, tenantID, controlID, mode string) error {
	const stmt = `
		UPDATE ` + tenant.MetaSchema + `.tenant_controls
		SET mode = $3
		WHERE tenant_id = $1 AND id = $2 AND status = 'active'
	`
	tag, err := s.pool.Exec(ctx, stmt, tenantID, controlID, mode)
	if err != nil {
		return fmt.Errorf("set control mode: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrControlNotFound
	}
	return nil
}

// List implements ControlStore.
func (s *PostgresControlStore) List(ctx context.Context, tenantID string) ([]Control, error) {
	const query = `
		SELECT id, tenant_id, domain, entity, actions, environment, workflow, version, activated_at, activated_by, status, mode
		FROM ` + tenant.MetaSchema + `.tenant_controls
		WHERE tenant_id = $1
		ORDER BY activated_at DESC
	`
	rows, err := s.pool.Query(ctx, query, tenantID)
	if err != nil {
		return nil, fmt.Errorf("query controls: %w", err)
	}
	defer rows.Close()
	return scanControls(rows)
}

func scanControls(rows pgx.Rows) ([]Control, error) {
	var out []Control
	for rows.Next() {
		var (
			c           Control
			actionsJSON []byte
		)
		if err := rows.Scan(
			&c.ID, &c.TenantID, &c.Domain, &c.Entity,
			&actionsJSON, &c.Environment, &c.Workflow, &c.Version, &c.ActivatedAt, &c.ActivatedBy, &c.Status, &c.Mode,
		); err != nil {
			return nil, fmt.Errorf("scan control: %w", err)
		}
		if err := json.Unmarshal(actionsJSON, &c.Actions); err != nil {
			return nil, fmt.Errorf("unmarshal actions: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
