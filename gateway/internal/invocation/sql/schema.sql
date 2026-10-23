-- Owned by the kiff_gateway role (internal/dbrole). apps/api
-- deletes a tenant's rows on erasure, and reads one thing: how a call it
-- authorized ended (invocations' state, message and tool_error).
-- It never writes them otherwise.
-- ApplySchema creates the gateway schema itself when it is missing; see there.

-- One row per tool call the gateway has seen. key is also the proposal
-- id and the ToolCall entity id sent to apps/api.
CREATE TABLE IF NOT EXISTS gateway.invocations (
    key           text PRIMARY KEY,
    tenant_id     text NOT NULL,
    credential_id text NOT NULL,
    agent_id      text NOT NULL,
    tool          text NOT NULL,
    operation_id  text NOT NULL DEFAULT '',
    args_hash     text NOT NULL,
    arguments     jsonb NOT NULL,
    state         text NOT NULL,
    exception_id  text NOT NULL DEFAULT '',
    review_url    text NOT NULL DEFAULT '',
    message       text NOT NULL DEFAULT '',
    result        jsonb,
    created_at    timestamptz NOT NULL,
    updated_at    timestamptz NOT NULL
);

-- The dedup window for calls without kiff_operation_id: the open
-- invocation for identical (credential, tool, arguments), replaced only
-- once the window since it opened has passed.
CREATE TABLE IF NOT EXISTS gateway.dedup_slots (
    credential_id  text NOT NULL,
    tool           text NOT NULL,
    args_hash      text NOT NULL,
    invocation_key text NOT NULL,
    opened_at      timestamptz NOT NULL,
    PRIMARY KEY (credential_id, tool, args_hash)
);

-- Tools an account has connected (remote MCP servers). Stored
-- credentials come with the connect flow.
CREATE TABLE IF NOT EXISTS gateway.tools (
    tenant_id       text NOT NULL,
    name            text NOT NULL,
    description     text NOT NULL DEFAULT '',
    input_schema    jsonb NOT NULL,
    upstream_url    text NOT NULL,
    upstream_tool   text NOT NULL,
    amount_arg      text NOT NULL DEFAULT '',
    idempotency_arg text NOT NULL DEFAULT '',
    PRIMARY KEY (tenant_id, name)
);

-- In-flight rows the stale sweep looks at.
CREATE INDEX IF NOT EXISTS invocations_in_flight
    ON gateway.invocations (updated_at) WHERE state IN ('deciding', 'forwarding');

-- Stored tool credentials: AES-256-GCM, bound to the
-- account and tool; the key id names the gateway key that sealed it.
ALTER TABLE gateway.tools ADD COLUMN IF NOT EXISTS credential_key_id text NOT NULL DEFAULT '';
ALTER TABLE gateway.tools ADD COLUMN IF NOT EXISTS credential bytea;
ALTER TABLE gateway.tools ADD COLUMN IF NOT EXISTS created_at timestamptz NOT NULL DEFAULT now();
ALTER TABLE gateway.tools ADD COLUMN IF NOT EXISTS updated_at timestamptz NOT NULL DEFAULT now();

-- Erasure: apps/api's Deprovision deletes a tenant's rows from
-- every table in this schema that has a tenant_id column, so every
-- gateway table carries one (enforced by TestEveryGatewayTableIsErasable).
ALTER TABLE gateway.dedup_slots ADD COLUMN IF NOT EXISTS tenant_id text NOT NULL DEFAULT '';
UPDATE gateway.dedup_slots d SET tenant_id = i.tenant_id
  FROM gateway.invocations i WHERE d.tenant_id = '' AND i.key = d.invocation_key;
CREATE INDEX IF NOT EXISTS invocations_tenant ON gateway.invocations (tenant_id);
CREATE INDEX IF NOT EXISTS dedup_slots_tenant ON gateway.dedup_slots (tenant_id);

-- The owner's declaration that a tool only reads. Its calls are
-- still decided against the Card, but are never deduplicated.
ALTER TABLE gateway.tools ADD COLUMN IF NOT EXISTS read_only boolean NOT NULL DEFAULT false;

-- The machine-readable codes for how a call ended (apps/api's
-- refusal reasons, or the gateway's own), reported with every replay.
ALTER TABLE gateway.invocations ADD COLUMN IF NOT EXISTS reasons text[] NOT NULL DEFAULT '{}';

-- When a held call stops waiting for the owner, as apps/api
-- reported it, so the agent is told when to stop retrying.
ALTER TABLE gateway.invocations ADD COLUMN IF NOT EXISTS hold_expires_at timestamptz;

-- The tool's own error, when a call it received came back as one:
-- its message, redacted. Empty when the tool reported success, or the call
-- was never delivered. Kept when the result is purged, so the record still
-- tells a refusal by the tool from a call it carried out.
ALTER TABLE gateway.invocations ADD COLUMN IF NOT EXISTS tool_error text NOT NULL DEFAULT '';

-- Whether the gateway recorded how a delivered call ended:
-- 'done' (the tool reported success) or 'error' (tool_error says
-- what). Empty for calls delivered before this column, or by an older
-- gateway during a rollout: their outcome was not recorded, and an empty
-- tool_error there proves nothing.
ALTER TABLE gateway.invocations ADD COLUMN IF NOT EXISTS tool_outcome text NOT NULL DEFAULT '';

-- Which MCP clients each agent connects with,
-- so we know which to verify for chat approvals next. Only the client's
-- own claims and when it was last seen; never arguments or results.
CREATE TABLE IF NOT EXISTS gateway.agent_clients (
    tenant_id       text NOT NULL,
    agent_id        text NOT NULL,
    client_name     text NOT NULL,
    client_version  text NOT NULL,
    url_elicitation boolean NOT NULL,
    last_seen_at    timestamptz NOT NULL,
    PRIMARY KEY (tenant_id, agent_id, client_name)
);

-- Finished calls keep no arguments (they are never forwarded again).
-- Transition and RecoverStale drop them; this clears rows written before
-- that, and is a no-op afterwards.
UPDATE gateway.invocations SET arguments = '{}'::jsonb
 WHERE state IN ('refused', 'forwarded', 'failed', 'unknown') AND arguments <> '{}'::jsonb;

-- What a tool's amount argument means, for display only (unit EUR, scale 2:
-- 8000 is €80.00). Enforcement sums the whole numbers either way.
ALTER TABLE gateway.tools ADD COLUMN IF NOT EXISTS amount_unit text NOT NULL DEFAULT '';
ALTER TABLE gateway.tools ADD COLUMN IF NOT EXISTS amount_scale integer NOT NULL DEFAULT 0;

-- Who holds a connection's downstream credential. sealed: KIFF,
-- encrypted (above). verify: nobody; the tool checks KIFF's execution
-- permit itself. relay: the customer's relay, which checks the permit and
-- adds the key; the stored credential is then only the relay's transport
-- credential. audience is what the tool or relay expects in the permit.
ALTER TABLE gateway.tools ADD COLUMN IF NOT EXISTS mode text NOT NULL DEFAULT 'sealed';
ALTER TABLE gateway.tools ADD COLUMN IF NOT EXISTS audience text NOT NULL DEFAULT '';

-- Kiff_pending lists one credential's held, sending and unknown calls.
CREATE INDEX IF NOT EXISTS invocations_pending
    ON gateway.invocations (credential_id, created_at DESC) WHERE state IN ('held', 'forwarding', 'unknown');

-- Owner-selected terminal-outcome adapter and durable evidence.
ALTER TABLE gateway.tools ADD COLUMN IF NOT EXISTS outcome_contract text NOT NULL DEFAULT '';
ALTER TABLE gateway.invocations ADD COLUMN IF NOT EXISTS no_effect_evidence jsonb;
ALTER TABLE gateway.invocations ADD COLUMN IF NOT EXISTS execution_binding jsonb;
ALTER TABLE gateway.tools ADD COLUMN IF NOT EXISTS outcome_lookup_tool text NOT NULL DEFAULT '';

-- The string argument naming what a call is on (an order, a
-- customer), sent to KIFF so a Card's resource scope can read it.
ALTER TABLE gateway.tools ADD COLUMN IF NOT EXISTS resource_arg text NOT NULL DEFAULT '';

-- One operation of a generic vendor tool as its own governed tool.
-- amount_path and resource_path say where the call carries the amount and
-- the resource ("parameters.amount"); fixed_args are arguments KIFF always
-- sends and the agent can't set ({"stripe_api_operation_id": "PostRefunds"}).
ALTER TABLE gateway.tools ADD COLUMN IF NOT EXISTS amount_path text NOT NULL DEFAULT '';
ALTER TABLE gateway.tools ADD COLUMN IF NOT EXISTS resource_path text NOT NULL DEFAULT '';
ALTER TABLE gateway.tools ADD COLUMN IF NOT EXISTS fixed_args jsonb;

-- The KIFF key that connected a tool, or '' for a person.
-- A connector key (the one planque keeps) tests and removes only the tools
-- it connected, and apps/api reads this to keep its contract writes to them.
ALTER TABLE gateway.tools ADD COLUMN IF NOT EXISTS created_by text NOT NULL DEFAULT '';

-- The name people see for a tool, from the vendor's own
-- MCP title when it gives one, set when the tool is connected. Display
-- only: the domain contract never carries it. Rows from before get their
-- vendor tool's name in words, once.
ALTER TABLE gateway.tools ADD COLUMN IF NOT EXISTS title text NOT NULL DEFAULT '';
UPDATE gateway.tools
   SET title = upper(left(replace(upstream_tool, '_', ' '), 1)) || substr(replace(upstream_tool, '_', ' '), 2)
 WHERE title = '' AND upstream_tool <> '';

-- The read that says which unit a call is in (a payment's
-- currency): {"tool", "argument", "path"}. A call in another unit than
-- amount_unit is refused before KIFF decides it. NULL: no check.
ALTER TABLE gateway.tools ADD COLUMN IF NOT EXISTS unit_lookup jsonb;
