-- access.sql: who may use, oversee or manage an agent.
--
-- Groups of an account's people, each with managers and members, and
-- grants of an agent to a group or to one person. Being on the account
-- alone gives no use of any agent. Idempotent; applied on every boot,
-- after the memberships and agents schemas.

CREATE TABLE IF NOT EXISTS cloud_meta.access_groups (
    tenant_id   TEXT        NOT NULL REFERENCES cloud_meta.tenants (id) ON DELETE CASCADE,
    id          TEXT        NOT NULL,
    name        TEXT        NOT NULL,
    -- The group the backfill made from the account's people when groups
    -- arrived (see below). Admins narrow it like any group.
    everyone    BOOLEAN     NOT NULL DEFAULT FALSE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_by  TEXT        NOT NULL DEFAULT '',
    PRIMARY KEY (tenant_id, id)
);

CREATE UNIQUE INDEX IF NOT EXISTS access_groups_name_key
    ON cloud_meta.access_groups (tenant_id, lower(name));

-- Each read of another person's work with an agent: who read whose,
-- which session ('' for the list of their sessions), why, and when.
-- Append only. The person whose work it is lists theirs; an admin lists
-- the account's.
CREATE TABLE IF NOT EXISTS cloud_meta.access_reads (
    tenant_id   TEXT        NOT NULL REFERENCES cloud_meta.tenants (id) ON DELETE CASCADE,
    id          TEXT        NOT NULL,
    reader      TEXT        NOT NULL,
    owner       TEXT        NOT NULL,
    agent_id    TEXT        NOT NULL,
    session_id  TEXT        NOT NULL DEFAULT '',
    purpose     TEXT        NOT NULL CHECK (purpose IN ('follow_up', 'fix', 'concern')),
    note        TEXT        NOT NULL DEFAULT '',
    at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (tenant_id, id)
);

CREATE INDEX IF NOT EXISTS access_reads_owner ON cloud_meta.access_reads (tenant_id, owner, at DESC);
CREATE INDEX IF NOT EXISTS access_reads_agent ON cloud_meta.access_reads (tenant_id, agent_id, at DESC);

CREATE TABLE IF NOT EXISTS cloud_meta.access_group_members (
    tenant_id   TEXT        NOT NULL,
    group_id    TEXT        NOT NULL,
    subject     TEXT        NOT NULL,
    role        TEXT        NOT NULL CHECK (role IN ('manager', 'member')),
    added_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    added_by    TEXT        NOT NULL DEFAULT '',
    PRIMARY KEY (tenant_id, group_id, subject),
    FOREIGN KEY (tenant_id, group_id) REFERENCES cloud_meta.access_groups (tenant_id, id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS access_group_members_subject_idx
    ON cloud_meta.access_group_members (tenant_id, subject);

-- One grant of an agent's use: to a group, or to one person ('' for the
-- other). The agent is its KIFF id, as its Cards and keys name it.
CREATE TABLE IF NOT EXISTS cloud_meta.access_grants (
    tenant_id   TEXT        NOT NULL REFERENCES cloud_meta.tenants (id) ON DELETE CASCADE,
    agent_id    TEXT        NOT NULL,
    group_id    TEXT        NOT NULL DEFAULT '',
    subject     TEXT        NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_by  TEXT        NOT NULL DEFAULT '',
    PRIMARY KEY (tenant_id, agent_id, group_id, subject),
    CHECK ((group_id = '') <> (subject = ''))
);

-- The backfill, once: so that no one loses a taf when
-- grants start to count, each account that exists then gets an
-- "Everyone" group of its active people, granted every taf it has. It
-- runs once for all accounts, and a marker keeps it from running again:
-- an account made later starts with no groups, a taf made later is
-- granted to its creator, and people who join later get what someone
-- shares with them.
-- Invitations pending when the backfill ran: whoever accepts one joins the
-- account's Everyone group, as they would have had they joined before.
-- Each row goes once its invitation is accepted.
CREATE TABLE IF NOT EXISTS cloud_meta.access_backfill_invites (
    tenant_id   TEXT        NOT NULL REFERENCES cloud_meta.tenants (id) ON DELETE CASCADE,
    invite_id   TEXT        NOT NULL,
    PRIMARY KEY (tenant_id, invite_id)
);

CREATE TABLE IF NOT EXISTS cloud_meta.access_backfilled (
    id       BOOLEAN     PRIMARY KEY DEFAULT TRUE CHECK (id),
    removed  INTEGER     NOT NULL DEFAULT 0,
    at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM cloud_meta.access_backfilled) THEN
        RETURN;
    END IF;

    INSERT INTO cloud_meta.access_groups (tenant_id, id, name, everyone, created_by)
    SELECT t.id, 'everyone', 'Everyone', TRUE, 'backfill'
      FROM cloud_meta.tenants t
    ON CONFLICT DO NOTHING;

    INSERT INTO cloud_meta.access_group_members (tenant_id, group_id, subject, role, added_by)
    SELECT m.tenant_id, 'everyone', m.subject, 'member', 'backfill'
      FROM cloud_meta.tenant_memberships m
     WHERE m.status = 'active'
    ON CONFLICT DO NOTHING;

    -- Every taf but the account's Setup (Coach), which is for its admins
    -- by default: KIFF answers that, with no grant (access.ForSetup). Setup
    -- is the agent a live setup_reader key is bound to; a database with no
    -- keys yet has no Setup.
    IF to_regclass('cloud_meta.api_keys') IS NOT NULL THEN
        INSERT INTO cloud_meta.access_grants (tenant_id, agent_id, group_id, created_by)
        SELECT a.tenant_id, a.id, 'everyone', 'backfill'
          FROM cloud_meta.agents a
         WHERE a.kind = 'taf' AND a.archived_at IS NULL
           AND NOT EXISTS (SELECT 1 FROM cloud_meta.api_keys k
                            WHERE k.tenant_id = a.tenant_id AND k.agent_id = a.id
                              AND k.revoked_at IS NULL AND k.roles ? 'setup_reader')
        ON CONFLICT DO NOTHING;
    ELSE
        INSERT INTO cloud_meta.access_grants (tenant_id, agent_id, group_id, created_by)
        SELECT a.tenant_id, a.id, 'everyone', 'backfill'
          FROM cloud_meta.agents a
         WHERE a.kind = 'taf' AND a.archived_at IS NULL
        ON CONFLICT DO NOTHING;
    END IF;

    INSERT INTO cloud_meta.access_backfill_invites (tenant_id, invite_id)
    SELECT i.tenant_id, i.id
      FROM cloud_meta.tenant_invites i
     WHERE i.accepted_at IS NULL AND i.revoked_at IS NULL AND i.expires_at > NOW()
    ON CONFLICT DO NOTHING;

    INSERT INTO cloud_meta.access_backfilled (id) VALUES (TRUE) ON CONFLICT DO NOTHING;
END $$;

-- That grants left on agents archived before archiving removed them
-- (AccessHandler.RemoveAgentGrants) were cleared, once, and how many:
-- ClearArchivedGrants does it at boot, and the API logs the count.
CREATE TABLE IF NOT EXISTS cloud_meta.access_archived_grants_cleared (
    id  BOOLEAN     PRIMARY KEY DEFAULT TRUE CHECK (id),
    at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

