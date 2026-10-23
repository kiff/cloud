-- signed_requests.sql
--
-- The phone's signed setup calls already made: one row per request
-- the API accepted, keyed by its replay ID (sha256 over the device key and
-- the signed message, VerifyRequest). A row lives until its signature could
-- no longer verify; until then the same request is refused, by every API
-- process and after a restart.
--
-- No tenant column and no foreign key: the replay ID already binds the
-- tenant through the signed message, and a row is gone within minutes.
--
-- This file is the source of truth. ApplySignedRequestsSchema embeds and
-- runs it at API startup. All statements are idempotent.

CREATE TABLE IF NOT EXISTS cloud_meta.signed_requests (
    replay_id   BYTEA       PRIMARY KEY,
    -- When the request's signature can no longer verify; the row is pruned
    -- after it.
    valid_until TIMESTAMPTZ NOT NULL
);

CREATE INDEX IF NOT EXISTS signed_requests_valid_until_idx
    ON cloud_meta.signed_requests (valid_until);
