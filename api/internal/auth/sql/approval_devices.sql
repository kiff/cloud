-- approval_devices.sql
--
-- The devices a person approves with: a device whose key lives in
-- its own secure hardware (the Secure Enclave, Android's Keystore, a TPM)
-- signs every answer it gives. KIFF makes each device's id. A
-- device without such hardware isn't meant to be enrolled; until KIFF
-- verifies a key's attestation, that's the app's word.
--
-- This file is the source of truth. ApplyDevicesSchema embeds and runs it
-- at API startup, after developer_sessions.sql. All statements are
-- idempotent.

CREATE TABLE IF NOT EXISTS cloud_meta.approval_devices (
    -- "dev_" and 26 base32 characters, made by KIFF.
    id              TEXT        PRIMARY KEY,
    tenant_id       TEXT        NOT NULL
        REFERENCES cloud_meta.tenants(id) ON DELETE CASCADE,
    -- The person whose device it is (the OIDC sub).
    subject         TEXT        NOT NULL,
    -- What the person calls it, as the device said at sign-in.
    name            TEXT        NOT NULL DEFAULT '',
    -- ios, ipados, macos, android or windows.
    platform        TEXT        NOT NULL,
    -- The device's P-256 key (SPKI DER).
    public_key      BYTEA       NOT NULL,
    -- The attestation format KIFF verified for the key; NULL when none was.
    attestation     TEXT        NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_used_at    TIMESTAMPTZ NULL,
    revoked_at      TIMESTAMPTZ NULL
);

-- A person's devices on an account, for the list and for the verify API.
CREATE INDEX IF NOT EXISTS approval_devices_person_idx
    ON cloud_meta.approval_devices (tenant_id, subject) WHERE revoked_at IS NULL;

-- A developer session signed in from a device names it (the column is in
-- developer_sessions.sql). Sessions from before approval devices that carry
-- device_public_key get a device at startup (LinkLegacyDevices); a CLI's
-- session has none.
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'developer_sessions_device_fk') THEN
        ALTER TABLE cloud_meta.developer_sessions
            ADD CONSTRAINT developer_sessions_device_fk
            FOREIGN KEY (device_id) REFERENCES cloud_meta.approval_devices(id);
    END IF;
END $$;

CREATE INDEX IF NOT EXISTS developer_sessions_device_idx
    ON cloud_meta.developer_sessions (device_id) WHERE device_id IS NOT NULL;

-- One active device per person, account and key: the same
-- phone signing in twice is one device. Duplicates from before this rule
-- (a phone linked once per session) are merged first: the oldest
-- stays, its sessions and theirs name it, and the others are revoked.
WITH ranked AS (
    SELECT id, first_value(id) OVER (PARTITION BY tenant_id, subject, public_key ORDER BY created_at, id) AS keep
    FROM cloud_meta.approval_devices WHERE revoked_at IS NULL
)
UPDATE cloud_meta.developer_sessions s SET device_id = r.keep
FROM ranked r WHERE s.device_id = r.id AND r.id <> r.keep;

WITH ranked AS (
    SELECT id, first_value(id) OVER (PARTITION BY tenant_id, subject, public_key ORDER BY created_at, id) AS keep
    FROM cloud_meta.approval_devices WHERE revoked_at IS NULL
)
UPDATE cloud_meta.approval_devices d SET revoked_at = NOW()
FROM ranked r WHERE d.id = r.id AND r.id <> r.keep;

CREATE UNIQUE INDEX IF NOT EXISTS approval_devices_one_per_key
    ON cloud_meta.approval_devices (tenant_id, subject, public_key) WHERE revoked_at IS NULL;
