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

//go:embed sql/tenant_domains.sql
var tenantDomainsSQL string

// TenantDomainsSQL returns the DDL that creates the
// cloud_meta.tenant_domains table. Idempotent. Call once at startup
// alongside tenant.ApplyCloudMeta.
func TenantDomainsSQL() string { return tenantDomainsSQL }

// ApplyTenantDomainsSchema runs the DDL against pool and migrates an
// existing single-domain table forward to the composite
// (tenant_id, domain_id) shape. Idempotent: safe to
// call on every boot, a no-op once the table is already composite.
// Callers usually invoke this at API startup, after
// tenant.ApplyCloudMeta.
func ApplyTenantDomainsSchema(ctx context.Context, pool *pgxpool.Pool) error {
	if _, err := pool.Exec(ctx, tenantDomainsSQL); err != nil {
		return fmt.Errorf("apply tenant_domains schema: %w", err)
	}
	if err := migrateTenantDomainsComposite(ctx, pool); err != nil {
		return fmt.Errorf("migrate tenant_domains to composite key: %w", err)
	}
	return nil
}

// migrateTenantDomainsComposite brings an existing single-domain
// tenant_domains table (tenant_id PRIMARY KEY, no domain_id) forward
// to the composite (tenant_id, domain_id) shape, idempotently:
//
//  1. add the domain_id / slug / is_default / created_at columns
//     (no-op when already present, e.g. a fresh composite table);
//  2. backfill each legacy row's identity from its YAML — derived in
//     Go because SQL cannot read the domain name out of the bytes —
//     marking the sole pre-migration row the tenant's default;
//  3. create the one-default-per-tenant partial unique index the
//     default-domain upsert relies on;
//  4. swap the primary key to (tenant_id, domain_id) when it is still
//     the single tenant_id column.
//
// Each step is guarded so re-running on an already-migrated (or
// freshly composite) table does nothing.
func migrateTenantDomainsComposite(ctx context.Context, pool *pgxpool.Pool) error {
	const table = tenant.MetaSchema + ".tenant_domains"

	// 1. Columns. Comma-separated ADD COLUMN IF NOT EXISTS with
	// defaults so existing rows are filled (domain_id = '' marks a
	// row still needing backfill).
	if _, err := pool.Exec(ctx, `
		ALTER TABLE `+table+`
			ADD COLUMN IF NOT EXISTS domain_id  TEXT        NOT NULL DEFAULT '',
			ADD COLUMN IF NOT EXISTS slug       TEXT        NOT NULL DEFAULT '',
			ADD COLUMN IF NOT EXISTS is_default BOOLEAN     NOT NULL DEFAULT FALSE,
			ADD COLUMN IF NOT EXISTS created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
	`); err != nil {
		return fmt.Errorf("add columns: %w", err)
	}

	// 2. Backfill rows that still carry no identity. Pre-migration
	// the table was keyed by tenant_id alone, so there is at most one
	// such row per tenant; the derived identity is therefore unique
	// within the tenant and the sole row becomes the default.
	rows, err := pool.Query(ctx, `SELECT tenant_id, yaml FROM `+table+` WHERE domain_id = ''`)
	if err != nil {
		return fmt.Errorf("scan rows needing backfill: %w", err)
	}
	type legacyRow struct {
		tenantID string
		yaml     []byte
	}
	var pending []legacyRow
	for rows.Next() {
		var lr legacyRow
		if err := rows.Scan(&lr.tenantID, &lr.yaml); err != nil {
			rows.Close()
			return fmt.Errorf("scan legacy row: %w", err)
		}
		pending = append(pending, lr)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate legacy rows: %w", err)
	}
	for _, lr := range pending {
		domainID, slug := DeriveStorageIdentity(lr.yaml)
		if _, err := pool.Exec(ctx, `
			UPDATE `+table+`
			SET domain_id = $2, slug = $3, is_default = TRUE
			WHERE tenant_id = $1 AND domain_id = ''
		`, lr.tenantID, domainID, slug); err != nil {
			return fmt.Errorf("backfill identity for tenant %s: %w", lr.tenantID, err)
		}
	}

	// 3. One-default-per-tenant guard (the default-domain upsert's
	// ON CONFLICT arbiter). Created after the column + backfill so it
	// never indexes a half-migrated row.
	if _, err := pool.Exec(ctx, `
		CREATE UNIQUE INDEX IF NOT EXISTS tenant_domains_one_default
			ON `+table+` (tenant_id) WHERE is_default
	`); err != nil {
		return fmt.Errorf("create one-default index: %w", err)
	}

	// 4. Swap the primary key to composite when it is still the lone
	// tenant_id column.
	var pkCols int
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*)
		FROM pg_index i
		JOIN pg_attribute a ON a.attrelid = i.indrelid AND a.attnum = ANY (i.indkey)
		WHERE i.indrelid = ($1)::regclass AND i.indisprimary
	`, table).Scan(&pkCols); err != nil {
		return fmt.Errorf("inspect primary key: %w", err)
	}
	if pkCols == 1 {
		if _, err := pool.Exec(ctx, `ALTER TABLE `+table+` DROP CONSTRAINT IF EXISTS tenant_domains_pkey`); err != nil {
			return fmt.Errorf("drop single-column pk: %w", err)
		}
		if _, err := pool.Exec(ctx, `ALTER TABLE `+table+` ADD CONSTRAINT tenant_domains_pkey PRIMARY KEY (tenant_id, domain_id)`); err != nil {
			return fmt.Errorf("add composite pk: %w", err)
		}
	}
	return nil
}

// PostgresSource is the production Source. It writes to
// cloud_meta.tenant_domains and increments the version on every
// Save so callers can cache by (tenantID, version).
type PostgresSource struct {
	pool *pgxpool.Pool
}

// NewPostgresSource wraps the cloud pool. The pool is shared with
// the tenant Store and must have ApplyCloudMeta + ApplyTenantDomainsSchema
// applied.
func NewPostgresSource(pool *pgxpool.Pool) *PostgresSource {
	return &PostgresSource{pool: pool}
}

// Load implements Source. Returns the tenant's default domain (RFC
// 033 D7: its first/default row), so the singular back-compat path
// keeps resolving one domain while the table can hold many.
func (p *PostgresSource) Load(ctx context.Context, tenantID string) (TenantDomain, error) {
	const stmt = `
		SELECT yaml, version, domain_id, slug
		FROM ` + tenant.MetaSchema + `.tenant_domains
		WHERE tenant_id = $1
		ORDER BY is_default DESC, created_at ASC, domain_id ASC
		LIMIT 1
	`
	row := p.pool.QueryRow(ctx, stmt, tenantID)
	var (
		yamlBytes []byte
		version   int
		domainID  string
		slug      string
	)
	if err := row.Scan(&yamlBytes, &version, &domainID, &slug); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return TenantDomain{}, ErrDomainNotFound
		}
		return TenantDomain{}, fmt.Errorf("load tenant domain: %w", err)
	}
	// Defence for any row that predates the backfill: derive from the
	// YAML so a reader never sees an empty identity.
	if domainID == "" {
		domainID, slug = DeriveStorageIdentity(yamlBytes)
	}
	return TenantDomain{
		TenantID: tenantID,
		YAML:     yamlBytes,
		Version:  version,
		DomainID: domainID,
		Slug:     slug,
	}, nil
}

// LoadByDomain implements Source. An empty selector resolves the
// tenant's default domain (delegates to Load). A named selector may
// be the immutable domain_id used by internal records or the current
// public slug used by requests. Exact id wins if one row's slug ever
// collides with another row's id.
func (p *PostgresSource) LoadByDomain(ctx context.Context, tenantID, selector string) (TenantDomain, error) {
	if selector == "" {
		return p.Load(ctx, tenantID)
	}
	const stmt = `
		SELECT yaml, version, domain_id, slug
		FROM ` + tenant.MetaSchema + `.tenant_domains
		WHERE tenant_id = $1 AND (domain_id = $2 OR slug = $2)
		ORDER BY (domain_id = $2) DESC, created_at ASC
		LIMIT 1
	`
	row := p.pool.QueryRow(ctx, stmt, tenantID, selector)
	var (
		yamlBytes []byte
		version   int
		gotID     string
		slug      string
	)
	if err := row.Scan(&yamlBytes, &version, &gotID, &slug); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return TenantDomain{}, ErrDomainNotFound
		}
		return TenantDomain{}, fmt.Errorf("load tenant domain by selector: %w", err)
	}
	return TenantDomain{
		TenantID: tenantID,
		YAML:     yamlBytes,
		Version:  version,
		DomainID: gotID,
		Slug:     slug,
	}, nil
}

// TenantDomainUpsertSQL is the canonical upsert for a tenant's
// **default** domain. Exported so the draft handler's transactional
// activate path can run
// the same statement inside its own BEGIN/COMMIT alongside the draft
// delete, keeping the upsert logic in one place.
//
// It targets the one-default-per-tenant partial unique index
// (tenant_domains_one_default): the first activation inserts the
// tenant's default domain; a re-upload updates that row in place
// (bumping version, refreshing slug + yaml) and crucially keeps the
// existing immutable domain_id rather than re-deriving it — so a
// rename changes the slug, never the identity. Creating
// a *non*-default domain is a different write (a later slice) and does
// not go through this statement.
//
// Parameters: $1 = tenant_id, $2 = domain_id, $3 = slug, $4 = yaml.
// Callers derive $2/$3 from the yaml via domain.DeriveStorageIdentity.
const TenantDomainUpsertSQL = `
	INSERT INTO ` + tenant.MetaSchema + `.tenant_domains AS td
		(tenant_id, domain_id, slug, yaml, version, is_default, created_at, updated_at)
	VALUES ($1, $2, $3, $4, 1, TRUE, NOW(), NOW())
	ON CONFLICT (tenant_id) WHERE is_default DO UPDATE
	SET yaml = EXCLUDED.yaml,
	    version = td.version + 1,
	    slug = EXCLUDED.slug,
	    updated_at = NOW()
