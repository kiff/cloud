-- One row per gateway call once it has ended, classified
-- once: whether it counts on the tafs tasks or KIFF actions meter, and if
-- not, why it's free. The gateway's own record is pruned and loses its
-- results, so the meters, the limits and the Stripe reporter read this.
-- Idempotent; applied on every boot.
CREATE TABLE IF NOT EXISTS cloud_meta.billable_calls (
    tenant_id   TEXT        NOT NULL REFERENCES cloud_meta.tenants(id) ON DELETE CASCADE,
    key         TEXT        NOT NULL, -- the call's key: its proposal id, and its Record id
    agent_id    TEXT        NOT NULL,
    tool        TEXT        NOT NULL,
    unit        TEXT        NOT NULL, -- 'task' or 'action'
    counted     BOOLEAN     NOT NULL,
    free        TEXT        NOT NULL DEFAULT '', -- why it's free; '' when counted
    at          TIMESTAMPTZ NOT NULL, -- when it ended
    reported_at TIMESTAMPTZ NULL,     -- when Stripe was told of it, for overage
    PRIMARY KEY (tenant_id, key)
);

-- The tool's title for people, as the gateway had it when the call was
-- recorded: a taf's tools are disconnected later (a
-- removed taf, an Eval's cleanup), and their gateway rows go with them, so
-- the title is kept with the call. '' when the tool had none.
ALTER TABLE cloud_meta.billable_calls ADD COLUMN IF NOT EXISTS tool_title TEXT NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS billable_calls_period
    ON cloud_meta.billable_calls (tenant_id, unit, at);

-- Who asked for each call: the person whose run made it,
-- as KIFF checked their run grant when it decided the call. Written at the
-- decision, read when the call is classified, so usage reads by person.
-- A cost and fair-use figure, never a view of how people work (decision 10).
-- It also keeps a person's reading to their own calls (decision 12).
CREATE TABLE IF NOT EXISTS cloud_meta.call_requesters (
    tenant_id TEXT        NOT NULL REFERENCES cloud_meta.tenants(id) ON DELETE CASCADE,
    key       TEXT        NOT NULL, -- the call's proposal id, as billable_calls.key
    asked_by  TEXT        NOT NULL, -- the person's subject
    at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (tenant_id, key)
);
CREATE INDEX IF NOT EXISTS call_requesters_asked_by ON cloud_meta.call_requesters (tenant_id, asked_by);

ALTER TABLE cloud_meta.billable_calls ADD COLUMN IF NOT EXISTS asked_by TEXT NOT NULL DEFAULT '';

-- One row per run of a taf or of Setup (Coach) that ended having used AI,
-- as its runtime reported it once, with the run's agent's key: what the run
-- cost the model, and its credits, worked out once when the row is kept.
-- The runtime's own run records go with its agents' keep rules, so the
-- credits read this. A run's first report is kept; one sent again changes
-- nothing.
CREATE TABLE IF NOT EXISTS cloud_meta.billable_runs (
    tenant_id      TEXT        NOT NULL REFERENCES cloud_meta.tenants(id) ON DELETE CASCADE,
    run_id         TEXT        NOT NULL, -- the run's id in its runtime
    agent_id       TEXT        NOT NULL, -- KIFF's agent: the reporting key's
    runtime_agent  TEXT        NOT NULL DEFAULT '', -- the run's agent in its runtime (a taf's id in tafs)
    setup          BOOLEAN     NOT NULL DEFAULT FALSE, -- reported with Setup's key: a run of Coach's
    trigger        TEXT        NOT NULL DEFAULT '', -- what started it: owner, schedule, event, trial, eval
    source         TEXT        NOT NULL DEFAULT '', -- what within its trigger: a schedule, an event, a door
    requester_kind TEXT        NOT NULL DEFAULT '', -- member, door, manager or agent; '' for none
    asked_by       TEXT        NOT NULL DEFAULT '', -- the person whose run it was, a member's or manager's subject
    started_at     TIMESTAMPTZ NOT NULL,
    ended_at       TIMESTAMPTZ NOT NULL,
    cost_micro_usd BIGINT      NOT NULL, -- what its model turns cost, millionths of a US dollar
    turns          INTEGER     NOT NULL DEFAULT 0,
    status         TEXT        NOT NULL DEFAULT '', -- COMPLETED, ERROR or CANCELLED
    stop           TEXT        NOT NULL DEFAULT '',
    failed         BOOLEAN     NOT NULL DEFAULT FALSE, -- it failed on the runtime's side
    refused        INTEGER     NOT NULL DEFAULT 0, -- its calls KIFF refused outright
    declined       INTEGER     NOT NULL DEFAULT 0, -- its calls held, then refused by the owner
    kept_until     TIMESTAMPTZ NULL,     -- when the runtime may delete the run; NULL, kept
    counted        BOOLEAN     NOT NULL,
    free           TEXT        NOT NULL DEFAULT '', -- why it's free: setup, trial or failed; '' when counted
    credits        BIGINT      NOT NULL DEFAULT 0,  -- 0 when free
    recorded_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (tenant_id, run_id)
);

CREATE INDEX IF NOT EXISTS billable_runs_period
    ON cloud_meta.billable_runs (tenant_id, ended_at);

-- How each run stood against the balance: no_plan, it ended while the
-- account had no plan (Setup's runs then are free, within an allowance
-- KIFF pays for); settled_at, when its credits were taken from the
-- balance (on Solo: the included credits, then the packs); over_credits,
-- what of them was past Solo's balance, which nothing pays for.
ALTER TABLE cloud_meta.billable_runs ADD COLUMN IF NOT EXISTS no_plan BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE cloud_meta.billable_runs ADD COLUMN IF NOT EXISTS settled_at TIMESTAMPTZ NULL;
ALTER TABLE cloud_meta.billable_runs ADD COLUMN IF NOT EXISTS over_credits BIGINT NOT NULL DEFAULT 0;

CREATE INDEX IF NOT EXISTS billable_runs_unsettled
    ON cloud_meta.billable_runs (tenant_id, ended_at) WHERE counted AND settled_at IS NULL;
