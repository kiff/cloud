-- code_sign_in.sql
--
-- Signing in with a code sent by email (README.md). This file is the source
-- of truth: ApplySchema embeds and runs it at API startup. All statements
-- are idempotent.

-- A person's setting: off unless they turned it on. Kept until their sign-in
-- is erased.
CREATE TABLE IF NOT EXISTS cloud_meta.signin_settings (
    -- The person (the OIDC sub), whatever accounts they're in.
    subject    TEXT        PRIMARY KEY,
    email_code BOOLEAN     NOT NULL DEFAULT FALSE,
    changed_at TIMESTAMPTZ NOT NULL
);

-- A code that was sent, as a keyed hash only, until it's used or lapses
-- (ten minutes). One per browser and address.
CREATE TABLE IF NOT EXISTS cloud_meta.signin_codes (
    id         TEXT        PRIMARY KEY,
    -- Keyed hashes of the browser's flow and of the address.
    flow       TEXT        NOT NULL,
    address    TEXT        NOT NULL,
    subject    TEXT        NOT NULL,
    -- HMAC over the code's id, the person and the code.
    mac        BYTEA       NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL
);

CREATE UNIQUE INDEX IF NOT EXISTS signin_codes_flow
    ON cloud_meta.signin_codes (flow, address);
CREATE INDEX IF NOT EXISTS signin_codes_subject
    ON cloud_meta.signin_codes (subject);

-- The rate limits' counts: a keyed hash of an address, an IP address (IPv6
-- by /64) or a person, per window. Kept up to a day.
CREATE TABLE IF NOT EXISTS cloud_meta.signin_counts (
    key          TEXT        NOT NULL,
    window_start TIMESTAMPTZ NOT NULL,
    n            INTEGER     NOT NULL,
    PRIMARY KEY (key, window_start)
);

-- When a person last signed in with a code: a session made after it isn't
-- a recent sign-in for changes that need one. Kept a quarter of an hour.
CREATE TABLE IF NOT EXISTS cloud_meta.signin_code_uses (
    subject TEXT        PRIMARY KEY,
    at      TIMESTAMPTZ NOT NULL
);
