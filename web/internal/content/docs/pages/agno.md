# Agno

The kiff-guard hook for Agno: each tool call is checked against the agent's [Card](/docs/kiff-cards) before it runs.
For builders whose agent runs on Agno; an agent that calls MCP tools can use the [MCP gateway](/docs/mcp-gateway) with no code.
Add `agno_hook(guard)` in `tool_hooks`, start in `observe`, then switch to `enforce`.

**Status: verified** verified live against the cloud decide endpoint; passes the conformance suite. **Shape:** middleware ([how kiff-guard works](/docs/concepts)).

## Install

```bash
pip install "git+https://github.com/kiff/kiff-guard.git#subdirectory=packages/python/kiff-guard"
pip install agno
```

## Wire it

```python
from agno.agent import Agent
from kiff_guard import Guard
from kiff_guard.adapters.agno import agno_hook

guard = Guard(mode="observe")

agent = Agent(model=..., tools=[refund_order], tool_hooks=[agno_hook(guard)])
```

To enforce, build the guard as in [Switch to enforce](/docs/concepts#switch-to-enforce), with a key from the [dashboard](/dashboard). A withheld call raises `kiff_guard.Hold`; the tool never runs.

Next: [Connect an agent](/docs/connect-an-agent). Code: [`adapters/agno.py`](https://github.com/kiff/kiff-guard/blob/main/packages/python/kiff-guard/src/kiff_guard/adapters/agno.py).
