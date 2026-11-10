-- device_sign_ins.sql
--
-- Device sign-ins waiting for approval (`kiff auth login`, the tafs app):
-- the code a device shows, until the person approves it in the browser or
-- it expires. Kept here, not in the API process's memory, so a deploy never
-- loses a code someone is about to approve. A row holds no secret: the
-- device code is kept as its sha256, and the session an approval mints is
-- handed to the device from memory, never stored here. A row goes when it
-- is approved, and when it expires (10 minutes).
--
-- This file is the source of truth. ApplyDeviceSignInsSchema embeds and
-- runs it at API startup. All statements are idempotent.

CREATE TABLE IF NOT EXISTS cloud_meta.device_sign_ins (
    -- sha256 of the device code, hex.
    device_hash     TEXT        PRIMARY KEY,
    -- The short code the person types or follows, e.g. "BCDF-GHJK".
    user_code       TEXT        NOT NULL UNIQUE,
    -- Set only for a re-link: the session's person and account, the only
    -- ones who may approve it, and the session whose key it replaces.
    subject         TEXT        NOT NULL DEFAULT '',
    tenant_id       TEXT        NOT NULL DEFAULT '',
    relink_session  TEXT        NOT NULL DEFAULT '',
    -- The device's keys and name, as it said at the start.
    device_key      BYTEA       NULL,
    approval_key    BYTEA       NULL,
    device_name     TEXT        NOT NULL DEFAULT '',
    platform        TEXT        NOT NULL DEFAULT '',
    platform_sent   BOOLEAN     NOT NULL DEFAULT FALSE,
    created_at      TIMESTAMPTZ NOT NULL,
    expires_at      TIMESTAMPTZ NOT NULL
);

CREATE INDEX IF NOT EXISTS device_sign_ins_expires_at ON cloud_meta.device_sign_ins (expires_at);
CREATE INDEX IF NOT EXISTS device_sign_ins_subject ON cloud_meta.device_sign_ins (subject) WHERE subject <> '';
