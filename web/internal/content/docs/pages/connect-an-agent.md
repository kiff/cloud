# Connect an agent

Three ways to send an agent's actions to KIFF so its [Card](/docs/kiff-cards) is checked.
For anyone wiring an agent to KIFF.
Pick the row that matches how your agent is built.

| Your agent | Path |
|---|---|
| Calls tools over MCP (Claude Code, Codex) | [The KIFF MCP gateway](/docs/mcp-gateway). No code. |
| Runs in your own code, on a framework | [kiff-guard](#connect-with-kiff-guard): one hook. |
| Any code that can make an HTTP call | [The decision API](#call-the-decision-api-directly): one call. |

Give each agent its own key from its page in KIFF Cloud: that is how
KIFF knows whose Card to draw on. In Claude Code or Codex, the
[KIFF skill](/docs/agent-skill) can walk you through it.

## Connect with kiff-guard

kiff-guard (MIT) hooks your agent's tool calls. Install the core and
your framework's adapter, here Agno (TypeScript: `npm install @kiff/kiff-guard`):

```bash
pip install "kiff-guard[agno]"
```

Start in `observe`: every tool runs, nothing calls KIFF, and you get
receipts of what the agent did. Then create a key on the agent's
**Keys** tab (**New key**, **Code that runs kiff-guard**), give it a
Card, and switch to `enforce`:

```python
from kiff_guard import Guard
from kiff_guard.adapters.agno import agno_hook

guard = Guard(mode="enforce", client=client)   # client carries the key
agent = Agent(model=..., tools=[refund_order], tool_hooks=[agno_hook(guard)])
```

If KIFF can't be reached, the guard refuses the call. Each
[framework page](/docs/agno) shows its hook; [How kiff-guard works](/docs/concepts)
explains observe and enforce.

## Call the decision API directly

Right before a consequential call, ask KIFF and branch on the answer.
Your code still runs the action.

```python
import os
import requests

def ask_kiff(*, entity_id, entity_type, action, parameters, actor_id):
    response = requests.post(
        f"{os.environ['KIFF_CLOUD_URL']}/v1/proposals/decide",
        headers={"Authorization": f"Bearer {os.environ['KIFF_CLOUD_API_KEY']}"},
        json={
            "entity_id": entity_id,
            "entity_type": entity_type,
            "action_name": action,
            "actor_id": actor_id,
            "parameters": parameters,
        },
        timeout=5,
    )
    return response.json()

decision = ask_kiff(entity_id=order_id, entity_type="Order", action="ISSUE_REFUND",
                    parameters={"amount": amount}, actor_id="refund-agent")
if decision["outcome"] == "allowed":
    process_refund(order_id, amount)
```

| Outcome | What your code does |
|---|---|
| `allowed` | Run the action. |
| `approval_required` | Hold it: the domain needs an approver. |
| `owner_required` | Hold it: it is outside a Card set to Ask. Decide the same proposal again later. |
| `approved_no_room` | Don't run yet: approved, but a Card total has no room. |
| `blocked` | Don't run it. |
| `invalid` | Fix the request or domain. |
| `limit_exceeded` | Don't run: the account's monthly checks are used up (**Settings → Usage**). |

Branch on `outcome`, not the HTTP status. Where the action draws on a
Card, `actor_id` must be the Card's holder and the key that agent's own
([Keys](/docs/reference-keys#which-key-can-use-a-card)).
