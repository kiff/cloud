-- oauth.sql: OAuth on the KIFF gateway.
--
-- KIFF is the authorization server; Clerk signs the person in. A grant
-- is one agent connection: it owns exactly one agent-bound
-- gateway_agent key (api_keys), and every way it ends revokes that key.
-- Tokens and codes are stored as SHA-256 hashes, never in plaintext.
-- All statements are idempotent.

CREATE SCHEMA IF NOT EXISTS cloud_meta;

-- Registered clients (RFC 7591). Public clients (PKCE, no secret).
-- Not per tenant: a client is registered before anyone signs in.
CREATE TABLE IF NOT EXISTS cloud_meta.oauth_clients (
    id             TEXT        PRIMARY KEY,
    name           TEXT        NOT NULL,
    redirect_uris  TEXT[]      NOT NULL,
    created_at     TIMESTAMPTZ NOT NULL
);

-- Authorization requests waiting for the person's consent. Not per
-- tenant: the tenant is known only once someone signs in and consents.
CREATE TABLE IF NOT EXISTS cloud_meta.oauth_requests (
    id              TEXT        PRIMARY KEY,
    client_id       TEXT        NOT NULL,
    redirect_uri    TEXT        NOT NULL,
    state           TEXT        NOT NULL DEFAULT '',
    code_challenge  TEXT        NOT NULL,
    resource        TEXT        NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL,
    expires_at      TIMESTAMPTZ NOT NULL,
    used_at         TIMESTAMPTZ
);

-- One agent connection. It is pending until its code is exchanged:
-- only then is its key minted (key_id/key_hash name that api_keys row)
-- and redeemed_at set. A pending grant has no key and is not a
-- connection; a failed exchange ends it.
CREATE TABLE IF NOT EXISTS cloud_meta.oauth_grants (
    id             TEXT        PRIMARY KEY,
    tenant_id      TEXT        NOT NULL,
    agent_id       TEXT        NOT NULL,
    key_id         TEXT        NOT NULL DEFAULT '',
    key_hash       BYTEA,
    redeemed_at    TIMESTAMPTZ,
    client_id      TEXT        NOT NULL,
    client_name    TEXT        NOT NULL,
    granted_by     TEXT        NOT NULL,
    resource       TEXT        NOT NULL,
    created_at     TIMESTAMPTZ NOT NULL,
    last_used_at   TIMESTAMPTZ NOT NULL,
    ended_at       TIMESTAMPTZ,
    ended_reason   TEXT        NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS oauth_grants_client_idx
    ON cloud_meta.oauth_grants (tenant_id, client_name, created_at DESC);

-- Single-use authorization codes (60 seconds).
CREATE TABLE IF NOT EXISTS cloud_meta.oauth_codes (
    hash            BYTEA       PRIMARY KEY,
    tenant_id       TEXT        NOT NULL,
    grant_id        TEXT        NOT NULL,
    client_id       TEXT        NOT NULL,
    redirect_uri    TEXT        NOT NULL,
    code_challenge  TEXT        NOT NULL,
    expires_at      TIMESTAMPTZ NOT NULL,
    used_at         TIMESTAMPTZ
);

-- Access (1 hour) and refresh (rotated on every use) tokens.
CREATE TABLE IF NOT EXISTS cloud_meta.oauth_tokens (
    hash        BYTEA       PRIMARY KEY,
    tenant_id   TEXT        NOT NULL,
    grant_id    TEXT        NOT NULL,
    kind        TEXT        NOT NULL CHECK (kind IN ('access', 'refresh')),
    expires_at  TIMESTAMPTZ,
    used_at     TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS oauth_tokens_grant_idx ON cloud_meta.oauth_tokens (grant_id);

-- Connect links. A link names one agent; its URL
-- (https://mcp.kiff.dev/a/<id>/mcp) is the OAuth resource, so the agent
-- is chosen before consent. Not a secret: using it still needs an admin
-- or editor of the account to sign in and approve.
CREATE TABLE IF NOT EXISTS cloud_meta.oauth_connect_links (
    id          TEXT        PRIMARY KEY,
    tenant_id   TEXT        NOT NULL,
    agent_id    TEXT        NOT NULL,
    created_by  TEXT        NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL,
    revoked_at  TIMESTAMPTZ,
    revoked_by  TEXT        NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS oauth_connect_links_agent_idx
    ON cloud_meta.oauth_connect_links (tenant_id, agent_id);

-- Requests and grants keep the link itself, not only its URL, so a
-- revoked link is caught at every step (Amendment 1, A4).
ALTER TABLE cloud_meta.oauth_requests ADD COLUMN IF NOT EXISTS link_id TEXT NOT NULL DEFAULT '';
ALTER TABLE cloud_meta.oauth_grants   ADD COLUMN IF NOT EXISTS link_id TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS oauth_grants_link_idx ON cloud_meta.oauth_grants (link_id) WHERE link_id <> '';

-- How a connection was made: "sign-in" (authorization code)
-- or "code" (device flow). Shown on the Connections view (D2c).
ALTER TABLE cloud_meta.oauth_grants ADD COLUMN IF NOT EXISTS method TEXT NOT NULL DEFAULT 'sign-in';

-- Device authorization (RFC 8628) for clients that cannot
-- receive a redirect. Its own endpoint and table: a device code started
-- here can only ever be redeemed for an agent credential, never a
-- developer session (the developer sign-in's flow is separate). status: pending →
-- claimed (an admin or editor is approving) → approved (grant_id set) →
-- redeemed; or denied; or failed (approval failed after the claim).
CREATE TABLE IF NOT EXISTS cloud_meta.oauth_device_codes (
    hash          BYTEA       PRIMARY KEY,
    user_code     TEXT        NOT NULL UNIQUE,
    client_id     TEXT        NOT NULL,
    resource      TEXT        NOT NULL,
    link_id       TEXT        NOT NULL DEFAULT '',
    status        TEXT        NOT NULL,
    tenant_id     TEXT        NOT NULL DEFAULT '',
    grant_id      TEXT        NOT NULL DEFAULT '',
    created_at    TIMESTAMPTZ NOT NULL,
    expires_at    TIMESTAMPTZ NOT NULL,
    last_poll_at  TIMESTAMPTZ
);

-- The retention purge selects ended grants by ended_at.
CREATE INDEX IF NOT EXISTS oauth_grants_ended_idx
    ON cloud_meta.oauth_grants (ended_at) WHERE ended_at IS NOT NULL;
