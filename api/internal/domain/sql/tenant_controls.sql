-- tenant_controls.sql
--
-- Per-tenant control records. A control is created every time a
-- tenant activates a domain. It is the product unit the pricing
-- page sells: one protected production action, governed by KIFF.
--
-- Status values:
--   active      — the current governing domain for this tenant.
--                 Only one active control per tenant at a time.
--   superseded  — a previous activation, replaced by a newer one.
--                 Kept for audit history; proof receipts reference
--                 it by entity_type + action_names.
--
-- actions is a JSONB array of action name strings derived from
-- the activated domain's action list.
--
-- environment and workflow give a control an addressable identity
-- beyond (tenant, action). They default to the
-- guard connection defaults ('dev' / 'default') so existing
-- single-control tenants are unchanged.
--
-- Idempotent: every CREATE uses IF NOT EXISTS; the ALTERs add the
-- identity columns to tables created before them.

CREATE TABLE IF NOT EXISTS cloud_meta.tenant_controls (
    id            TEXT        PRIMARY KEY,
    tenant_id     TEXT        NOT NULL REFERENCES cloud_meta.tenants(id) ON DELETE CASCADE,
    domain        TEXT        NOT NULL,
    entity        TEXT        NOT NULL,
    actions       JSONB       NOT NULL DEFAULT '[]',
    environment   TEXT        NOT NULL DEFAULT 'dev',
    workflow      TEXT        NOT NULL DEFAULT 'default',
    version       INTEGER     NOT NULL DEFAULT 1,
    activated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    activated_by  TEXT        NOT NULL DEFAULT '',
    status        TEXT        NOT NULL DEFAULT 'active'
                              CHECK (status IN ('active', 'superseded'))
);

-- Backfill identity columns on pre-#376 tables.
ALTER TABLE cloud_meta.tenant_controls
    ADD COLUMN IF NOT EXISTS environment TEXT NOT NULL DEFAULT 'dev';
ALTER TABLE cloud_meta.tenant_controls
    ADD COLUMN IF NOT EXISTS workflow TEXT NOT NULL DEFAULT 'default';
-- Monotonic version per (tenant, environment) control lineage.
ALTER TABLE cloud_meta.tenant_controls
    ADD COLUMN IF NOT EXISTS version INTEGER NOT NULL DEFAULT 1;
-- Per-domain governance posture (observe | enforce). '' means "unset";
-- the decide path falls back to the tenant-wide mode for controls
-- created before per-domain posture, so existing behavior is preserved.
-- New activations default to 'observe' (watch real traffic first, flip deliberately).
ALTER TABLE cloud_meta.tenant_controls
    ADD COLUMN IF NOT EXISTS mode TEXT NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS tenant_controls_tenant_status
    ON cloud_meta.tenant_controls (tenant_id, status);

-- One active control per (tenant, domain, environment). Closes the
-- concurrent first-activation race: without it, two
-- racing first-activations both supersede zero rows, take no lock, and
-- both insert status='active' version=1. The partial unique index makes
-- the second insert fail cleanly so PostgresControlStore.Create retries
-- into the correct next version.
--
-- Scoped by domain: a tenant with more than one
-- domain has one active control PER DOMAIN in a given environment, so
-- activating domain B never supersedes domain A's control. The pre-
-- multi-domain index keyed only on (tenant, environment) — dropped
-- below so a fresh boot and an existing deployment converge.
DROP INDEX IF EXISTS cloud_meta.tenant_controls_one_active_per_env;
CREATE UNIQUE INDEX IF NOT EXISTS tenant_controls_one_active_per_domain_env
    ON cloud_meta.tenant_controls (tenant_id, domain, environment)
    WHERE status = 'active';

-- Identity lookup: active control for a tenant by environment.
CREATE INDEX IF NOT EXISTS tenant_controls_identity
    ON cloud_meta.tenant_controls (tenant_id, status, environment);
