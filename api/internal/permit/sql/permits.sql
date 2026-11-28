-- permits.sql — execution permits.
--
-- One row per operation (the gateway invocation key, sent as the proposal
-- id) that asked for a permit. tool and args_sha256 are bound at the
-- operation's first decision, whatever its outcome, and every later
-- decide request for it must match them (D3). permit_jti is set when the
-- one permit for the operation is minted; it is never minted twice.
-- All statements are idempotent.

CREATE SCHEMA IF NOT EXISTS cloud_meta;

CREATE TABLE IF NOT EXISTS cloud_meta.permit_bindings (
    tenant_id    TEXT        NOT NULL,
    op           TEXT        NOT NULL,
    tool         TEXT        NOT NULL,
    args_sha256  TEXT        NOT NULL,
    audience     TEXT        NOT NULL,
    permit_jti   TEXT,
    minted_at    TIMESTAMPTZ,
    created_at   TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (tenant_id, op)
);

CREATE INDEX IF NOT EXISTS permit_bindings_created ON cloud_meta.permit_bindings (created_at);

-- Where an action's permit goes, set by the account's owner or an admin
-- (domains.manage) when they connect a tool in verify or relay mode, never
-- by the decide request. The decide path signs these, not the
-- tool and audience a caller names, and checks that the governed argument
-- the Card reads is the one in the signed arguments.
-- A target belongs to one domain (the gateway's, for the gateway's
-- connections): a same-named action in another domain, authorized by that
-- domain's Cards, never gets a permit for it.
CREATE TABLE IF NOT EXISTS cloud_meta.permit_targets (
    tenant_id   TEXT        NOT NULL,
    domain      TEXT        NOT NULL,
    action      TEXT        NOT NULL,
    tool        TEXT        NOT NULL,
    audience    TEXT        NOT NULL,
    amount_arg  TEXT        NOT NULL DEFAULT '',
    updated_at  TIMESTAMPTZ NOT NULL,
    updated_by  TEXT        NOT NULL,
    PRIMARY KEY (tenant_id, domain, action)
);

-- The string argument a Card's resource scope reads, bound like the
-- amount: the resource the Card evaluated is the one in the signed arguments.
ALTER TABLE cloud_meta.permit_targets ADD COLUMN IF NOT EXISTS resource_arg TEXT NOT NULL DEFAULT '';

-- A relay tool whose amount the gateway reads from the call's resource
-- (amount_lookup): the amount is decided, not signed.
ALTER TABLE cloud_meta.permit_targets ADD COLUMN IF NOT EXISTS amount_looked_up BOOLEAN NOT NULL DEFAULT false;

-- A tool that takes its amount as a decimal string (connection profiles): the Card
-- counts it in minor units at amount_scale, converted exactly.
ALTER TABLE cloud_meta.permit_targets ADD COLUMN IF NOT EXISTS amount_decimal BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE cloud_meta.permit_targets ADD COLUMN IF NOT EXISTS amount_scale INTEGER NOT NULL DEFAULT 0;

-- Where a relay tool's signed arguments carry the governed values when
-- they're nested ("invoice.total"); '' at the top level.
ALTER TABLE cloud_meta.permit_targets ADD COLUMN IF NOT EXISTS amount_path TEXT NOT NULL DEFAULT '';
ALTER TABLE cloud_meta.permit_targets ADD COLUMN IF NOT EXISTS resource_path TEXT NOT NULL DEFAULT '';
