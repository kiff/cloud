-- pairings.sql
--
-- Computers a person's phone approved with its approval key
-- (the desktop client contract, §5). The computer claims each once, proving it
-- holds the device key, and gets a session bound to the keys kept here.
-- KIFF keeps the nonce's SHA-256, never the nonce, and no token.
--
-- This file is the source of truth. ApplyPairingsSchema embeds and runs it
-- at API startup, after approval_devices.sql. All statements are
-- idempotent.

CREATE TABLE IF NOT EXISTS cloud_meta.pairings (
    -- "pair_" and 26 base32 characters, made by KIFF.
    id                  TEXT        PRIMARY KEY,
    tenant_id           TEXT        NOT NULL
        REFERENCES cloud_meta.tenants(id) ON DELETE CASCADE,
    -- The person whose phone approved it.
    subject             TEXT        NOT NULL,
    -- The device whose approval key approved it.
    approved_by         TEXT        NOT NULL,
    -- The computer's device and approval keys (P-256, SPKI DER).
    public_key          BYTEA       NOT NULL,
    approval_public_key BYTEA       NULL,
    name                TEXT        NOT NULL,
    -- windows, macos or linux.
    platform            TEXT        NOT NULL,
    -- The QR's kind: hello or enclave.
    kind                TEXT        NOT NULL,
    -- sha256 of the QR's 32-byte nonce: approved once.
    nonce_hash          BYTEA       NOT NULL UNIQUE,
    expires_at          TIMESTAMPTZ NOT NULL,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    -- Set once, when the computer claims it.
    claimed_at          TIMESTAMPTZ NULL
);
