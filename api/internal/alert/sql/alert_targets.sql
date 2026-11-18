CREATE TABLE IF NOT EXISTS cloud_meta.alert_targets (
    id         TEXT        NOT NULL,
    tenant_id  TEXT        NOT NULL REFERENCES cloud_meta.tenants(id) ON DELETE CASCADE,
    action     TEXT        NOT NULL DEFAULT '',
    url        TEXT        NOT NULL,
    outcomes   JSONB       NOT NULL DEFAULT '[]'::JSONB,
    enabled    BOOLEAN     NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    PRIMARY KEY (tenant_id, id)
);

CREATE INDEX IF NOT EXISTS alert_targets_tenant_idx
    ON cloud_meta.alert_targets (tenant_id, created_at DESC);

-- The secret that signs every delivery to this target (KIFF-Signature).
-- Shown once, when the target is made or its secret rotated. Empty
-- for targets made before signing, which are sent unsigned until rotated.
ALTER TABLE cloud_meta.alert_targets
    ADD COLUMN IF NOT EXISTS signing_secret TEXT NOT NULL DEFAULT '';

-- Why and when KIFF switched a target off: its deliveries kept failing
-- for about a day. Empty while on.
ALTER TABLE cloud_meta.alert_targets ADD COLUMN IF NOT EXISTS disabled_reason TEXT NOT NULL DEFAULT '';
ALTER TABLE cloud_meta.alert_targets ADD COLUMN IF NOT EXISTS disabled_at TIMESTAMPTZ NULL;
