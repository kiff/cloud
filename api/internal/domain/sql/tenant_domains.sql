-- tenant_domains.sql
--
-- Per-tenant operational domains, keyed by (tenant_id, domain_id).
-- A tenant may hold more than one domain; domain_id
-- is the stable per-tenant identity (immutable), slug the URL-safe
-- token (mutable on rename), is_default marks the domain the
-- back-compat singular /v1/me/domain* routes operate on.
-- Versioned so the runtime resolver can cache the built
-- *kiff/runtime.Runtime and invalidate when the tenant edits.
--
-- Fresh databases get the composite-key shape directly from this
-- CREATE. Existing single-domain databases (tenant_id PRIMARY KEY,
-- no domain_id) are migrated forward idempotently in Go by
-- migrateTenantDomainsComposite — add columns, backfill the sole
-- row's identity from its YAML, swap the primary key — because the
-- backfill must read the domain name out of the YAML, which SQL
-- cannot do. ApplyTenantDomainsSchema runs both.
--
-- Idempotent: every CREATE uses IF NOT EXISTS; the Go migration is a
-- no-op once the table is already composite-keyed.

CREATE TABLE IF NOT EXISTS cloud_meta.tenant_domains (
    tenant_id   TEXT        NOT NULL REFERENCES cloud_meta.tenants(id) ON DELETE CASCADE,
    domain_id   TEXT        NOT NULL DEFAULT '',
    slug        TEXT        NOT NULL DEFAULT '',
    yaml        BYTEA       NOT NULL,
    version     INT         NOT NULL DEFAULT 1,
    is_default  BOOLEAN     NOT NULL DEFAULT FALSE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    PRIMARY KEY (tenant_id, domain_id)
);
