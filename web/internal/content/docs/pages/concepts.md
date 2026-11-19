# How kiff-guard works

kiff-guard is a thin wrapper on your agent's tool calls that asks KIFF before each one runs.
For builders using one of the framework pages that follow; each shows only its own hook.
Start in `observe`, derive a domain, then switch to `enforce`.

## Install

```bash
pip install "git+https://github.com/kiff/kiff-guard.git#subdirectory=packages/python/kiff-guard"
```

## Two modes

- **`observe`** runs every tool, records a receipt and learns your actions. It never calls KIFF and
  needs no account.
- **`enforce`** asks KIFF **before** each tool runs: `allowed` runs it, `approval_required` holds it
  for a person, `blocked` or `invalid` refuses it. A guard error fails closed unless you pass
  `fail_closed=False`.

Guard carries the proposal; KIFF decides. Where the action declares `requires_authority`, the
[Card](/docs/kiff-cards) issued to the agent is one more gate, refused with `no_mandate_issued` if
none was issued. Guard neither issues nor administers cards, and never asserts roles.

Every tool call gets **exactly one** receipt, in both modes and every adapter.

## Derive a domain

After observing, print a starter domain, review it, and activate it in the [dashboard](/dashboard):

```python
from kiff_guard import export_yaml
print(export_yaml("my-domain", guard.catalog))
```

## Switch to enforce

Mint an API key in the [dashboard](/dashboard), then build the guard this way. The framework wiring
doesn't change:

```python
from kiff_guard import Guard, HTTPClient, ToolMap

client = HTTPClient(
    api_key="kiff_live_...",
    tool_map=ToolMap().bind(
        "refund_order", action="REFUND_ORDER",
        entity_type="Order", entity_arg="order_id"),
)
guard = Guard(client=client, tenant="<tenant>", agent="support", mode="enforce")
```

## Two adapter shapes

- **Middleware**: the framework hands the guard the tool to run, as
  `guard.evaluate(tool, args, run=...)`. A withheld call raises `kiff_guard.Hold`.
- **Vote**: the framework runs the tool; the hook only says yes or no, through
  `guard.observe()` or `guard.decide_only()`.

Which one is the framework's API, not a choice. The code and conformance suite are in
[`kiff/kiff-guard`](https://github.com/kiff/kiff-guard).
