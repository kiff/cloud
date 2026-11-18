-- usage.sql
--
-- Per-tenant usage counters. Aggregated hourly so billing rollups
-- and usage anchoring read fixed-cardinality batches rather
-- than per-call rows.
--
-- Lives in cloud_meta because billing is a cloud concern (billing
-- is kept apart from the runtime). Rows carry tenant_id; access control
-- is enforced by the cloud's billing layer, not by Postgres schema.
--
-- Idempotent: every CREATE uses IF NOT EXISTS.

CREATE TABLE IF NOT EXISTS cloud_meta.tenant_usage (
    tenant_id     TEXT        NOT NULL REFERENCES cloud_meta.tenants(id) ON DELETE CASCADE,
    counter_name  TEXT        NOT NULL,
    entity_type   TEXT        NOT NULL,
    domain_id     TEXT        NOT NULL DEFAULT '',
    hour_bucket   TIMESTAMPTZ NOT NULL,
    count         BIGINT      NOT NULL DEFAULT 0,
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    PRIMARY KEY (tenant_id, counter_name, entity_type, domain_id, hour_bucket)
);

-- domain_id dimension: added idempotently for
-- existing databases; the primary-key swap to include it is handled
-- in Go (migrateTenantUsageDomain) because it must be ordered after
-- the column add. Existing rows default to '' (tenant-wide).
ALTER TABLE cloud_meta.tenant_usage
    ADD COLUMN IF NOT EXISTS domain_id TEXT NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS tenant_usage_hour_idx
    ON cloud_meta.tenant_usage (hour_bucket);

CREATE INDEX IF NOT EXISTS tenant_usage_counter_idx
    ON cloud_meta.tenant_usage (counter_name);
