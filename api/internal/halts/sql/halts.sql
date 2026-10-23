-- The owner's halts: one row per agent, or one for
-- the whole account (agent_id ''). halted_at is when the latest halt
-- began, kept after a resume. Idempotent; applied on every boot.
CREATE TABLE IF NOT EXISTS cloud_meta.halts (
    tenant_id   TEXT        NOT NULL REFERENCES cloud_meta.tenants(id) ON DELETE CASCADE,
    agent_id    TEXT        NOT NULL,
    halted      BOOLEAN     NOT NULL,
    halted_at   TIMESTAMPTZ NOT NULL,
    halted_by   TEXT        NOT NULL DEFAULT '',
    resumed_at  TIMESTAMPTZ NULL,
    resumed_by  TEXT        NOT NULL DEFAULT '',
    planque_ack TIMESTAMPTZ NULL,
    PRIMARY KEY (tenant_id, agent_id)
);

-- Each halt and resume, for the owner's Record, with
-- the receipt that signs it.
CREATE TABLE IF NOT EXISTS cloud_meta.halt_events (
    tenant_id  TEXT        NOT NULL REFERENCES cloud_meta.tenants(id) ON DELETE CASCADE,
    id         TEXT        NOT NULL,
    agent_id   TEXT        NOT NULL,
    kind       TEXT        NOT NULL,
    at         TIMESTAMPTZ NOT NULL,
    by_subject TEXT        NOT NULL DEFAULT '',
    receipt    TEXT        NOT NULL DEFAULT '',
    PRIMARY KEY (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS halt_events_at ON cloud_meta.halt_events (tenant_id, at DESC);
