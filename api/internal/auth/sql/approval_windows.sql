-- approval_windows.sql
--
-- The approval windows (kiff/tafs#494): one per opening, by the phone's
-- approval key behind its fingerprint. While a window is open, routine
-- actions from its device are signed with the device key and carry the
-- window's id. KIFF restarts the window's expiry on each approval, never
-- past its cap, and closes it on sign-out, revocation, a halt, the app's
-- own Close, or a new window on the same device.
--
-- This file is the source of truth. ApplyWindowsSchema embeds and runs it
-- at API startup, after approval_devices.sql. All statements are
-- idempotent.

CREATE TABLE IF NOT EXISTS cloud_meta.approval_windows (
    -- "win_" and 26 base32 characters, made by KIFF.
    id          TEXT        PRIMARY KEY,
    tenant_id   TEXT        NOT NULL
        REFERENCES cloud_meta.tenants(id) ON DELETE CASCADE,
    subject     TEXT        NOT NULL,
    device_id   TEXT        NOT NULL
        REFERENCES cloud_meta.approval_devices(id) ON DELETE CASCADE,
    -- The phone's session that opened it.
    session_id  TEXT        NOT NULL,
    opened_at   TIMESTAMPTZ NOT NULL,
    expires_at  TIMESTAMPTZ NOT NULL,
    cap_at      TIMESTAMPTZ NOT NULL,
    -- The fingerprint of the approval key that opened it.
    opened_by   TEXT        NOT NULL DEFAULT '',
    closed_at   TIMESTAMPTZ NULL,
    closed_by   TEXT        NOT NULL DEFAULT ''
);

-- A device's open window, read on every routine action inside it.
CREATE INDEX IF NOT EXISTS approval_windows_device_open_idx
    ON cloud_meta.approval_windows (device_id) WHERE closed_at IS NULL;
