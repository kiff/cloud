-- The tafs app's Apple purchases. Idempotent; applied on every boot, after
-- cloud_meta.tenants.

-- Each account's appAccountToken: the UUID the app attaches to every
-- purchase, so Apple's transactions and notifications name their account.
-- Made the first time the account is read for it, and never changed.
CREATE TABLE IF NOT EXISTS cloud_meta.apple_accounts (
    tenant_id         TEXT        PRIMARY KEY REFERENCES cloud_meta.tenants(id) ON DELETE CASCADE,
    app_account_token UUID        NOT NULL UNIQUE,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);
