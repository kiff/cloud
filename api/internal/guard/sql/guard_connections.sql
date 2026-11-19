CREATE TABLE IF NOT EXISTS cloud_meta.guard_connections (
    tenant_id     TEXT        NOT NULL REFERENCES cloud_meta.tenants(id) ON DELETE CASCADE,
    project       TEXT        NOT NULL DEFAULT 'default',
    environment   TEXT        NOT NULL DEFAULT 'dev',
    agent_id      TEXT        NOT NULL,
    workflow      TEXT        NOT NULL DEFAULT 'default',
    adapter       TEXT        NOT NULL,
    sdk_version   TEXT        NOT NULL DEFAULT '',
    mode          TEXT        NOT NULL DEFAULT 'observe',
    first_seen_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_seen_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    seen_count    BIGINT      NOT NULL DEFAULT 1,

    PRIMARY KEY (tenant_id, project, environment, agent_id, workflow)
);

-- key_id records which API key most recently authenticated this runtime,
-- so the dashboard can revoke exactly that key (the control lever for a
-- runtime — there is no per-runtime mutation on the guard API). Added via
-- ALTER ... IF NOT EXISTS so existing deployments pick it up idempotently;
-- '' when the runtime authenticated via a session JWT rather than a key.
ALTER TABLE cloud_meta.guard_connections
    ADD COLUMN IF NOT EXISTS key_id TEXT NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS guard_connections_tenant_last_seen_idx
    ON cloud_meta.guard_connections (tenant_id, last_seen_at DESC);

CREATE TABLE IF NOT EXISTS cloud_meta.guard_observations (
    tenant_id    TEXT        NOT NULL REFERENCES cloud_meta.tenants(id) ON DELETE CASCADE,
    project      TEXT        NOT NULL DEFAULT 'default',
    environment  TEXT        NOT NULL DEFAULT 'dev',
    agent_id     TEXT        NOT NULL,
    workflow     TEXT        NOT NULL DEFAULT 'default',
    tools        JSONB       NOT NULL DEFAULT '[]'::JSONB,
    observed_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    PRIMARY KEY (tenant_id, project, environment, agent_id, workflow),
    FOREIGN KEY (tenant_id, project, environment, agent_id, workflow)
        REFERENCES cloud_meta.guard_connections (tenant_id, project, environment, agent_id, workflow)
        ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS guard_observations_tenant_updated_idx
    ON cloud_meta.guard_observations (tenant_id, updated_at DESC);
