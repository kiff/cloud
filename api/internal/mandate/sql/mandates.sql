-- Mandates bound what a named subject may do cumulatively, and
-- draws are the ledger of authority consumed against them.

CREATE TABLE IF NOT EXISTS cloud_meta.mandates (
    id           TEXT        NOT NULL,
    tenant_id    TEXT        NOT NULL REFERENCES cloud_meta.tenants(id) ON DELETE CASCADE,
    subject      TEXT        NOT NULL,
    domain       TEXT        NOT NULL DEFAULT '',
    environment  TEXT        NOT NULL DEFAULT '',
    -- Grants and aggregates are small, authored documents read whole on
    -- every evaluation. Columns would buy nothing: nothing queries
    -- inside them, and a schema change per new bound is worse than a
    -- JSONB read.
    grants       JSONB       NOT NULL DEFAULT '[]'::JSONB,
    aggregates   JSONB       NOT NULL DEFAULT '[]'::JSONB,
    valid_from   TIMESTAMPTZ,
    valid_until  TIMESTAMPTZ,
    revoked_at   TIMESTAMPTZ,
    archived_at  TIMESTAMPTZ,
    location     TEXT        NOT NULL DEFAULT '',
    issued_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    PRIMARY KEY (tenant_id, id)
);

ALTER TABLE cloud_meta.mandates
    ADD COLUMN IF NOT EXISTS archived_at TIMESTAMPTZ;

-- The immutable id of the card's domain, recorded at issuance so a
-- card survives its domain being renamed. '' on cards issued before it,
-- which keep matching by domain name; nothing is inferred for them.
ALTER TABLE cloud_meta.mandates
    ADD COLUMN IF NOT EXISTS domain_id TEXT NOT NULL DEFAULT '';

-- Every evaluation looks up by (tenant, subject). Validity is
-- deliberately not in the index predicate: the store returns lapsed
-- mandates too, because Evaluate has to see a revoked one to report it
-- rather than mistaking it for no mandate at all.
CREATE INDEX IF NOT EXISTS mandates_tenant_subject_idx
    ON cloud_meta.mandates (tenant_id, subject);

CREATE TABLE IF NOT EXISTS cloud_meta.mandate_draws (
    tenant_id    TEXT        NOT NULL REFERENCES cloud_meta.tenants(id) ON DELETE CASCADE,
    mandate_id   TEXT        NOT NULL,
    -- The idempotency anchor. A retried proposal carries the same id,
    -- and the primary key makes the second write an upsert rather than
    -- a second draw.
    proposal_id  TEXT        NOT NULL,
    quantity_key TEXT        NOT NULL,
    amount       BIGINT      NOT NULL,
    drawn_at     TIMESTAMPTZ NOT NULL,

    PRIMARY KEY (tenant_id, mandate_id, proposal_id, quantity_key),
    FOREIGN KEY (tenant_id, mandate_id)
        REFERENCES cloud_meta.mandates (tenant_id, id) ON DELETE CASCADE
);

-- The hot query: sum one quantity over a window for one mandate.
CREATE INDEX IF NOT EXISTS mandate_draws_usage_idx
    ON cloud_meta.mandate_draws (tenant_id, mandate_id, quantity_key, drawn_at DESC);

-- A proposal id that drew once is spent; Authorize looks up
-- every draw a proposal id recorded in the tenant, on any mandate.
CREATE INDEX IF NOT EXISTS mandate_draws_proposal_idx
    ON cloud_meta.mandate_draws (tenant_id, proposal_id);

