# Keep tool keys out of KIFF

Choose who holds a connected tool's key: KIFF, nobody, or your own relay.
For owners of a tool connected through the [MCP gateway](/docs/mcp-gateway) who don't want KIFF to hold its key.
Switch the connection to `verify` or `relay` and check KIFF's permit before the tool acts.

| Mode | Who holds the tool's key | What KIFF sends the tool |
|---|---|---|
| `sealed` (the default) | KIFF, stored encrypted | the call and the stored credential |
| `verify` | nobody: the tool checks KIFF itself | the call and an **execution permit** |
| `relay` | your relay, from your own vault | the call and an execution permit |

## The execution permit

For each allowed call, KIFF sends a permit in the `KIFF-Permit` header:
a 60-second Ed25519 JWT (`typ: kiff-permit+jwt`) naming the account, the
connection's audience, the tool, a hash of the exact arguments, the
operation id, the agent and the Card. Keys are published at
`https://api.kiff.dev/.well-known/kiff-permit-keys.json`.

## Verify mode: the tool checks the permit

For a tool you run. Check the permit before the tool does anything:

```bash
pip install "kiff-guard[verifier]"
```

```python
from kiff_guard.permit import JWKSKeys, SQLiteStore, Verifier

verifier = Verifier(
    issuer="https://api.kiff.dev",
    audience="https://refunds.example.com/mcp",  # the connection's audience
    tenant="<your KIFF account id>",
    keys=JWKSKeys(),
    store=SQLiteStore("/var/lib/refunds/permits.db"),
)

outcome = verifier.run(headers["KIFF-Permit"], "refund_order", arguments, adapter)
```

`adapter` makes the one downstream call (`execute`) and finds its result
again (`lookup`). The verifier refuses a permit that is not KIFF's, not
for this tool, audience and arguments, or expired, and runs each
operation at most once. Run `verifier.sweep(...)` at start-up and
periodically; `verifier.unknown()` lists what a person must check.

## Relay mode: your relay holds the key

For a service that won't check KIFF's permit. Run
`kiff_guard.permit.relay.Relay` next to your vault: it verifies the
permit, then makes the one call with your key.

## Moving a connection

Under **Connected tools**, choose **Change who holds the key**. Over the
API:

```bash
curl -s -X PUT https://mcp.kiff.dev/v1/connections/refund_order/mode \
  -H "Authorization: Bearer $OWNER_KEY" -H "Content-Type: application/json" \
  -d '{"mode": "verify", "audience": "https://refunds.example.com/mcp"}'
```

Moving to `verify` deletes the credential KIFF held. A `verify` or
`relay` connection needs an idempotency argument.

## When revoking or expiring a Card takes effect

KIFF checks the Card at every call, so no new call is allowed once a
Card is revoked or expired. A call allowed just before can still start:
in `sealed` mode if it is already on its way, and in `verify` or `relay`
mode for up to 90 seconds (the permit's 60 seconds plus 30 seconds of
clock tolerance). Nothing undoes a call already sent.
