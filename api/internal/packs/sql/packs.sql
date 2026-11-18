-- Task packs: the one-time top-ups of tafs tasks, and what each has left.
-- Idempotent; applied on every boot, after cloud_meta.tenants.

-- One row per pack an account got: bought through Stripe, Apple or Google
-- Play, or a
-- bonus KIFF granted. A pack is recorded once per store transaction.
CREATE TABLE IF NOT EXISTS cloud_meta.task_packs (
    id          TEXT        PRIMARY KEY,
    tenant_id   TEXT        NOT NULL REFERENCES cloud_meta.tenants(id) ON DELETE CASCADE,
    pack        TEXT        NOT NULL, -- the pack's id in the pack table, or 'bonus'
    tasks       INTEGER     NOT NULL CHECK (tasks > 0),
    tasks_left  INTEGER     NOT NULL CHECK (tasks_left >= 0 AND tasks_left <= tasks),
    bought_at   TIMESTAMPTZ NOT NULL,
    expires_at  TIMESTAMPTZ NOT NULL,
    kind        TEXT        NOT NULL CHECK (kind IN ('bought', 'bonus')),
    source      TEXT        NOT NULL CHECK (source IN ('stripe', 'apple', 'google', 'kiff')),
    source_txn  TEXT        NOT NULL, -- the store's transaction id; KIFF's grant id for a bonus
    request_id  TEXT        NULL,     -- the member's request it answered, if any
    revoked_at  TIMESTAMPTZ NULL,     -- refunded or revoked: its tasks are taken back
    UNIQUE (source, source_txn)
);

CREATE INDEX IF NOT EXISTS task_packs_tenant
    ON cloud_meta.task_packs (tenant_id, bought_at);

-- A pack's payment taken back (refunded in full, or a dispute lost)
-- before the pack was recorded: the pack is recorded taken back when its
-- payment arrives, and the row removed.
CREATE TABLE IF NOT EXISTS cloud_meta.task_pack_takebacks (
    source     TEXT        NOT NULL CHECK (source IN ('stripe', 'apple', 'google', 'kiff')),
    source_txn TEXT        NOT NULL,
    taken_at   TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (source, source_txn)
);

-- Google Play's packs (source 'google'), on a database made before them:
-- each source check is widened once, in place.
DO $$
DECLARE
    t TEXT;
BEGIN
    FOREACH t IN ARRAY ARRAY['task_packs', 'task_pack_takebacks'] LOOP
        IF NOT EXISTS (
            SELECT 1 FROM pg_constraint c
            JOIN pg_class r ON r.oid = c.conrelid
            JOIN pg_namespace n ON n.oid = r.relnamespace
            WHERE n.nspname = 'cloud_meta' AND r.relname = t AND c.conname = t || '_source_check'
              AND pg_get_constraintdef(c.oid) LIKE '%google%'
        ) THEN
            EXECUTE format('ALTER TABLE cloud_meta.%I DROP CONSTRAINT IF EXISTS %I', t, t || '_source_check');
            EXECUTE format('ALTER TABLE cloud_meta.%I ADD CONSTRAINT %I CHECK (source IN (''stripe'', ''apple'', ''google'', ''kiff''))', t, t || '_source_check');
        END IF;
    END LOOP;
END
$$;

-- Each counted task past what the plan included, and the pack it was
-- drawn from. A task is drawn once.
CREATE TABLE IF NOT EXISTS cloud_meta.task_pack_draws (
    tenant_id TEXT        NOT NULL REFERENCES cloud_meta.tenants(id) ON DELETE CASCADE,
    call_key  TEXT        NOT NULL, -- the call's key on cloud_meta.billable_calls
    pack_id   TEXT        NOT NULL REFERENCES cloud_meta.task_packs(id) ON DELETE CASCADE,
    call_at   TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (tenant_id, call_key)
);

CREATE INDEX IF NOT EXISTS task_pack_draws_period
    ON cloud_meta.task_pack_draws (tenant_id, call_at);