-- Re-issuing a mandate id upserts the row, which is how a
-- ceiling is changed — and what that costs is the previous ceiling.
-- Without a record of it the product cannot say who changed a card's
-- authority, or what it used to be, which is most of what makes an
-- issued instrument different from a setting.
--
-- Append-only: rows are written on every issue and never updated. Each
-- holds the state *after* that issuance, so two consecutive rows are
-- the before and after of one decision and no stored diff can drift
-- out of step with the mandate it describes.
CREATE TABLE IF NOT EXISTS cloud_meta.mandate_revisions (
    tenant_id   TEXT        NOT NULL REFERENCES cloud_meta.tenants(id) ON DELETE CASCADE,
    mandate_id  TEXT        NOT NULL,
    revision    BIGINT      NOT NULL,
    changed_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    -- The management identity that issued it. Empty rather than absent
    -- when it could not be established: losing the change because we
    -- could not name who made it would be the worse trade.
    changed_by  TEXT        NOT NULL DEFAULT '',
    grants      JSONB       NOT NULL DEFAULT '[]'::JSONB,
    aggregates  JSONB       NOT NULL DEFAULT '[]'::JSONB,
    valid_until TIMESTAMPTZ,

    PRIMARY KEY (tenant_id, mandate_id, revision),
    FOREIGN KEY (tenant_id, mandate_id)
        REFERENCES cloud_meta.mandates (tenant_id, id) ON DELETE CASCADE
);

-- What a draw was for. A statement whose movements are proposal ids is
-- a log: nobody recognises their own agent's work in it. Added rather
-- than joined at read time because the audit row lives in the tenant's
-- own schema and the statement must not depend on reaching it.
--
-- Nullable-by-default and empty for draws recorded before this existed;
-- the surface renders those as unnamed rather than inventing a name.
ALTER TABLE cloud_meta.mandate_draws
    ADD COLUMN IF NOT EXISTS action    TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS entity_id TEXT NOT NULL DEFAULT '';

-- One authorization per proposal id per tenant. RecordAll inserts
-- the claim in the same transaction as the draws, ON CONFLICT DO
-- NOTHING: a second authorization racing under the same id finds the
-- claim taken, and its whole transaction rolls back. This is what makes
-- "a proposal id that drew is spent" atomic, independently of any lock.
CREATE TABLE IF NOT EXISTS cloud_meta.mandate_proposal_claims (
    tenant_id   TEXT        NOT NULL REFERENCES cloud_meta.tenants(id) ON DELETE CASCADE,
    proposal_id TEXT        NOT NULL,
    claimed_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (tenant_id, proposal_id)
);

ALTER TABLE cloud_meta.mandate_proposal_claims
    ADD COLUMN IF NOT EXISTS accounting_version INTEGER NOT NULL DEFAULT 0;

