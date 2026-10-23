-- cloud_meta.sql
--
-- Bookkeeping schema for KIFF Cloud. Holds the cloud's own data —
-- tenant rows, future api_key records, future anchor batch metadata,
-- future billing usage rows. Distinct from the per-tenant
-- `kiff_t_<id>` schemas that hold framework-side data.
--
-- This file is the source of truth. ApplyCloudMeta() embeds and
-- runs it at API startup. All statements are idempotent so calling
-- them twice is safe.

CREATE SCHEMA IF NOT EXISTS cloud_meta;

CREATE TABLE IF NOT EXISTS cloud_meta.tenants (
    id              TEXT        PRIMARY KEY,
    slug            TEXT        NOT NULL,
    display_name    TEXT        NOT NULL DEFAULT '',
    status          TEXT        NOT NULL DEFAULT 'provisioning',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    provisioned_at  TIMESTAMPTZ NULL,
    metadata        JSONB       NOT NULL DEFAULT '{}'::JSONB,

    CONSTRAINT tenants_slug_key UNIQUE (slug)
);

CREATE INDEX IF NOT EXISTS tenants_status_idx
    ON cloud_meta.tenants (status);

-- Per-tenant secret used to salt anchorable Merkle leaves and (eventually)
-- the per-domain parameter redaction config. Nullable on column add
-- so existing rows survive the ALTER; the API startup hook
-- (BackfillRedactionSalts) populates the column for legacy rows before any
-- request reaches the anchor path. New rows always carry a salt because
-- PostgresStore.Create generates one server-side when the field is empty.
ALTER TABLE cloud_meta.tenants
    ADD COLUMN IF NOT EXISTS redaction_salt BYTEA NULL;