`

// Save implements Source. Upserts the tenant's default domain: the
// version increments atomically, the slug follows the current YAML,
// and the immutable domain_id is preserved across re-uploads.
func (p *PostgresSource) Save(ctx context.Context, tenantID string, yaml []byte) error {
	domainID, slug := DeriveStorageIdentity(yaml)
	if _, err := p.pool.Exec(ctx, TenantDomainUpsertSQL, tenantID, domainID, slug, yaml); err != nil {
		return fmt.Errorf("save tenant domain: %w", err)
	}
	return nil
}

// List implements Source. Returns all of the tenant's domains,
// default first, then oldest-created, then domain_id — the same
// ordering Load uses to pick the single default, extended to the
// full set. A tenant with no rows gets an empty slice (not an
// error), matching the list-shaped read projection's contract.
func (p *PostgresSource) List(ctx context.Context, tenantID string) ([]TenantDomain, error) {
	const stmt = `
		SELECT yaml, version, domain_id, slug
		FROM ` + tenant.MetaSchema + `.tenant_domains
		WHERE tenant_id = $1
		ORDER BY is_default DESC, created_at ASC, domain_id ASC
	`
	rows, err := p.pool.Query(ctx, stmt, tenantID)
	if err != nil {
		return nil, fmt.Errorf("list tenant domains: %w", err)
	}
	defer rows.Close()

	out := []TenantDomain{}
	for rows.Next() {
		var (
			yamlBytes []byte
			version   int
			domainID  string
			slug      string
		)
		if err := rows.Scan(&yamlBytes, &version, &domainID, &slug); err != nil {
			return nil, fmt.Errorf("scan tenant domain: %w", err)
		}
		// Defence for any row that predates the backfill: derive from
		// the YAML so a reader never sees an empty identity.
		if domainID == "" {
			domainID, slug = DeriveStorageIdentity(yamlBytes)
		}
		out = append(out, TenantDomain{
			TenantID: tenantID,
			YAML:     yamlBytes,
			Version:  version,
			DomainID: domainID,
			Slug:     slug,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate tenant domains: %w", err)
	}
	return out, nil
}

// TenantDomainCreateSQL inserts an additional, non-default domain
// for a tenant. Unlike
// TenantDomainUpsertSQL it never updates: ON CONFLICT on the
// composite primary key (tenant_id, domain_id) DO NOTHING, so a
// caller can detect "already exists" from a zero row count and map
// it to ErrDomainExists. is_default is always FALSE — the default is
// owned by the upsert path.
//
// Parameters: $1 = tenant_id, $2 = domain_id, $3 = slug, $4 = yaml.
const TenantDomainCreateSQL = `
	INSERT INTO ` + tenant.MetaSchema + `.tenant_domains
		(tenant_id, domain_id, slug, yaml, version, is_default, created_at, updated_at)
	VALUES ($1, $2, $3, $4, 1, FALSE, NOW(), NOW())
	ON CONFLICT (tenant_id, domain_id) DO NOTHING
