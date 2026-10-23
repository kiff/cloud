package domain

import (
	"context"
	_ "embed"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kiff/cloud/api/internal/tenant"
)

//go:embed sql/tenant_domain_drafts.sql
var tenantDomainDraftsSQL string

// TenantDomainDraftsSQL returns the DDL that creates the
// cloud_meta.tenant_domain_drafts table. Idempotent. Call at
// startup alongside tenant.ApplyCloudMeta and
// ApplyTenantDomainsSchema.
func TenantDomainDraftsSQL() string { return tenantDomainDraftsSQL }

// ApplyTenantDomainDraftsSchema runs the DDL against pool.
// Idempotent.
func ApplyTenantDomainDraftsSchema(ctx context.Context, pool *pgxpool.Pool) error {
	if _, err := pool.Exec(ctx, tenantDomainDraftsSQL); err != nil {
		return fmt.Errorf("apply tenant_domain_drafts schema: %w", err)
	}
	return nil
}

// PostgresDraftSource is the production DraftSource. It writes
// to cloud_meta.tenant_domain_drafts. The schema is documented
// in sql/tenant_domain_drafts.sql.
type PostgresDraftSource struct {
	pool *pgxpool.Pool
}

// NewPostgresDraftSource wraps the cloud pool. The pool is
// shared with tenant.Store and must have ApplyCloudMeta +
// ApplyTenantDomainDraftsSchema applied.
func NewPostgresDraftSource(pool *pgxpool.Pool) *PostgresDraftSource {
	return &PostgresDraftSource{pool: pool}
}

// Load implements DraftSource.
func (p *PostgresDraftSource) Load(ctx context.Context, tenantID string) (TenantDraft, error) {
	const stmt = `
		SELECT yaml, EXTRACT(EPOCH FROM updated_at)::BIGINT
		FROM ` + tenant.MetaSchema + `.tenant_domain_drafts
		WHERE tenant_id = $1
	`
	row := p.pool.QueryRow(ctx, stmt, tenantID)
	var (
		yamlBytes []byte
		updatedAt int64
	)
	if err := row.Scan(&yamlBytes, &updatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return TenantDraft{}, ErrDraftNotFound
		}
		return TenantDraft{}, fmt.Errorf("load tenant draft: %w", err)
	}
	return TenantDraft{
		TenantID:  tenantID,
		YAML:      yamlBytes,
		UpdatedAt: updatedAt,
	}, nil
}

// Save implements DraftSource. Upsert; last write wins.
func (p *PostgresDraftSource) Save(ctx context.Context, tenantID string, yaml []byte) error {
	const stmt = `
		INSERT INTO ` + tenant.MetaSchema + `.tenant_domain_drafts
			(tenant_id, yaml, updated_at)
		VALUES ($1, $2, NOW())
		ON CONFLICT (tenant_id) DO UPDATE
		SET yaml = EXCLUDED.yaml,
		    updated_at = NOW()
	`
	if _, err := p.pool.Exec(ctx, stmt, tenantID, yaml); err != nil {
		return fmt.Errorf("save tenant draft: %w", err)
	}
	return nil
}

// Delete implements DraftSource.
func (p *PostgresDraftSource) Delete(ctx context.Context, tenantID string) error {
	const stmt = `DELETE FROM ` + tenant.MetaSchema + `.tenant_domain_drafts WHERE tenant_id = $1`
	if _, err := p.pool.Exec(ctx, stmt, tenantID); err != nil {
		return fmt.Errorf("delete tenant draft: %w", err)
	}
	return nil
}