CREATE TABLE IF NOT EXISTS cloud_meta.mandate_accounting_cutover (
    version INTEGER PRIMARY KEY,
    activated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Backfill historical claims only before the first accounting cutover.
-- Later boots must never attempt version-zero inserts behind the writer guard.
INSERT INTO cloud_meta.mandate_proposal_claims (tenant_id, proposal_id, claimed_at)
SELECT d.tenant_id, d.proposal_id, MIN(d.drawn_at)
  FROM cloud_meta.mandate_draws d
 WHERE NOT EXISTS (SELECT 1 FROM cloud_meta.mandate_accounting_cutover WHERE version = 1)
   AND NOT EXISTS (SELECT 1 FROM cloud_meta.mandate_proposal_claims c
                    WHERE c.tenant_id = d.tenant_id AND c.proposal_id = d.proposal_id)
 GROUP BY tenant_id, proposal_id
ON CONFLICT DO NOTHING;

-- Who held the Card after each issuance. A Card's holder is changed by reissuing its id, which
-- overwrites the row; this is how the statement can say "moved from A
-- to B". Nullable: revisions written before it existed have no holder.
ALTER TABLE cloud_meta.mandate_revisions
    ADD COLUMN IF NOT EXISTS subject TEXT;

-- The work comes home. What happens to work outside
-- a Card: 'owner' holds it for the owner, 'refuse' refuses it. Cards that
-- exist when this runs become 'refuse', the behaviour they were issued
-- under; the issue path defaults new Cards to 'owner'.
ALTER TABLE cloud_meta.mandates
    ADD COLUMN IF NOT EXISTS on_exceed TEXT NOT NULL DEFAULT 'refuse';
ALTER TABLE cloud_meta.mandate_revisions
    ADD COLUMN IF NOT EXISTS on_exceed TEXT;

-- An action held for a Card's owner. One row per (tenant, proposal): a
-- retry of a held proposal finds this row rather than holding it twice.
CREATE TABLE IF NOT EXISTS cloud_meta.mandate_exceptions (
    tenant_id    TEXT        NOT NULL REFERENCES cloud_meta.tenants(id) ON DELETE CASCADE,
    id           TEXT        NOT NULL,
    proposal_id  TEXT        NOT NULL,
    subject      TEXT        NOT NULL,
    action       TEXT        NOT NULL DEFAULT '',
    entity_id    TEXT        NOT NULL DEFAULT '',
    fingerprint  TEXT        NOT NULL,
    request      JSONB       NOT NULL DEFAULT '{}'::JSONB,
    breaches     JSONB       NOT NULL DEFAULT '[]'::JSONB,
    status       TEXT        NOT NULL,
    reason       TEXT        NOT NULL DEFAULT '',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    resolved_by  TEXT        NOT NULL DEFAULT '',
    resolved_at  TIMESTAMPTZ,
    consumed_at  TIMESTAMPTZ,

    PRIMARY KEY (tenant_id, id),
    UNIQUE (tenant_id, proposal_id)
);
CREATE INDEX IF NOT EXISTS mandate_exceptions_status_idx
    ON cloud_meta.mandate_exceptions (tenant_id, status, created_at DESC);

-- Authority used outside a Card on its owner's say-so (approve once). Not
-- a draw: the Card's balance is untouched. The statement totals these
-- separately, so exceptions are never invisible.
CREATE TABLE IF NOT EXISTS cloud_meta.mandate_exception_entries (
    tenant_id     TEXT        NOT NULL REFERENCES cloud_meta.tenants(id) ON DELETE CASCADE,
    exception_id  TEXT        NOT NULL,
    mandate_id    TEXT        NOT NULL,
    proposal_id   TEXT        NOT NULL,
    quantity_key  TEXT        NOT NULL,
    amount        BIGINT      NOT NULL,
    recorded_at   TIMESTAMPTZ NOT NULL,
    action        TEXT        NOT NULL DEFAULT '',
    entity_id     TEXT        NOT NULL DEFAULT '',

    PRIMARY KEY (tenant_id, exception_id, mandate_id, quantity_key)
);
CREATE INDEX IF NOT EXISTS mandate_exception_entries_card_idx
    ON cloud_meta.mandate_exception_entries (tenant_id, mandate_id, recorded_at);

-- A held action never runs on silence. How long work held
-- for a Card's owner waits for an answer, in seconds; 0 is the default
-- (10 minutes). expires_at is fixed when an exception is first held;
-- NULL on exceptions held before this, which never expire.
ALTER TABLE cloud_meta.mandates
    ADD COLUMN IF NOT EXISTS hold_expiry_seconds BIGINT NOT NULL DEFAULT 0;
ALTER TABLE cloud_meta.mandate_exceptions
    ADD COLUMN IF NOT EXISTS expires_at TIMESTAMPTZ;
-- What the expiry sweep looks for: held calls past their expiry,
-- and expired ones whose alert hasn't gone out (a retry recorded the
-- expiry first). expiry_told_at is when the sweep told the alert targets.
ALTER TABLE cloud_meta.mandate_exceptions
    ADD COLUMN IF NOT EXISTS expiry_told_at TIMESTAMPTZ;
CREATE INDEX IF NOT EXISTS mandate_exceptions_held_expiry
    ON cloud_meta.mandate_exceptions (expires_at) WHERE status = 'held';
