-- tenant_domain_drafts.sql
--
-- Per-tenant DRAFT domain YAML — the auto-save store for KIFF
-- Studio. Drafts are durable
-- across sessions, browser closes, and machines: every block
-- edit on /dashboard/start/studio writes here.
--
-- The runtime resolver does NOT read this table. Drafts never
-- reach the per-tenant Runtime; only the active domain
-- (cloud_meta.tenant_domains) does. "Save and activate" is a
-- single transaction that upserts the draft into tenant_domains
-- and deletes the draft row, so the resolver sees no missing-
-- domain window.
--
-- One draft per tenant (PRIMARY KEY on tenant_id). Two sessions
-- racing on the same tenant resolve via last-write-wins on the
-- upsert; out of scope to mediate.
--
-- Idempotent: every CREATE uses IF NOT EXISTS.

CREATE TABLE IF NOT EXISTS cloud_meta.tenant_domain_drafts (
    tenant_id   TEXT        PRIMARY KEY REFERENCES cloud_meta.tenants(id) ON DELETE CASCADE,
    yaml        BYTEA       NOT NULL,
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
