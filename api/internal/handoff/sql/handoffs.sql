-- "Your step": a step a taf may never take, handed to its owner to finish
-- (kiff/tafs "Your step"). The taf raises it with its own key; only a
-- person answers it. The words (what, why, about) are kept here, outside
-- anything a receipt hashes or signs, so a row's words can be erased and
-- every receipt still verifies: a receipt carries their hash. The link is
-- kept as its host only, never its path or query, which may carry a token.
CREATE SCHEMA IF NOT EXISTS cloud_meta;

CREATE TABLE IF NOT EXISTS cloud_meta.handoffs (
    tenant_id       TEXT        NOT NULL,
    id              TEXT        NOT NULL,
    agent_id        TEXT        NOT NULL,
    operation       TEXT        NOT NULL,
    what            TEXT        NOT NULL,
    why             TEXT        NOT NULL,
    link_host       TEXT        NOT NULL DEFAULT '',
    about           TEXT        NOT NULL DEFAULT '',
    prepared_by     TEXT[]      NOT NULL DEFAULT '{}',
    version         INT         NOT NULL DEFAULT 1,
    status          TEXT        NOT NULL DEFAULT 'waiting',
    created_at      TIMESTAMPTZ NOT NULL,
    expires_at      TIMESTAMPTZ NOT NULL,
    answered_by     TEXT        NOT NULL DEFAULT '',
    answered_by_id  TEXT        NOT NULL DEFAULT '',
    answered_at     TIMESTAMPTZ,
    device          JSONB,
    checked_by      TEXT        NOT NULL DEFAULT '',
    checked_at      TIMESTAMPTZ,
    raised_receipt  BOOLEAN     NOT NULL DEFAULT false,
    settled_at      TIMESTAMPTZ,
    PRIMARY KEY (tenant_id, id),
    UNIQUE (tenant_id, agent_id, operation)
);

-- The sweep's reads: what waits past its expiry, and what still owes a
-- receipt.
CREATE INDEX IF NOT EXISTS handoffs_waiting ON cloud_meta.handoffs (expires_at) WHERE status = 'waiting';
CREATE INDEX IF NOT EXISTS handoffs_unsettled ON cloud_meta.handoffs (created_at) WHERE settled_at IS NULL;
