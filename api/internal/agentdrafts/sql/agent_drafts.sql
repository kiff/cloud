-- agent_drafts: a draft of a new agent and its Cards. It
-- grants nothing until an owner confirms it; its key is minted only when
-- the holder of the draft's claim secret collects it, once. Idempotent.
CREATE TABLE IF NOT EXISTS cloud_meta.agent_drafts (
    id            TEXT        PRIMARY KEY,
    tenant_id     TEXT        NOT NULL REFERENCES cloud_meta.tenants(id) ON DELETE CASCADE,
    agent_id      TEXT        NOT NULL,
    label         TEXT        NOT NULL DEFAULT '',
    cards         JSONB       NOT NULL,
    version       TEXT        NOT NULL,
    status        TEXT        NOT NULL,
    created_by    TEXT        NOT NULL DEFAULT '',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at    TIMESTAMPTZ NOT NULL,
    confirmed_by  TEXT        NOT NULL DEFAULT '',
    confirmed_role TEXT       NOT NULL DEFAULT '',
    confirmed_at  TIMESTAMPTZ NULL,
    card_ids      JSONB       NOT NULL DEFAULT '[]'::JSONB,
    claim_hash    TEXT        NOT NULL,
    claimed_at    TIMESTAMPTZ NULL,
    key_id        TEXT        NOT NULL DEFAULT '',
    reason        TEXT        NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS agent_drafts_tenant_idx ON cloud_meta.agent_drafts (tenant_id, created_at DESC);

-- The drafts a sweep must finish: rolling back, discarding, or confirming.
CREATE INDEX IF NOT EXISTS agent_drafts_unsettled_idx ON cloud_meta.agent_drafts (status)
    WHERE status IN ('confirming', 'rolling_back', 'discarding');

-- A revision draft (kiff/tafs#107): for an agent that already holds Cards.
-- Its Cards revise or add to the agent's, it mints no key, and revises
-- lists the Cards it revises with the revision each was drafted from.
ALTER TABLE cloud_meta.agent_drafts ADD COLUMN IF NOT EXISTS revision BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE cloud_meta.agent_drafts ADD COLUMN IF NOT EXISTS revises JSONB NOT NULL DEFAULT '[]'::JSONB;

-- The agent's Cards a revision draft revokes on confirmation, with the
-- revision each was drafted from: going back to a version that didn't hold
-- them (kiff/tafs#107).
ALTER TABLE cloud_meta.agent_drafts ADD COLUMN IF NOT EXISTS retires JSONB NOT NULL DEFAULT '[]'::JSONB;
-- Whether it is a restore, which retires the agent's other Cards: retires
-- can be empty, and a Card issued before confirmation must still be caught.
ALTER TABLE cloud_meta.agent_drafts ADD COLUMN IF NOT EXISTS retire_rest BOOLEAN NOT NULL DEFAULT false;

-- A draft's latest trial (kiff/tafs#173): its trial Cards, when it started,
-- when they lapse, and how it ended. NULL when it was never tried.
ALTER TABLE cloud_meta.agent_drafts ADD COLUMN IF NOT EXISTS trial JSONB NULL;
CREATE INDEX IF NOT EXISTS agent_drafts_trying_idx ON cloud_meta.agent_drafts (tenant_id, agent_id)
    WHERE status = 'trying';

-- The held call a draft was made for ("Allow this from now on"): the
-- exception's id; '' for every other draft.
ALTER TABLE cloud_meta.agent_drafts ADD COLUMN IF NOT EXISTS from_hold TEXT NOT NULL DEFAULT '';
