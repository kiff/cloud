-- subject_tenants.sql
--
-- Maps an authenticated subject (Clerk OIDC sub, future provider
-- subjects) to the tenant the cloud auto-provisioned for that
-- user. One row per subject. The mapping is permanent for v0.1:
-- a subject keeps its tenant for the lifetime of the account.
-- Multi-tenant memberships and tenant transfer are out of scope
-- (multiple workspaces per user is deferred).
--
-- ApplySubjectTenantsSchema() embeds and runs this at API startup
-- alongside ApplyCloudMeta. All statements are idempotent.

CREATE TABLE IF NOT EXISTS cloud_meta.subject_tenants (
    subject     TEXT        PRIMARY KEY,
    tenant_id   TEXT        NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT subject_tenants_tenant_fk
        FOREIGN KEY (tenant_id) REFERENCES cloud_meta.tenants (id)
        ON DELETE RESTRICT
);

-- Reverse-lookup index. Used by ops queries ("which subjects map
-- to this tenant"); not on the hot path of session resolution.
CREATE INDEX IF NOT EXISTS subject_tenants_tenant_idx
    ON cloud_meta.subject_tenants (tenant_id);
