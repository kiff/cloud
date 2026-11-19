# Strands Agents

The kiff-guard hook for Strands: each tool call is checked against the agent's [Card](/docs/kiff-cards) before it runs.
For builders whose agent runs on Strands; an agent that calls MCP tools can use the [MCP gateway](/docs/mcp-gateway) with no code.
Add `kiff_hook_provider(guard)` in `hooks`, start in `observe`, then switch to `enforce`.

**Status: verified** against the `strands-agents` package; passes the conformance suite. **Shape:** vote, via `BeforeToolCallEvent` ([how kiff-guard works](/docs/concepts)).

## Install

```bash
pip install "git+https://github.com/kiff/kiff-guard.git#subdirectory=packages/python/kiff-guard"
pip install strands-agents
```

## Wire it

```python
from strands import Agent
from kiff_guard import Guard
from kiff_guard.adapters.strands import kiff_hook_provider

guard = Guard(mode="observe")

agent = Agent(model=..., tools=[refund_order], hooks=[kiff_hook_provider(guard)])
```

To enforce, build the guard as in [Switch to enforce](/docs/concepts#switch-to-enforce), with a key from the [dashboard](/dashboard). A withheld call sets `event.cancel_tool` to the reason; Strands cancels the call.

Next: [Connect an agent](/docs/connect-an-agent). Code: [`adapters/strands.py`](https://github.com/kiff/kiff-guard/blob/main/packages/python/kiff-guard/src/kiff_guard/adapters/strands.py).
