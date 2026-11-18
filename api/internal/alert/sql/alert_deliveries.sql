-- alert_deliveries.sql
--
-- One row per alert delivery attempt against one target, triggered by
-- one consequential decision. Persisted so the Control Room evidence
-- timeline can show "alert sent / failed / blocked" per triggering
-- decision (proposal_id), tenant-scoped.
--
-- status is one of delivered | failed | blocked (the SSRF guard's
-- refusal is a first-class status, not a silent drop).
--
-- Idempotent: CREATE IF NOT EXISTS.

CREATE TABLE IF NOT EXISTS cloud_meta.alert_deliveries (
    id           TEXT        NOT NULL,
    tenant_id    TEXT        NOT NULL REFERENCES cloud_meta.tenants(id) ON DELETE CASCADE,
    proposal_id  TEXT        NOT NULL DEFAULT '',
    target_id    TEXT        NOT NULL,
    target_type  TEXT        NOT NULL DEFAULT 'webhook',
    action       TEXT        NOT NULL DEFAULT '',
    outcome      TEXT        NOT NULL DEFAULT '',
    status       TEXT        NOT NULL,
    detail       TEXT        NOT NULL DEFAULT '',
    attempted_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    PRIMARY KEY (tenant_id, id)
);

-- Read deliveries for one triggering decision, newest first.
CREATE INDEX IF NOT EXISTS alert_deliveries_tenant_proposal_idx
    ON cloud_meta.alert_deliveries (tenant_id, proposal_id, attempted_at DESC);

-- alert_retries: failed deliveries waiting to be sent again, with
-- the exact body (signed afresh on each send). A row leaves the queue when
-- it is sent, given up on, or its target is gone or switched off.
CREATE TABLE IF NOT EXISTS cloud_meta.alert_retries (
    id              TEXT        NOT NULL,
    tenant_id       TEXT        NOT NULL REFERENCES cloud_meta.tenants(id) ON DELETE CASCADE,
    target_id       TEXT        NOT NULL,
    event_id        TEXT        NOT NULL DEFAULT '',
    proposal_id     TEXT        NOT NULL DEFAULT '',
    action          TEXT        NOT NULL DEFAULT '',
    outcome         TEXT        NOT NULL DEFAULT '',
    body            BYTEA       NOT NULL,
    attempts        INTEGER     NOT NULL,
    next_attempt_at TIMESTAMPTZ NOT NULL,
    last_error      TEXT        NOT NULL DEFAULT '',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    -- The claim that holds the row while it is sent: updates name it, so
    -- a worker whose lease lapsed can't overwrite a newer attempt.
    claim_token     TEXT        NOT NULL DEFAULT '',
    PRIMARY KEY (tenant_id, id)
);

CREATE INDEX IF NOT EXISTS alert_retries_due_idx
    ON cloud_meta.alert_retries (next_attempt_at);
