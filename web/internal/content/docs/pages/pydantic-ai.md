# Pydantic AI

The kiff-guard hook for Pydantic AI: each tool call is checked against the agent's [Card](/docs/kiff-cards) before it runs.
For builders whose agent runs on Pydantic AI; an agent that calls MCP tools can use the [MCP gateway](/docs/mcp-gateway) with no code.
Add `kiff_before_tool_execute(guard)` in a `Hooks` capability, start in `observe`, then switch to `enforce`.

**Status: verified** against the `pydantic-ai` package; passes the conformance suite. **Shape:** vote ([how kiff-guard works](/docs/concepts)).

## Install

```bash
pip install "git+https://github.com/kiff/kiff-guard.git#subdirectory=packages/python/kiff-guard"
pip install pydantic-ai
```

## Wire it

```python
from pydantic_ai import Agent
from pydantic_ai.capabilities import Hooks
from kiff_guard import Guard
from kiff_guard.adapters.pydantic_ai import kiff_before_tool_execute

guard = Guard(mode="observe")

agent = Agent("...", tools=[refund_order],
              capabilities=[Hooks(before_tool_execute=kiff_before_tool_execute(guard))])
```

To enforce, build the guard as in [Switch to enforce](/docs/concepts#switch-to-enforce), with a key from the [dashboard](/dashboard). A withheld call raises `SkipToolExecution`; the tool never runs.

Next: [Connect an agent](/docs/connect-an-agent). Code: [`adapters/pydantic_ai.py`](https://github.com/kiff/kiff-guard/blob/main/packages/python/kiff-guard/src/kiff_guard/adapters/pydantic_ai.py).
