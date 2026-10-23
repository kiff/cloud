-- memberships.sql — tenant memberships and governance roles.
--
-- ApplySchema() embeds and runs this at API startup, after
-- ApplyCloudMeta and ApplySubjectTenantsSchema. Every statement is
-- idempotent.

CREATE TABLE IF NOT EXISTS cloud_meta.tenant_memberships (
    id            TEXT        PRIMARY KEY,
    tenant_id     TEXT        NOT NULL REFERENCES cloud_meta.tenants (id),
    subject       TEXT        NOT NULL,          -- OIDC sub (Clerk today)
    role          TEXT        NOT NULL CHECK (role IN ('admin','editor','approver','viewer')),
    status        TEXT        NOT NULL CHECK (status IN ('active','suspended','removed')),
    invited_by    TEXT,                          -- subject; NULL for the founding admin
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    removed_at    TIMESTAMPTZ,
    version       BIGINT      NOT NULL DEFAULT 1  -- bumped on every write
);

-- One live membership per person per tenant. A removed row stays as
-- history, and a person invited back gets a new row next to it.
CREATE UNIQUE INDEX IF NOT EXISTS tenant_memberships_live_uq
    ON cloud_meta.tenant_memberships (tenant_id, subject)
    WHERE status <> 'removed';

-- Session resolution reads every live membership of a subject.
CREATE INDEX IF NOT EXISTS tenant_memberships_subject_idx
    ON cloud_meta.tenant_memberships (subject);

CREATE TABLE IF NOT EXISTS cloud_meta.tenant_invites (
    id            TEXT        PRIMARY KEY,
    tenant_id     TEXT        NOT NULL REFERENCES cloud_meta.tenants (id),
    email         TEXT        NOT NULL,          -- where the link was sent; not an authority
    role          TEXT        NOT NULL CHECK (role IN ('admin','editor','approver','viewer')),
    invited_by    TEXT        NOT NULL,
    token_hash    BYTEA       NOT NULL UNIQUE,   -- SHA-256 of the token; the token is never stored
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at    TIMESTAMPTZ NOT NULL,
    accepted_at   TIMESTAMPTZ,
    accepted_by   TEXT,                          -- subject that accepted
    revoked_at    TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS tenant_invites_tenant_idx
    ON cloud_meta.tenant_invites (tenant_id, created_at DESC);

CREATE TABLE IF NOT EXISTS cloud_meta.tenant_governance (
    tenant_id              TEXT        PRIMARY KEY REFERENCES cloud_meta.tenants (id),
    separation_of_duties   BOOLEAN     NOT NULL DEFAULT FALSE,
    updated_by             TEXT,
    updated_at             TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Append-only: the application writes INSERT and reads SELECT, never
-- UPDATE or DELETE.
CREATE TABLE IF NOT EXISTS cloud_meta.membership_audit (
    id            TEXT        PRIMARY KEY,
    tenant_id     TEXT        NOT NULL,
    actor         TEXT        NOT NULL,          -- subject that did it
    action        TEXT        NOT NULL,
    target        TEXT,                          -- subject, invite id, or NULL for policy
    old_value     TEXT,
    new_value     TEXT,
    request_id    TEXT,
    auth_method   TEXT,                          -- session | developer
    reauth_age_s  INTEGER,                       -- step-up age when the action required it
    at            TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS membership_audit_tenant_idx
    ON cloud_meta.membership_audit (tenant_id, at DESC);

-- Who answered each hold, with their role and the policy at the time,
-- so a statement can say "approved by an approver" or "approved by an
-- editor, separation of duties off". Written once per answer.
CREATE TABLE IF NOT EXISTS cloud_meta.hold_answers (
    tenant_id             TEXT        NOT NULL,
    hold_id               TEXT        NOT NULL,
    answer                TEXT        NOT NULL,
    answerer              TEXT        NOT NULL,
    role                  TEXT,                  -- NULL when a key answered
    separation_of_duties  BOOLEAN     NOT NULL,
    at                    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (tenant_id, hold_id)
);

-- The phone's signed proof of an answer: device, key, the message
-- KIFF verified and the signature. NULL when answered without a phone.
ALTER TABLE cloud_meta.hold_answers ADD COLUMN IF NOT EXISTS device_proof JSONB NULL;