CREATE INDEX IF NOT EXISTS mandate_exceptions_expiry_untold
    ON cloud_meta.mandate_exceptions (expires_at) WHERE status = 'expired' AND expiry_told_at IS NULL;

-- A Card can be shared: a Card has one or more
-- holders, mandates.subject being the first. Discovery reads this table
-- (an agent's Cards are the ones it holds), and every Card that existed
-- before gets its subject as its one holder.
CREATE TABLE IF NOT EXISTS cloud_meta.mandate_holders (
    tenant_id   TEXT    NOT NULL REFERENCES cloud_meta.tenants(id) ON DELETE CASCADE,
    mandate_id  TEXT    NOT NULL,
    subject     TEXT    NOT NULL,
    position    INTEGER NOT NULL,

    PRIMARY KEY (tenant_id, mandate_id, subject),
    FOREIGN KEY (tenant_id, mandate_id)
        REFERENCES cloud_meta.mandates (tenant_id, id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS mandate_holders_subject_idx
    ON cloud_meta.mandate_holders (tenant_id, subject);
INSERT INTO cloud_meta.mandate_holders (tenant_id, mandate_id, subject, position)
SELECT tenant_id, id, subject, 0
  FROM cloud_meta.mandates
 WHERE subject IS NOT NULL AND subject <> ''
   AND NOT EXISTS (SELECT 1 FROM cloud_meta.mandate_holders h
                    WHERE h.tenant_id = mandates.tenant_id AND h.mandate_id = mandates.id)
ON CONFLICT DO NOTHING;
ALTER TABLE cloud_meta.mandate_revisions
    ADD COLUMN IF NOT EXISTS holders TEXT[];

-- Which holder drew. One holder left it implied; a shared Card cannot.
-- Draws and entries recorded before carry the Card's holder at the time
-- this ran.
ALTER TABLE cloud_meta.mandate_draws
    ADD COLUMN IF NOT EXISTS subject TEXT NOT NULL DEFAULT '';
UPDATE cloud_meta.mandate_draws d
   SET subject = m.subject
  FROM cloud_meta.mandates m
 WHERE d.subject = '' AND m.tenant_id = d.tenant_id AND m.id = d.mandate_id
   AND m.subject IS NOT NULL;
ALTER TABLE cloud_meta.mandate_exception_entries
    ADD COLUMN IF NOT EXISTS subject TEXT NOT NULL DEFAULT '';
UPDATE cloud_meta.mandate_exception_entries e
   SET subject = m.subject
  FROM cloud_meta.mandates m
 WHERE e.subject = '' AND m.tenant_id = e.tenant_id AND m.id = e.mandate_id
   AND m.subject IS NOT NULL;

-- Accounting. Historical rows retain version zero;
-- no old exception is converted into a draw and no old amount changes.
ALTER TABLE cloud_meta.mandate_draws
    ADD COLUMN IF NOT EXISTS measure TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS accounting_version INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS charged_window TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS window_start TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS charged_limits JSONB NOT NULL DEFAULT '[]'::JSONB;
ALTER TABLE cloud_meta.mandate_exceptions
    ADD COLUMN IF NOT EXISTS accounting_version INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS legacy_approval JSONB,
    ADD COLUMN IF NOT EXISTS covering_cards TEXT[] NOT NULL DEFAULT '{}',
    ADD COLUMN IF NOT EXISTS prior_approvals JSONB NOT NULL DEFAULT '[]'::JSONB;
ALTER TABLE cloud_meta.mandate_exception_entries
    ADD COLUMN IF NOT EXISTS accounting_version INTEGER NOT NULL DEFAULT 0;

-- Append-only compensation for a trusted terminal non-action. The original
-- draw and operation claim are retained; one physical draw has one release.
CREATE TABLE IF NOT EXISTS cloud_meta.mandate_effect_releases (
    tenant_id TEXT NOT NULL,
    mandate_id TEXT NOT NULL,
    proposal_id TEXT NOT NULL,
    quantity_key TEXT NOT NULL,
    amount BIGINT NOT NULL CHECK (amount >= 0),
    recorded_at TIMESTAMPTZ NOT NULL,
    source TEXT NOT NULL,
    evidence_id TEXT NOT NULL,
    reason TEXT NOT NULL,
    charged_limits JSONB NOT NULL,
    PRIMARY KEY (tenant_id, mandate_id, proposal_id, quantity_key),
    FOREIGN KEY (tenant_id, mandate_id, proposal_id, quantity_key)
      REFERENCES cloud_meta.mandate_draws (tenant_id, mandate_id, proposal_id, quantity_key)
);

-- One immutable cutover record. All new ordinary and exception
-- authorizations claim an operation with this version. Old writers omit
-- it and fail closed, including an old redemption that writes no draws.
-- A rollback cannot silently restore outside-budget authorizations.
INSERT INTO cloud_meta.mandate_accounting_cutover (version)
VALUES (1) ON CONFLICT DO NOTHING;

CREATE OR REPLACE FUNCTION cloud_meta.require_cumulative_card_writer()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.accounting_version <> 1 THEN
        RAISE EXCEPTION 'Card accounting requires writer version 1; stop authorizations or fix forward';
    END IF;
    RETURN NEW;
END;
$$;
DROP TRIGGER IF EXISTS mandate_accounting_writer_guard ON cloud_meta.mandate_proposal_claims;
CREATE TRIGGER mandate_accounting_writer_guard
BEFORE INSERT ON cloud_meta.mandate_proposal_claims
FOR EACH ROW EXECUTE FUNCTION cloud_meta.require_cumulative_card_writer();

-- Optional scoped decision-maker grants. No execution or management authority.
ALTER TABLE cloud_meta.mandate_exceptions ADD COLUMN IF NOT EXISTS external_answer JSONB;
-- The next step and note given with a rejection, read by the agent.
ALTER TABLE cloud_meta.mandate_exceptions ADD COLUMN IF NOT EXISTS refusal JSONB;
CREATE TABLE IF NOT EXISTS cloud_meta.approver_grants (
 tenant_id TEXT NOT NULL REFERENCES cloud_meta.tenants(id),
 id TEXT NOT NULL,
 card_id TEXT NOT NULL,
 key_hash TEXT NOT NULL UNIQUE,
 grant_record JSONB NOT NULL,
 revoked_at TIMESTAMPTZ,
 PRIMARY KEY(tenant_id,id),
 FOREIGN KEY(tenant_id,card_id) REFERENCES cloud_meta.mandates(tenant_id,id)
);

ALTER TABLE cloud_meta.approver_grants ADD COLUMN IF NOT EXISTS last_seen_at TIMESTAMPTZ;

-- trial_draws (kiff/tafs#107): what a trial's allowed writes would have
-- drawn, kept apart from the real ledger, per trial run, so its later
-- writes are decided on live usage plus its own. Nothing reads them but
-- that trial; they are kept two days and erased with the account.
CREATE TABLE IF NOT EXISTS cloud_meta.trial_draws (
    tenant_id    TEXT        NOT NULL REFERENCES cloud_meta.tenants(id) ON DELETE CASCADE,
    trial_id     TEXT        NOT NULL,
    subject      TEXT        NOT NULL,
    mandate_id   TEXT        NOT NULL,
    quantity_key TEXT        NOT NULL,
    amount       BIGINT      NOT NULL,
    drawn_at     TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS trial_draws_trial_idx ON cloud_meta.trial_draws (tenant_id, trial_id, subject, mandate_id, quantity_key, drawn_at);
CREATE INDEX IF NOT EXISTS trial_draws_age_idx ON cloud_meta.trial_draws (drawn_at);
