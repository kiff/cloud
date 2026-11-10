-- thune: KIFF's money layer for refunds (thune step one).
-- Its own tables, in the gateway schema the kiff_gateway role owns
-- (internal/dbrole), reached only through internal/thune's functions.
-- apps/api reads gateway.thune_adjustments for the room a failed refund
-- drew and the owner's notices, and never writes them. Every change is a conditional
-- update, so a repeated step changes nothing.

-- The claim: one business adjustment is handled once, across tafs and
-- across connections to one processor account. The first taf to insert
-- the identity holds it; a released claim (its call ended not_sent) can be
-- taken again, by an update that only matches a released row.
CREATE TABLE IF NOT EXISTS gateway.thune_claims (
    tenant_id   text NOT NULL,
    account     text NOT NULL,
    object      text NOT NULL,
    kind        text NOT NULL,
    request     text NOT NULL,
    holder      text NOT NULL,
    taken_at    timestamptz NOT NULL,
    released_at timestamptz,
    PRIMARY KEY (tenant_id, account, object, kind, request)
);

-- The charge lock: one charge's refunds run one at a time, so two cases
-- can't both read the same remainder. A lease, so a crashed holder frees
-- it once until has passed.
CREATE TABLE IF NOT EXISTS gateway.thune_charge_locks (
    tenant_id text NOT NULL,
    account   text NOT NULL,
    object    text NOT NULL,
    holder    text NOT NULL,
    until     timestamptz NOT NULL,
    PRIMARY KEY (tenant_id, account, object)
);

-- One adjustment per gateway call (key is the invocation key, which is
-- also the marker written on the refund). Two statuses, never mixed:
-- execution is KIFF's call, financial is the money at the processor.
CREATE TABLE IF NOT EXISTS gateway.thune_adjustments (
    key            text PRIMARY KEY,
    tenant_id      text NOT NULL,
    account        text NOT NULL,
    object         text NOT NULL,
    kind           text NOT NULL,
    request        text NOT NULL DEFAULT '',
    requested      bigint NOT NULL DEFAULT 0,
    remainder      boolean NOT NULL DEFAULT false,
    authorized     bigint NOT NULL DEFAULT 0,
    currency       text NOT NULL DEFAULT '',
    facts          jsonb NOT NULL,
    facts_read_at  timestamptz NOT NULL,
    verdict        text NOT NULL,
    reasons        text[] NOT NULL DEFAULT '{}',
    execution      text NOT NULL,
    financial      text NOT NULL DEFAULT 'none',
    refund_id      text NOT NULL DEFAULT '',
    watchable      boolean NOT NULL DEFAULT true,
    created_at     timestamptz NOT NULL,
    updated_at     timestamptz NOT NULL
);
CREATE INDEX IF NOT EXISTS thune_adjustments_charge ON gateway.thune_adjustments (tenant_id, account, object);

-- Every refund seen on a charge, KIFF's and others'. A refund with no
-- marker of ours is "refunded outside KIFF"; it counts in every later
-- case's remainder, because the processor's facts carry it.
CREATE TABLE IF NOT EXISTS gateway.thune_refunds (
    tenant_id  text NOT NULL,
    account    text NOT NULL,
    refund_id  text NOT NULL,
    object     text NOT NULL,
    amount     bigint NOT NULL,
    status     text NOT NULL,
    marker     text NOT NULL DEFAULT '',
    origin     text NOT NULL,
    seen_at    timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    PRIMARY KEY (tenant_id, account, refund_id)
);
CREATE INDEX IF NOT EXISTS thune_refunds_charge ON gateway.thune_refunds (tenant_id, account, object);

-- Processor events, kept by their id so a redelivery changes nothing.
CREATE TABLE IF NOT EXISTS gateway.thune_events (
    tenant_id   text NOT NULL,
    event_id    text NOT NULL,
    received_at timestamptz NOT NULL,
    PRIMARY KEY (tenant_id, event_id)
);

-- The watch's work: one row per adjustment still to follow, taken with a
-- lock that skips taken rows, with attempts and back-off.
CREATE TABLE IF NOT EXISTS gateway.thune_watch_work (
    key          text PRIMARY KEY REFERENCES gateway.thune_adjustments(key) ON DELETE CASCADE,
    tenant_id    text NOT NULL,
    available_at timestamptz NOT NULL,
    attempts     integer NOT NULL DEFAULT 0,
    last_error   text NOT NULL DEFAULT '',
    completed_at timestamptz
);
CREATE INDEX IF NOT EXISTS thune_watch_work_due ON gateway.thune_watch_work (available_at) WHERE completed_at IS NULL;
