CREATE SCHEMA IF NOT EXISTS purchase;

-- One row per case version. Only the newest version of a case is live: a
-- new version supersedes the earlier ones and releases their reservation.
CREATE TABLE IF NOT EXISTS purchase.cases (
    tenant_id     TEXT        NOT NULL,
    case_id       TEXT        NOT NULL,
    version       INT         NOT NULL,
    digest        TEXT        NOT NULL,
    body          JSONB       NOT NULL,
    state         TEXT        NOT NULL,
    ended         TEXT        NOT NULL DEFAULT '',
    card_id       TEXT        NOT NULL,
    currency      TEXT        NOT NULL,
    max_amount    BIGINT      NOT NULL,
    decided       JSONB,
    reserved      BIGINT      NOT NULL DEFAULT 0,
    room_day      DATE,
    decided_at    TIMESTAMPTZ,
    valid_until   TIMESTAMPTZ,
    approved_once BOOLEAN     NOT NULL DEFAULT false,
    authorized    BIGINT      NOT NULL DEFAULT 0,
    lease_holder  TEXT        NOT NULL DEFAULT '',
    lease_until   TIMESTAMPTZ,
    outcome       TEXT        NOT NULL DEFAULT 'none',
    issued_card   TEXT        NOT NULL DEFAULT '',
    created_at    TIMESTAMPTZ NOT NULL,
    updated_at    TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (tenant_id, case_id, version)
);

-- The live authorization finds a case by its issued card: one card per case.
CREATE UNIQUE INDEX IF NOT EXISTS purchase_cases_card ON purchase.cases (issued_card) WHERE issued_card <> '';

-- A Card's room, per day and currency: a reservation is a conditional
-- update here, in the decision's transaction.
CREATE TABLE IF NOT EXISTS purchase.room (
    tenant_id TEXT   NOT NULL,
    card_id   TEXT   NOT NULL,
    day       DATE   NOT NULL,
    currency  TEXT   NOT NULL,
    used      BIGINT NOT NULL DEFAULT 0,
    PRIMARY KEY (tenant_id, card_id, day, currency)
);

-- Each live authorization's answer, once: a repeat of the same
-- authorization id gets the same answer.
CREATE TABLE IF NOT EXISTS purchase.authorizations (
    authorization_id TEXT        PRIMARY KEY,
    tenant_id        TEXT        NOT NULL,
    case_id          TEXT        NOT NULL,
    version          INT         NOT NULL,
    approved         BOOLEAN     NOT NULL,
    reason           TEXT        NOT NULL,
    amount           BIGINT      NOT NULL,
    at               TIMESTAMPTZ NOT NULL
);

-- Each business is its own connected account at the issuer, and the
-- cardholder: an authorization counts only on its tenant's account.
CREATE TABLE IF NOT EXISTS purchase.accounts (
    tenant_id TEXT PRIMARY KEY,
    account   TEXT NOT NULL UNIQUE
);

-- The issuer's events, once each: a replayed event id is refused.
CREATE TABLE IF NOT EXISTS purchase.events (
    event_id TEXT        PRIMARY KEY,
    at       TIMESTAMPTZ NOT NULL
);

-- The hold KIFF opened for a case the owner decides: its answer signs the
-- case's purchase line.
ALTER TABLE purchase.cases ADD COLUMN IF NOT EXISTS exception_id TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS purchase_cases_exception ON purchase.cases (tenant_id, exception_id) WHERE exception_id <> '';

-- What the receipt says of each authorization: the issuer's merchant beside
-- the case's, and the issuer's verification result.
ALTER TABLE purchase.authorizations ADD COLUMN IF NOT EXISTS merchant_name TEXT NOT NULL DEFAULT '';
ALTER TABLE purchase.authorizations ADD COLUMN IF NOT EXISTS merchant_category TEXT NOT NULL DEFAULT '';
ALTER TABLE purchase.authorizations ADD COLUMN IF NOT EXISTS verification TEXT NOT NULL DEFAULT '';

-- The issuer's money events on a case's card (a capture, a refund), once
-- each by the issuer's event id: what the money's status is read from. An
-- event for a card in no case is kept with an empty case, to be found.
CREATE TABLE IF NOT EXISTS purchase.card_events (
    event_id         TEXT        PRIMARY KEY,
    tenant_id        TEXT        NOT NULL,
    case_id          TEXT        NOT NULL,
    version          INT         NOT NULL,
    card             TEXT        NOT NULL,
    kind             TEXT        NOT NULL,
    authorization_id TEXT        NOT NULL,
    amount           BIGINT      NOT NULL,
    at               TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS purchase_card_events_case ON purchase.card_events (tenant_id, case_id, version);

-- A Card whose buying is paused: a force capture or an over-capture on one
-- of its purchases. Within limits buys nothing until the owner resumes it;
-- the owner's own approval still decides.
CREATE TABLE IF NOT EXISTS purchase.paused (
    tenant_id TEXT        NOT NULL,
    card_id   TEXT        NOT NULL,
    reason    TEXT        NOT NULL,
    case_id   TEXT        NOT NULL,
    at        TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (tenant_id, card_id)
);

-- The order the shop confirmed, as the execution read it on the page.
ALTER TABLE purchase.cases ADD COLUMN IF NOT EXISTS order_ref TEXT NOT NULL DEFAULT '';
ALTER TABLE purchase.cases ADD COLUMN IF NOT EXISTS order_confirmed_at TIMESTAMPTZ;
