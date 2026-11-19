-- support_tickets.sql
--
-- Private support tickets. A registered tenant files
-- a ticket (title + opening body); operators answer from /ops; both
-- sides exchange replies on support_ticket_messages. Tickets are never
-- public — visible only to the submitting tenant and to operators.
--
-- Idempotent: call ApplySchema once at startup after
-- tenant.ApplyCloudMeta (the tenant_id foreign key needs
-- cloud_meta.tenants).

CREATE TABLE IF NOT EXISTS cloud_meta.support_tickets (
    id             TEXT        NOT NULL,
    tenant_id      TEXT        NOT NULL REFERENCES cloud_meta.tenants(id) ON DELETE CASCADE,
    author_subject TEXT        NOT NULL DEFAULT '',
    author_email   TEXT        NOT NULL DEFAULT '',
    title          TEXT        NOT NULL,
    body           TEXT        NOT NULL,
    category       TEXT        NOT NULL DEFAULT 'general',
    status         TEXT        NOT NULL DEFAULT 'open'
                               CHECK (status IN ('open', 'in_progress', 'answered', 'closed')),
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    PRIMARY KEY (id)
);

CREATE INDEX IF NOT EXISTS support_tickets_tenant_idx
    ON cloud_meta.support_tickets (tenant_id, updated_at DESC);

-- Operator triage list, filterable by status, newest activity first.
CREATE INDEX IF NOT EXISTS support_tickets_status_idx
    ON cloud_meta.support_tickets (status, updated_at DESC);

CREATE TABLE IF NOT EXISTS cloud_meta.support_ticket_messages (
    id             TEXT        NOT NULL,
    ticket_id      TEXT        NOT NULL REFERENCES cloud_meta.support_tickets(id) ON DELETE CASCADE,
    author_kind    TEXT        NOT NULL CHECK (author_kind IN ('user', 'operator')),
    author_subject TEXT        NOT NULL DEFAULT '',
    body           TEXT        NOT NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    PRIMARY KEY (id)
);

CREATE INDEX IF NOT EXISTS support_messages_ticket_idx
    ON cloud_meta.support_ticket_messages (ticket_id, created_at ASC);
