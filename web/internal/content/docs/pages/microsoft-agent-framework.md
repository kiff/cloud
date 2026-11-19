# Microsoft Agent Framework

The kiff-guard hook for Microsoft Agent Framework: each tool call is checked against the agent's [Card](/docs/kiff-cards) before it runs.
For builders whose agent runs on Microsoft Agent Framework; an agent that calls MCP tools can use the [MCP gateway](/docs/mcp-gateway) with no code.
Add `kiff_guard_middleware(guard)` in `middleware`, start in `observe`, then switch to `enforce`.

**Status: verified** against the `agent-framework-core` package; passes the conformance suite. **Shape:** async middleware, via `FunctionMiddleware` ([how kiff-guard works](/docs/concepts)).

## Install

```bash
pip install "git+https://github.com/kiff/kiff-guard.git#subdirectory=packages/python/kiff-guard"
pip install agent-framework-core
```

## Wire it

```python
from agent_framework import Agent
from kiff_guard import Guard
from kiff_guard.adapters.microsoft_agent_framework import kiff_guard_middleware

guard = Guard(mode="observe")

agent = Agent(client=..., name="assistant", tools=[refund_order],
              middleware=[kiff_guard_middleware(guard)])
```

To enforce, build the guard as in [Switch to enforce](/docs/concepts#switch-to-enforce), with a key from the [dashboard](/dashboard). A withheld call sets `context.result` to the reason and skips `call_next`; the tool never runs.

Next: [Connect an agent](/docs/connect-an-agent). Code: [`adapters/microsoft_agent_framework.py`](https://github.com/kiff/kiff-guard/blob/main/packages/python/kiff-guard/src/kiff_guard/adapters/microsoft_agent_framework.py).
