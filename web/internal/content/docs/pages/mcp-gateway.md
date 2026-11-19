# Use the KIFF MCP gateway

The gateway sits between an agent and its remote MCP tools and checks each call against the agent's Card.
For owners and admins connecting tools over the API, and for anyone building the agent side.
Connect the tool, give the agent a Card, then point the agent at `https://mcp.kiff.dev/mcp`.

```text
your agent ──MCP──▶ mcp.kiff.dev ──MCP──▶ your connected tool
```

The dashboard steps are in the [quickstart](/docs/quickstart). You need
a remote MCP server at a public HTTPS URL and, if it needs one, its
bearer credential. KIFF stores it encrypted, or the tool checks a
[permit](/docs/tool-permits) instead. KIFF does not sign in to a tool
with OAuth.

## 1. Connect a tool

With an owner or admin key, put the body in a local file only you can
read, then post it:

```json
{
  "name": "issue_refund",
  "url": "https://tools.example.com/mcp",
  "tool": "refund",
  "amount_argument": "amount_eur",
  "amount_unit": "EUR",
  "amount_scale": 0,
  "idempotency_argument": "idempotency_key",
  "credential": "<tool server bearer token>"
}
```

```bash
curl -s -X POST https://mcp.kiff.dev/v1/connections \
  -H "Authorization: Bearer $OWNER_KEY" \
  -H "Content-Type: application/json" \
  --data-binary @connection.json
```

- `tool` is the name on the server; `name` is the name the agent sees.
- `amount_argument` is the whole-number amount a Card limits.
- `idempotency_argument` is the tool's idempotency key; KIFF fills it.
- `resource_argument` (optional) is the parameter a call is on, such as
  `"order_number"`, so a Card's **Only for** can list values.

Delete the request file afterwards. To change **Each call is on** or
**If a call did nothing** later, use the tool's **Settings**.

### One action of a tool that does many

When one server tool does many actions, connect one as its own tool with
`fixed_arguments` (sent on every call), and point at nested values with
`amount_path` and `resource_path`:

```json
{
  "name": "stripe_refund",
  "url": "https://mcp.stripe.com",
  "tool": "stripe_api_write",
  "credential": "rk_test_your_agent_key",
  "fixed_arguments": {"stripe_api_operation_id": "PostRefunds"},
  "amount_path": "parameters.amount",
  "amount_unit": "EUR",
  "amount_scale": 2,
  "resource_path": "parameters.charge"
}
```

A call without the amount is refused. Paths don't work on `verify`
connections. If the call's currency isn't fixed, add a `unit_lookup`; if
the call doesn't carry its amount, an `amount_lookup`. Each names one of
the agent's read tools on the same connection, for example
`{"tool": "stripe_payment_intent", "argument": "intent", "path": "currency"}`.

### Trusted no-effect outcomes

Set `outcome_contract: "terminal_no_effect_v1"` only for a tool that
guarantees a final refusal had no effect and cannot run later. It needs
an idempotency argument. The tool then returns:

```json
{
  "isError": true,
  "structuredContent": {
    "kiff_outcome": {
      "operation_id": "<KIFF invocation identity>",
      "effect": "none",
      "terminal": true,
      "reason_code": "order_not_paid"
    }
  }
}
```

An ordinary MCP error never counts as this.

## 2. Give the agent a Card

Use **Give an agent a Card** on the connected tool, or
`POST /v1/me/mandates` ([KIFF Cards](/docs/kiff-cards#setting-one)).

## 3. Connect the agent

Each agent uses its own gateway key or connect link, from its **Tools**
tab. Either can only ask: it cannot connect tools, issue Cards or answer
approvals. Revoke them on **Connections**.

Over the API, an admin creates a gateway key:

```bash
curl -s -X POST https://api.kiff.dev/v1/keys \
  -H "Authorization: Bearer $ADMIN_KEY" \
  -H "Content-Type: application/json" \
  -d '{"label":"my-agent","roles":["gateway_agent"],"agent_id":"my-agent"}'
```

The agent's MCP server is `https://mcp.kiff.dev/mcp` with the key as a
bearer token. In Codex, add `default_tools_approval_mode = "approve"`
under `[mcp_servers.kiff]`, or Codex asks before every call.

## Operation ids and held calls

The gateway adds an optional `kiff_operation_id` argument to every tool.
Give each new action a new id, and reuse it when retrying the same one.
A seen id gets that call's outcome and is never forwarded again. Without
an id, an identical call within 10 minutes counts as a retry. Give every
call that moves money an id.

`kiff_pending` lists this credential's held, sending and unknown calls
from the last 7 days, with each held call's original arguments, its
`kiff_operation_id` and the answer (`waiting`, `approved`, `refused`,
`approved_no_room`, `withdrawn`, `ended` or `unavailable`). In a new
session, call it first:

```json
{"name": "kiff_pending", "arguments": {}}
```

Collect an approved call with the same tool, arguments and id. It is
sent at most once. A call whose outcome is unknown is not resent: check
with the tool.

## What the agent sees

- Each tool's description ends with what the agent's Card allows.
- `kiff_card` shows the agent's Cards and what each total has left.
- `kiff_about` (such as "Order 1042") and `kiff_reason` are shown to the
  approver as the agent's words. They decide nothing.
- KIFF's facts are under `dev.kiff/call` in `_meta`: `state`, `sent`
  (`no`, `yes` or `unknown`), `retry` (`same_call`, `new_call` or
  `none`) and `reasons`. A held call adds `review_url` and
  `hold_expires_at`.

Calls made through the tafs app follow extra rules; see
[Connection profiles](/docs/tafs-connection-profiles).
