# LangGraph / LangChain

The kiff-guard hook for LangGraph or LangChain: each tool call is checked against the agent's [Card](/docs/kiff-cards) before it runs.
For builders whose agent runs on LangGraph or LangChain; an agent that calls MCP tools can use the [MCP gateway](/docs/mcp-gateway) with no code.
Add `kiff_wrap_tool_call(guard)` as `wrap_tool_call` middleware, start in `observe`, then switch to `enforce`.

**Status: verified** against the current LangChain middleware API; passes the conformance suite. **Shape:** middleware ([how kiff-guard works](/docs/concepts)).

## Install

```bash
pip install "git+https://github.com/kiff/kiff-guard.git#subdirectory=packages/python/kiff-guard"
pip install langgraph langchain
```

## Wire it

```python
from langchain.agents import create_agent
from langchain.agents.middleware import wrap_tool_call
from kiff_guard import Guard
from kiff_guard.adapters.langgraph import kiff_wrap_tool_call

guard = Guard(mode="observe")
kiff_mw = wrap_tool_call(kiff_wrap_tool_call(guard))

agent = create_agent(model="...", tools=[...], middleware=[kiff_mw])
```

To enforce, build the guard as in [Switch to enforce](/docs/concepts#switch-to-enforce), with a key from the [dashboard](/dashboard). A withheld call returns a `ToolMessage` with `status="error"` and the reason; the tool never runs.

Next: [Connect an agent](/docs/connect-an-agent). Code: [`adapters/langgraph.py`](https://github.com/kiff/kiff-guard/blob/main/packages/python/kiff-guard/src/kiff_guard/adapters/langgraph.py).
