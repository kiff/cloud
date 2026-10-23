-- decision_cursors.sql
--
-- Per-tenant high-water mark for the decision-receipt sweep
-- (ReconcileDecisions). reconciled_through is the CreatedAt of the
-- newest proposal_decision reconciled in a clean sweep. Records strictly
-- BEFORE it are skipped (already reconciled), so the sweep doesn't
-- re-evaluate all history every cycle and a restart resumes instead of
-- re-minting from scratch (which caused a first-sweep anchor
-- thundering-herd).
--
-- Timestamp-only boundary: records exactly AT the mark are re-checked each
-- sweep (a cheap idempotent no-op via the receipt-existence dedup) rather
-- than skipped, so a later audit record sharing that exact timestamp is
-- never missed. A future compound cursor (created_at, audit_record_id)
-- could implement exact at-or-before skipping if needed.
--
-- The watermark only advances past a decision once that decision is
-- confirmed reconciled, so a transient mint failure holds the mark and
-- self-heals on a later sweep — the cursor is an optimization layered on
-- top of the receipt-existence dedup, never the sole correctness mechanism.
--
-- Lives in cloud_meta (cloud-owned bookkeeping, not framework state);
-- cascade-on-tenant-delete so it never outlives the tenant.
-- Idempotent: CREATE uses IF NOT EXISTS.

CREATE TABLE IF NOT EXISTS cloud_meta.guard_decision_cursors (
    tenant_id          TEXT        NOT NULL REFERENCES cloud_meta.tenants(id) ON DELETE CASCADE,
    reconciled_through TIMESTAMPTZ NOT NULL,
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    PRIMARY KEY (tenant_id)
);
