-- The tenant's agents as first-class records. The id is the
-- one name an agent has everywhere: its cards' holder, its bound keys'
-- agent_id, its guard runtime's agent_id, its decisions' actor_id.
-- Idempotent; applied on every boot.
CREATE TABLE IF NOT EXISTS cloud_meta.agents (
    tenant_id   TEXT        NOT NULL REFERENCES cloud_meta.tenants(id) ON DELETE CASCADE,
    id          TEXT        NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_by  TEXT        NOT NULL DEFAULT '',
    archived_at TIMESTAMPTZ NULL,
    PRIMARY KEY (tenant_id, id)
);

-- What kind of agent it is, for the two meters: 'taf'
-- for an agent the tafs app made (a phone's signed request), whose calls
-- are tafs tasks; '' for the account's own agents, whose calls are KIFF
-- actions.
ALTER TABLE cloud_meta.agents ADD COLUMN IF NOT EXISTS kind TEXT NOT NULL DEFAULT '';

-- The agents made before the mark: every agent a confirmed draft names
-- is a taf's (the drafts path is the tafs app's). Only ever adds the mark.
DO $$
BEGIN
    IF to_regclass('cloud_meta.agent_drafts') IS NOT NULL THEN
        UPDATE cloud_meta.agents a SET kind = 'taf'
         WHERE a.kind = ''
           AND EXISTS (SELECT 1 FROM cloud_meta.agent_drafts d
                        WHERE d.tenant_id = a.tenant_id AND d.agent_id = a.id
                          AND d.confirmed_at IS NOT NULL);
    END IF;
END $$;

-- The name people read an agent by: the label
-- its latest confirmed draft was submitted with ("Refunds 4", "Coach"), so
-- usage and the apps say it instead of the agent's id. '' when it has none.
ALTER TABLE cloud_meta.agents ADD COLUMN IF NOT EXISTS name TEXT NOT NULL DEFAULT '';

-- The agents named before the column: each takes its latest confirmed
-- draft's label. Only fills an empty name.
DO $$
BEGIN
    IF to_regclass('cloud_meta.agent_drafts') IS NOT NULL THEN
        UPDATE cloud_meta.agents a SET name = d.label
          FROM (SELECT DISTINCT ON (tenant_id, agent_id) tenant_id, agent_id, label
                  FROM cloud_meta.agent_drafts
                 WHERE confirmed_at IS NOT NULL AND label <> ''
                 ORDER BY tenant_id, agent_id, confirmed_at DESC) d
         WHERE a.name = '' AND d.tenant_id = a.tenant_id AND d.agent_id = a.id;
    END IF;
END $$;
