-- cloud_meta.operator_audit — append-only record of mutating
-- operator actions on the control plane.
--
-- Append-only by contract in the code: there is no UPDATE/DELETE path.
-- The database does not enforce it: kiff-api connects as the RDS master
-- user, which can change or delete rows. The only automatic change is
-- ON DELETE SET NULL on target_tenant_id when a tenant is hard-deleted.
-- The row id is an application-generated ULID-based string.

CREATE SCHEMA IF NOT EXISTS cloud_meta;

CREATE TABLE IF NOT EXISTS cloud_meta.operator_audit (
    id               TEXT PRIMARY KEY,
    operator_subject TEXT        NOT NULL,
    action           TEXT        NOT NULL,
    target_tenant_id TEXT        NULL REFERENCES cloud_meta.tenants(id) ON DELETE SET NULL,
    reason           TEXT        NOT NULL DEFAULT '',
    metadata         JSONB       NOT NULL DEFAULT '{}'::jsonb,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Newest-first reads, optionally scoped to one tenant.
CREATE INDEX IF NOT EXISTS operator_audit_created_idx
    ON cloud_meta.operator_audit (created_at DESC);
CREATE INDEX IF NOT EXISTS operator_audit_tenant_idx
    ON cloud_meta.operator_audit (target_tenant_id, created_at DESC);
