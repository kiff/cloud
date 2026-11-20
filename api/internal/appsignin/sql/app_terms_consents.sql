-- app_terms_consents.sql
--
-- Each time a person agrees to the terms when signing in inside the tafs
-- apps: the version they ticked, recorded in the same step that mints the
-- session (README.md, Consent). Kept with the account and erased with it.
--
-- This file is the source of truth. ApplyConsentsSchema embeds and runs it
-- at API startup, after developer_sessions.sql and approval_devices.sql.
-- All statements are idempotent.

CREATE TABLE IF NOT EXISTS cloud_meta.app_terms_consents (
    id            BIGSERIAL   PRIMARY KEY,
    tenant_id     TEXT        NOT NULL
        REFERENCES cloud_meta.tenants(id) ON DELETE CASCADE,
    -- The person who ticked (the OIDC sub).
    subject       TEXT        NOT NULL,
    -- The terms' version in force, as GET /v1/auth/app/terms says it.
    terms_version TEXT        NOT NULL,
    -- The client they signed in with: "tafs-app".
    client        TEXT        NOT NULL,
    -- The device and session the sign-in made ("" for a device with no key).
    device_id     TEXT        NOT NULL DEFAULT '',
    session_id    TEXT        NOT NULL,
    -- KIFF's own clock, never the app's.
    accepted_at   TIMESTAMPTZ NOT NULL
);

CREATE INDEX IF NOT EXISTS app_terms_consents_person
    ON cloud_meta.app_terms_consents (tenant_id, subject, accepted_at DESC);
