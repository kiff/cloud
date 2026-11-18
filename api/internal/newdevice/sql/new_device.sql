-- new_device.sql
--
-- What the "new device signed in" email needs to tell a new device from
-- one the person has used, and to wait a moment before it goes.
--
-- known_devices holds, per person, a sha256 of each sign-in client the
-- sign-in provider named (Clerk's client_id: one per browser or app), and
-- when it was first and last seen. Nothing readable: no IP, no place, no
-- device name. A device unseen for 180 days is pruned (newdevice.Unseen),
-- and a person's rows are erased with them (tenant/erasure.go, by subject).
--
-- new_device_emails holds a new-device sign-in's email for a moment, so a
-- device approval in the tafs app within it marks it "tafs by KIFF". It
-- holds the sign-in's session and time, no device, no place and no email
-- body (the email reads those from the sign-in provider as it goes), and
-- goes once sent, or when it expires within the device-approval window
-- (minutes). A device approval writes its sign-in session id here only, so
-- that id lasts no longer than the row.
--
-- This file is the source of truth. ApplySchema embeds and runs it at API
-- startup. All statements are idempotent.

CREATE TABLE IF NOT EXISTS cloud_meta.known_devices (
    -- The person (the OIDC sub).
    subject     TEXT        NOT NULL,
    -- sha256 of the sign-in provider's client id; never the id itself.
    client_hash BYTEA       NOT NULL,
    first_seen  TIMESTAMPTZ NOT NULL,
    last_seen   TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (subject, client_hash)
);

-- The prune of devices unseen for 180 days.
CREATE INDEX IF NOT EXISTS known_devices_last_seen_idx
    ON cloud_meta.known_devices (last_seen);

CREATE TABLE IF NOT EXISTS cloud_meta.new_device_emails (
    -- The sign-in provider's session id of the sign-in.
    session_id   TEXT        PRIMARY KEY,
    subject      TEXT        NOT NULL,
    -- The provider told KIFF of this sign-in on a new device: the email
    -- goes at due_at. A row only a device approval wrote waits for it.
    new_device   BOOLEAN     NOT NULL DEFAULT FALSE,
    -- device, browser, city and country are no longer written; they stay
    -- while a deploy runs the release before this one beside it, and a
    -- later release drops them.
    device       TEXT        NOT NULL DEFAULT '',
    browser      TEXT        NOT NULL DEFAULT '',
    city         TEXT        NOT NULL DEFAULT '',
    country      TEXT        NOT NULL DEFAULT '',
    signed_in_at TIMESTAMPTZ NULL,
    -- A device approval in the tafs app came from this sign-in.
    tafs         BOOLEAN     NOT NULL DEFAULT FALSE,
    due_at       TIMESTAMPTZ NULL,
    -- Gone after it, sent or not.
    expires_at   TIMESTAMPTZ NOT NULL
);

CREATE INDEX IF NOT EXISTS new_device_emails_due_idx
    ON cloud_meta.new_device_emails (due_at) WHERE new_device;
CREATE INDEX IF NOT EXISTS new_device_emails_expires_idx
    ON cloud_meta.new_device_emails (expires_at);