`

// Create implements Source. Inserts an additional non-default
// domain; returns ErrDomainExists when (tenant_id, domain_id)
// already exists (the upsert/activate path owns updates).
func (p *PostgresSource) Create(ctx context.Context, tenantID string, yaml []byte) error {
	domainID, slug := DeriveStorageIdentity(yaml)
	tag, err := p.pool.Exec(ctx, TenantDomainCreateSQL, tenantID, domainID, slug, yaml)
	if err != nil {
		return fmt.Errorf("create tenant domain: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrDomainExists
	}
	return nil
}

// Delete implements Source.
func (p *PostgresSource) Delete(ctx context.Context, tenantID string) error {
	const stmt = `DELETE FROM ` + tenant.MetaSchema + `.tenant_domains WHERE tenant_id = $1`
	if _, err := p.pool.Exec(ctx, stmt, tenantID); err != nil {
		return fmt.Errorf("delete tenant domain: %w", err)
	}
	return nil
}

// DeleteByDomain implements Source. Removes the single
// (tenant_id, domain_id) row; ErrDomainNotFound when absent.
func (p *PostgresSource) DeleteByDomain(ctx context.Context, tenantID, domainID string) error {
	const stmt = `DELETE FROM ` + tenant.MetaSchema + `.tenant_domains WHERE tenant_id = $1 AND domain_id = $2`
	tag, err := p.pool.Exec(ctx, stmt, tenantID, domainID)
	if err != nil {
		return fmt.Errorf("delete tenant domain by id: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrDomainNotFound
	}
	return nil
}

// UpdateByDomain implements Source. Re-authors the addressed domain's
// yaml in place: version increments, slug and immutable domain_id are
// preserved (an in-place edit never moves the {domain} URL).
// ErrDomainNotFound when the row is absent.
func (p *PostgresSource) UpdateByDomain(ctx context.Context, tenantID, domainID string, yaml []byte) error {
	const stmt = `
		UPDATE ` + tenant.MetaSchema + `.tenant_domains
		SET yaml = $3, version = version + 1, updated_at = NOW()
		WHERE tenant_id = $1 AND domain_id = $2
	`
	tag, err := p.pool.Exec(ctx, stmt, tenantID, domainID, yaml)
	if err != nil {
		return fmt.Errorf("update tenant domain by id: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrDomainNotFound
	}
	return nil
}
