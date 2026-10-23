-- wallets.sql
--
-- Cloud-meta tables for the optional wallet link May
-- 2026 amendment.
--
--   - cloud_meta.wallet_nonces: outstanding link requests. One row
--     per IssueNonce call, consumed exactly once by VerifyLink.
--     `used_at IS NULL` means the nonce is still redeemable;
--     non-null means it has been consumed (replay attempts find
--     the row but find used_at set and refuse).
--
--   - cloud_meta.user_wallets: persistent link from a subject (the
--     Clerk user id) to a wallet address. One row per subject in
--     v0.1; multi-wallet is a v0.2 concern.
--
-- This file is the source of truth. ApplyWalletsSchema embeds and
-- runs it at API startup. All statements are idempotent.

CREATE TABLE IF NOT EXISTS cloud_meta.wallet_nonces (
    nonce       TEXT        PRIMARY KEY,
    subject     TEXT        NOT NULL,
    address     TEXT        NOT NULL,
    chain_id    BIGINT      NOT NULL,
    issued_at   TIMESTAMPTZ NOT NULL,
    expires_at  TIMESTAMPTZ NOT NULL,
    used_at     TIMESTAMPTZ NULL
);

CREATE INDEX IF NOT EXISTS wallet_nonces_subject_idx
    ON cloud_meta.wallet_nonces (subject);

CREATE INDEX IF NOT EXISTS wallet_nonces_active_idx
    ON cloud_meta.wallet_nonces (subject) WHERE used_at IS NULL;

CREATE TABLE IF NOT EXISTS cloud_meta.user_wallets (
    subject     TEXT        PRIMARY KEY,
    address     TEXT        NOT NULL,
    chain_id    BIGINT      NOT NULL,
    linked_at   TIMESTAMPTZ NOT NULL
);

CREATE INDEX IF NOT EXISTS user_wallets_address_idx
    ON cloud_meta.user_wallets (address);
