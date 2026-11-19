-- cloud_meta.operator_alerts — the operator review queue.
-- Deduped by (tenant_id, kind): one open alert per condition per
-- tenant; re-sync bumps count + last_seen_at.

CREATE SCHEMA IF NOT EXISTS cloud_meta;

CREATE TABLE IF NOT EXISTS cloud_meta.operator_alerts (
    id            TEXT PRIMARY KEY,
    tenant_id     TEXT        NOT NULL REFERENCES cloud_meta.tenants(id) ON DELETE CASCADE,
    tenant_slug   TEXT        NOT NULL DEFAULT '',
    kind          TEXT        NOT NULL,
    severity      TEXT        NOT NULL,
    detail        TEXT        NOT NULL DEFAULT '',
    status        TEXT        NOT NULL DEFAULT 'open',
    count         INTEGER     NOT NULL DEFAULT 1,
    first_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    acked_by      TEXT        NOT NULL DEFAULT '',
    note          TEXT        NOT NULL DEFAULT '',
    UNIQUE (tenant_id, kind)
);

CREATE INDEX IF NOT EXISTS operator_alerts_status_idx
    ON cloud_meta.operator_alerts (status, last_seen_at DESC);
