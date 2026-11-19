-- What each call is about, in the taf's words ("Order 1042"), beside its
-- decision and outside anything a receipt hashes or signs, so a row can be
-- erased and every receipt still verifies.
CREATE SCHEMA IF NOT EXISTS cloud_meta;

CREATE TABLE IF NOT EXISTS cloud_meta.call_about (
    tenant_id   TEXT        NOT NULL,
    proposal_id TEXT        NOT NULL,
    about       TEXT        NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, proposal_id)
);
