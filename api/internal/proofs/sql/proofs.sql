-- cloud_meta.proof_runs: one row per live proof of an integration, sent by
-- the proof runner in kiff/agents (POST /v1/ops/proofs, GitHub OIDC only).
-- Test tenants only: the ingest refuses any other tenant. Append-only: the
-- application inserts and reads, and the only delete is the 60-day expiry
-- sweep. The run's step-by-step events stay in the private reports bucket
-- under events_key; only the summary is kept here.
CREATE SCHEMA IF NOT EXISTS cloud_meta;

CREATE TABLE IF NOT EXISTS cloud_meta.proof_runs (
    run_id          TEXT        PRIMARY KEY,
    schema_version  INTEGER     NOT NULL,
    app             TEXT        NOT NULL,
    job             TEXT        NOT NULL DEFAULT '',
    tenant_id       TEXT        NOT NULL,
    run_trigger     TEXT        NOT NULL,
    thread          TEXT        NOT NULL DEFAULT '',
    started_at      TIMESTAMPTZ NOT NULL,
    ended_at        TIMESTAMPTZ NOT NULL,
    outcome         TEXT        NOT NULL,
    checks          JSONB       NOT NULL,
    limits_held     BOOLEAN     NOT NULL,
    p50_ms          INTEGER     NOT NULL,
    p95_ms          INTEGER     NOT NULL,
    runner_version  TEXT        NOT NULL,
    events_key      TEXT        NOT NULL,
    -- The catalog entry revision the run proved (name@rev): an app's state
    -- is kept per entry, and planque reads it per entry.
    entry           TEXT        NOT NULL,
    -- Digests of what judged the run, as the runner sent them: JSON, not
    -- JSONB, so the text is kept exactly as given.
    rules           JSON        NOT NULL,
    -- Whether runner_version was confirmed on kiff/agents main at ingest.
    -- An uncounted row is shown, but never moves an app's state.
    counted         BOOLEAN     NOT NULL,
    -- The GitHub run that sent it, from its signed token.
    gh_run_id       TEXT        NOT NULL DEFAULT '',
    gh_run_attempt  TEXT        NOT NULL DEFAULT '',
    received_at     TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS proof_runs_app_idx
    ON cloud_meta.proof_runs (app, ended_at DESC);
CREATE INDEX IF NOT EXISTS proof_runs_entry_idx
    ON cloud_meta.proof_runs (entry, ended_at DESC);
CREATE INDEX IF NOT EXISTS proof_runs_received_idx
    ON cloud_meta.proof_runs (received_at);

-- cloud_meta.proof_overrides: an operator's hold or release of one app's
-- writes, as an append-only log. The newest row per app that hasn't
-- expired is in force; "clear" ends it. Each row is also written to the
-- operator audit. A release needs the owner's yes (owner_approval).
CREATE TABLE IF NOT EXISTS cloud_meta.proof_overrides (
    id              TEXT        PRIMARY KEY,
    app             TEXT        NOT NULL,
    action          TEXT        NOT NULL,
    operator        TEXT        NOT NULL,
    reason          TEXT        NOT NULL,
    owner_approval  TEXT        NOT NULL DEFAULT '',
    until_at        TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS proof_overrides_app_idx
    ON cloud_meta.proof_overrides (app, created_at DESC);
