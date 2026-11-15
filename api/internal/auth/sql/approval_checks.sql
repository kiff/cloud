-- approval_checks.sql
--
-- KIFF's own records that it verified an owner's approval of a connection
-- inside an open approval window (kiff/tafs window_required). Written by
-- /v1/devices/verify when planque asks at the approve, after KIFF checked the
-- device's signature and found the window open. planque may present that
-- approval once more, for Setup's read Card of the same connection, within
-- 30 minutes of checked_at; used_at marks that once. Rows older than a day
-- are deleted with the next record.
--
-- This file is the source of truth. ApplyApprovalChecksSchema embeds and
-- runs it at API startup, after approval_windows.sql. All statements are
-- idempotent.

CREATE TABLE IF NOT EXISTS cloud_meta.approval_checks (
    -- SHA-256 of the signed bytes, a zero byte, and the signature.
    id          TEXT        PRIMARY KEY,
    tenant_id   TEXT        NOT NULL
        REFERENCES cloud_meta.tenants(id) ON DELETE CASCADE,
    device_id   TEXT        NOT NULL
        REFERENCES cloud_meta.approval_devices(id) ON DELETE CASCADE,
    -- planque's grant id, from the signed request's own path.
    grant_id    TEXT        NOT NULL,
    window_id   TEXT        NOT NULL,
    -- KIFF's clock at the check.
    checked_at  TIMESTAMPTZ NOT NULL,
    used_at     TIMESTAMPTZ NULL
);

CREATE INDEX IF NOT EXISTS approval_checks_checked_at
    ON cloud_meta.approval_checks (checked_at);
