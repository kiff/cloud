-- receipts.sql
--
-- Per-tenant Attested Action Receipts. v0.1 stores the full
-- receipt as JSONB so the schema does not have to track every
-- field of an experimental format; future slices add typed
-- columns once the format is frozen at v1.
--
-- The (tenant_id, trace_id) primary key matches the receipt's
-- own identity contract: one receipt per (tenant, execution).
-- Re-issuing for the same trace is an UPDATE (e.g. when the
-- attestation lands after the next anchor cycle).
--
-- Lives in cloud_meta because the receipt is a cloud-owned
-- artifact, not framework state. cascade-on-tenant-delete keeps
-- a deleted tenant's receipts from outliving the tenant row.
--
-- Idempotent: every CREATE uses IF NOT EXISTS.

CREATE TABLE IF NOT EXISTS cloud_meta.receipts (
    tenant_id        TEXT        NOT NULL REFERENCES cloud_meta.tenants(id) ON DELETE CASCADE,
    trace_id         TEXT        NOT NULL,
    proposal_id      TEXT        NOT NULL DEFAULT '',
    schema_version   TEXT        NOT NULL,
    entity_id        TEXT        NOT NULL,
    entity_type      TEXT        NOT NULL,
    action_name      TEXT        NOT NULL,
    issued_at        TIMESTAMPTZ NOT NULL,
    causal_chain_hash TEXT       NOT NULL,
    body             JSONB       NOT NULL,
    anchor_tx_hash   TEXT        NULL,
    anchored_at      TIMESTAMPTZ NULL,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    PRIMARY KEY (tenant_id, trace_id)
);

-- Explicit decision->receipt link: the proposal id the
-- receipt was produced for, stamped at build time from the decision's
-- audit data. Added idempotently for receipts created before G2.
ALTER TABLE cloud_meta.receipts
    ADD COLUMN IF NOT EXISTS proposal_id TEXT NOT NULL DEFAULT '';

-- Resolve a receipt by the proposal that produced it (the evidence
-- object / proof pack link), tenant-scoped.
CREATE INDEX IF NOT EXISTS receipts_tenant_proposal_idx
    ON cloud_meta.receipts (tenant_id, proposal_id)
    WHERE proposal_id <> '';

-- Lookups in the dashboard typically filter by tenant + entity,
-- ordered by issuance.
CREATE INDEX IF NOT EXISTS receipts_tenant_entity_idx
    ON cloud_meta.receipts (tenant_id, entity_id, issued_at DESC);

-- The anchoring batch reads receipts that have not yet been
-- attested. NULL anchor_tx_hash is the fast path; partial index
-- keeps it cheap.
CREATE INDEX IF NOT EXISTS receipts_unanchored_idx
    ON cloud_meta.receipts (tenant_id, issued_at)
    WHERE anchor_tx_hash IS NULL;
