-- developer_sessions.sql
--
-- Cloud-meta table for developer sessions.
-- One row per issued session minted through `kiff auth login`'s device
-- flow. The plaintext token is NEVER stored — only its SHA-256 hash.
-- Lookups go: presented token -> hash -> row -> subject + tenant.
--
-- A stored row (rather than a stateless signed token) is deliberate:
-- a developer session can mint runtime keys and drive the operator
-- CLI, so per-device revocation (delete/flag one row) is required —
-- all-or-nothing revoke would be a brand liability for a governance
-- product.
--
-- This file is the source of truth. ApplyDeveloperSessionsSchema
-- embeds and runs it at API startup. All statements are idempotent.

CREATE TABLE IF NOT EXISTS cloud_meta.developer_sessions (
    -- Stable ULID identifier for the session (per-device revoke keys
    -- on this via Revoke(id); `kiff auth logout` revokes by hash).
    id              TEXT        PRIMARY KEY,

    -- Tenant the session acts within, bound at approval time. Foreign
    -- key to cloud_meta.tenants so a tenant delete cascades.
    tenant_id       TEXT        NOT NULL
        REFERENCES cloud_meta.tenants(id) ON DELETE CASCADE,

    -- The human subject that approved the device flow (the OIDC sub).
    subject         TEXT        NOT NULL,

    -- Device label for the human to recognize the session (e.g. the
    -- CLI host). Free-form, set at approval.
    label           TEXT        NOT NULL DEFAULT '',

    -- SHA-256 of the plaintext token, lowercase hex (64 chars).
    -- Lookups use this column; the plaintext is delivered once via the
    -- device-token poll and never persisted.
    token_hash      TEXT        NOT NULL,

    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    -- Bounded TTL backstop (revocation is the primary lever).
    expires_at      TIMESTAMPTZ NOT NULL,
    last_seen_at    TIMESTAMPTZ NULL,
    revoked_at      TIMESTAMPTZ NULL,

    CONSTRAINT developer_sessions_hash_key UNIQUE (token_hash)
);

-- Active-session lookups for `kiff auth status` are per subject.
CREATE INDEX IF NOT EXISTS developer_sessions_subject_active_idx
    ON cloud_meta.developer_sessions (subject) WHERE revoked_at IS NULL;

CREATE INDEX IF NOT EXISTS developer_sessions_tenant_idx
    ON cloud_meta.developer_sessions (tenant_id);

-- The phone's P-256 public key (SPKI DER), bound when its device flow was
-- approved. An answer to a held call through the session counts
-- only with a signature from this key. NULL for sessions without one.
ALTER TABLE cloud_meta.developer_sessions
    ADD COLUMN IF NOT EXISTS device_public_key BYTEA NULL;

-- The approval device the session signed in from, when it has a
-- key. Its foreign key to cloud_meta.approval_devices is added by
-- approval_devices.sql, which runs after this file. NULL for a session
-- without a device (the CLI's), and for a phone from before approval devices until
-- LinkLegacyDevices gives it one.
ALTER TABLE cloud_meta.developer_sessions
    ADD COLUMN IF NOT EXISTS device_id TEXT NULL;
