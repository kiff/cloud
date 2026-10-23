-- api_keys.sql
--
-- Cloud-meta table for API keys. One row per issued
-- key. The plaintext key is NEVER stored — only the SHA-256 hash.
-- Lookups go: parsed plaintext -> hash -> row -> tenant + actor.
--
-- This file is the source of truth. ApplyAPIKeysSchema embeds and
-- runs it at API startup. All statements are idempotent.

CREATE TABLE IF NOT EXISTS cloud_meta.api_keys (
    -- Stable identifier for the key, used by the public CRUD
    -- surface (POST /v1/keys returns it; DELETE /v1/keys/{id}
    -- references it). Generated as a ULID at creation time.
    id              TEXT        PRIMARY KEY,

    -- Tenant the key is scoped to. Foreign key to cloud_meta.tenants
    -- so revoking a tenant cascades to its keys.
    tenant_id       TEXT        NOT NULL
        REFERENCES cloud_meta.tenants(id) ON DELETE CASCADE,

    -- Subject that the resolver maps the key to. For machine
    -- callers this is typically the actor id; for humans creating
    -- a key for themselves it is the Clerk user id. The auth
    -- middleware stamps it onto Identity.Subject on every request.
    subject         TEXT        NOT NULL,

    -- Optional human-readable label, set at creation time. Useful
    -- in the dashboard ("Production deploy", "Looker integration"). 
    label           TEXT        NOT NULL DEFAULT '',

    -- Cloud-side roles attached to the key. Stored as a JSONB
    -- array of strings to keep the schema flat without committing
    -- to a per-role join table.
    roles           JSONB       NOT NULL DEFAULT '[]'::JSONB,

    -- Optional domain binding. Empty string means
    -- the key is tenant-wide (the default, and every key issued before
    -- multi-domain). A non-empty value binds the key to one domain_id:
    -- the decide path treats that binding as an isolation boundary —
    -- a request naming a different domain is refused, not redirected.
    -- The key string (kiff_live_<tenant>_<random32>) is unchanged; the
    -- binding is server-side metadata only.
    domain_id       TEXT        NOT NULL DEFAULT '',

    -- Optional agent binding: the card holder this key speaks for.
    -- Empty string means unbound (the default, and every key issued
    -- before binding). A non-empty value is an opaque agent id matched
    -- against the proposal's actor_id: a bound key may only act as
    -- that agent, and an action that requires a card refuses any key
    -- not bound to the card's holder. Server-side metadata only.
    agent_id        TEXT        NOT NULL DEFAULT '',

    -- SHA-256 of the plaintext key, lowercase hex (64 chars).
    -- Lookups use this column. Constant-time comparison happens
    -- in Go after the row is fetched.
    key_hash        TEXT        NOT NULL,

    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_used_at    TIMESTAMPTZ NULL,
    revoked_at      TIMESTAMPTZ NULL,

    CONSTRAINT api_keys_hash_key UNIQUE (key_hash)
);

CREATE INDEX IF NOT EXISTS api_keys_tenant_idx
    ON cloud_meta.api_keys (tenant_id);

CREATE INDEX IF NOT EXISTS api_keys_active_idx
    ON cloud_meta.api_keys (tenant_id) WHERE revoked_at IS NULL;

-- Optional per-domain key binding. Added
-- idempotently for databases created before multi-domain; existing
-- keys default to '' (tenant-wide), so no key changes behaviour.
ALTER TABLE cloud_meta.api_keys
    ADD COLUMN IF NOT EXISTS domain_id TEXT NOT NULL DEFAULT '';

-- Optional agent binding (card-holder identity). Added idempotently
-- for databases created before it; existing keys default to ''
-- (unbound), which keeps them working for actions that need no card.
ALTER TABLE cloud_meta.api_keys
    ADD COLUMN IF NOT EXISTS agent_id TEXT NOT NULL DEFAULT '';

-- Optional environment binding. Added idempotently; existing keys
-- default to '' (any environment), so no key changes behaviour.
ALTER TABLE cloud_meta.api_keys
    ADD COLUMN IF NOT EXISTS environment TEXT NOT NULL DEFAULT '';