-- A member's request that an admin add a pack. The first answer counts:
-- approved (a pack bought with the request's id), declined, withdrawn by
-- the member, or expired at the period's end.
CREATE TABLE IF NOT EXISTS cloud_meta.task_pack_requests (
    id          TEXT        PRIMARY KEY,
    tenant_id   TEXT        NOT NULL REFERENCES cloud_meta.tenants(id) ON DELETE CASCADE,
    asked_by    TEXT        NOT NULL, -- the member's subject
    pack        TEXT        NOT NULL,
    reason      TEXT        NOT NULL DEFAULT '',
    status      TEXT        NOT NULL CHECK (status IN ('open', 'approved', 'declined', 'withdrawn', 'expired')),
    created_at  TIMESTAMPTZ NOT NULL,
    expires_at  TIMESTAMPTZ NOT NULL,
    answered_by TEXT        NULL,
    answered_at TIMESTAMPTZ NULL,
    note        TEXT        NOT NULL DEFAULT '',
    pack_id     TEXT        NULL -- the pack bought when approved
);

-- One open request per member at a time.
CREATE UNIQUE INDEX IF NOT EXISTS task_pack_requests_one_open
    ON cloud_meta.task_pack_requests (tenant_id, asked_by) WHERE status = 'open';

CREATE INDEX IF NOT EXISTS task_pack_requests_tenant
    ON cloud_meta.task_pack_requests (tenant_id, created_at);

-- Packs of credits: what Solo's tafs use past the plan's included
-- credits. A pack never expires. One row per pack, recorded once per
-- store transaction, or per KIFF grant for a bonus.
CREATE TABLE IF NOT EXISTS cloud_meta.credit_packs (
    id           TEXT        PRIMARY KEY,
    tenant_id    TEXT        NOT NULL REFERENCES cloud_meta.tenants(id) ON DELETE CASCADE,
    pack         TEXT        NOT NULL, -- the pack's id in the pack table, or 'bonus'
    credits      BIGINT      NOT NULL CHECK (credits > 0),
    credits_left BIGINT      NOT NULL CHECK (credits_left >= 0 AND credits_left <= credits),
    bought_at    TIMESTAMPTZ NOT NULL,
    kind         TEXT        NOT NULL CHECK (kind IN ('bought', 'bonus')),
    source       TEXT        NOT NULL CHECK (source IN ('stripe', 'apple', 'google', 'kiff')),
    source_txn   TEXT        NOT NULL, -- the store's transaction id; KIFF's grant id for a bonus
    request_id   TEXT        NULL,     -- the member's request it answered, if any
    revoked_at   TIMESTAMPTZ NULL,     -- refunded or revoked: what it had left is taken back
    UNIQUE (source, source_txn)
);

CREATE INDEX IF NOT EXISTS credit_packs_tenant
    ON cloud_meta.credit_packs (tenant_id, bought_at);

-- What each run took from a pack, past the plan's included credits. A
-- run is settled once (billable_runs.settled_at), so it draws once.
CREATE TABLE IF NOT EXISTS cloud_meta.credit_pack_draws (
    tenant_id TEXT        NOT NULL REFERENCES cloud_meta.tenants(id) ON DELETE CASCADE,
    run_id    TEXT        NOT NULL, -- the run's id on cloud_meta.billable_runs
    pack_id   TEXT        NOT NULL REFERENCES cloud_meta.credit_packs(id) ON DELETE CASCADE,
    credits   BIGINT      NOT NULL CHECK (credits > 0),
    at        TIMESTAMPTZ NOT NULL, -- when the run ended: the period it's drawn for
    PRIMARY KEY (tenant_id, run_id, pack_id)
);

CREATE INDEX IF NOT EXISTS credit_pack_draws_period
    ON cloud_meta.credit_pack_draws (tenant_id, at);

-- A request's kind: a pack (Solo), or a raise of the spend limit
-- (Company), which has no pack.
ALTER TABLE cloud_meta.task_pack_requests ADD COLUMN IF NOT EXISTS kind TEXT NOT NULL DEFAULT 'pack'
    CHECK (kind IN ('pack', 'raise'));
