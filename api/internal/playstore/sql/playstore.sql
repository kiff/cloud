-- The tafs Android app's Google Play purchases. Idempotent; applied on
-- every boot, after cloud_meta.tenants.

-- Each purchase KIFF applied, by Play's purchase token: Solo's subscription
-- or a pack. Recorded once per token and updated with what Play said last,
-- so a purchase sent again changes nothing, and support can find it.
CREATE TABLE IF NOT EXISTS cloud_meta.google_purchases (
    purchase_token  TEXT        PRIMARY KEY,
    tenant_id       TEXT        NOT NULL REFERENCES cloud_meta.tenants(id) ON DELETE CASCADE,
    product         TEXT        NOT NULL,
    kind            TEXT        NOT NULL CHECK (kind IN ('subscription', 'product')),
    order_id        TEXT        NOT NULL DEFAULT '',
    state           TEXT        NOT NULL DEFAULT '', -- Play's state, as last read
    test            BOOLEAN     NOT NULL DEFAULT false,
    acknowledged_at TIMESTAMPTZ NULL,                -- when KIFF acknowledged a subscription
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS google_purchases_tenant
    ON cloud_meta.google_purchases (tenant_id);
