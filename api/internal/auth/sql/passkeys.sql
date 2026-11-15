-- passkeys.sql
--
-- WebAuthn credentials registered to an account (the desktop client
-- contract, §2.6). Today each is a computer's own platform passkey (Windows
-- Hello), registered by the phone's pairing for that one device and removed
-- with it. KIFF keeps the public key (P-256, SPKI DER) and the last sign
-- counter, never anything secret.
--
-- This file is the source of truth. ApplyPasskeysSchema embeds and runs it
-- at API startup, after approval_devices.sql. All statements are
-- idempotent.

CREATE TABLE IF NOT EXISTS cloud_meta.passkeys (
    tenant_id     TEXT        NOT NULL
        REFERENCES cloud_meta.tenants(id) ON DELETE CASCADE,
    -- The WebAuthn credential id, as the authenticator made it.
    credential_id BYTEA       NOT NULL,
    -- The person it approves for.
    subject       TEXT        NOT NULL,
    -- The device a computer's own passkey belongs to: it counts only for
    -- that device's session.
    device_id     TEXT        NOT NULL DEFAULT '',
    -- ES256 (P-256), SPKI DER.
    public_key    BYTEA       NOT NULL,
    -- The last sign counter an assertion carried; 0 when it keeps none.
    sign_count    BIGINT      NOT NULL DEFAULT 0,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    revoked_at    TIMESTAMPTZ NULL,
    PRIMARY KEY (tenant_id, credential_id)
);

-- The passkeys a device holds, removed with it.
CREATE INDEX IF NOT EXISTS passkeys_device_idx
    ON cloud_meta.passkeys (device_id) WHERE device_id <> '' AND revoked_at IS NULL;

-- A passkey the person registered for the account (POST /v1/me/passkeys):
-- KIFF's id for it, the person's name for it, and whether its last assertion
-- said it's synced (NULL until one has). A computer's own has no key_id.
ALTER TABLE cloud_meta.passkeys ADD COLUMN IF NOT EXISTS key_id TEXT NULL;
ALTER TABLE cloud_meta.passkeys ADD COLUMN IF NOT EXISTS name TEXT NOT NULL DEFAULT '';
ALTER TABLE cloud_meta.passkeys ADD COLUMN IF NOT EXISTS synced BOOLEAN NULL;
CREATE UNIQUE INDEX IF NOT EXISTS passkeys_key_id_idx ON cloud_meta.passkeys (key_id) WHERE key_id IS NOT NULL;
