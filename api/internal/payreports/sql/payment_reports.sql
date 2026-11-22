-- Payments their owner reported as unexpected ("Report this payment"):
-- one row per receipt, the first report's. The receipt it names is kept
-- through a test account's prune; the account's deletion removes both.
-- Idempotent; applied on every boot.
CREATE TABLE IF NOT EXISTS cloud_meta.payment_reports (
    tenant_id   TEXT        NOT NULL REFERENCES cloud_meta.tenants(id) ON DELETE CASCADE,
    trace_id    TEXT        NOT NULL,
    proposal_id TEXT        NOT NULL,
    agent_id    TEXT        NOT NULL,
    reported_at TIMESTAMPTZ NOT NULL,
    reported_by TEXT        NOT NULL DEFAULT '',
    PRIMARY KEY (tenant_id, trace_id)
);
