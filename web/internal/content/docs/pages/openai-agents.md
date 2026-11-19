# OpenAI Agents SDK

The kiff-guard hook for the OpenAI Agents SDK: each tool call is checked against the agent's [Card](/docs/kiff-cards) before it runs.
For builders whose agent runs on the OpenAI Agents SDK; an agent that calls MCP tools can use the [MCP gateway](/docs/mcp-gateway) with no code.
Add `kiff_tool_input_guardrail(guard)` on each `function_tool`, start in `observe`, then switch to `enforce`.

**Status: verified** against `openai-agents` v0.17.4 and a live model call; passes the conformance suite. **Shape:** vote, via a tool input guardrail ([how kiff-guard works](/docs/concepts)).

## Install

```bash
pip install "git+https://github.com/kiff/kiff-guard.git#subdirectory=packages/python/kiff-guard"
pip install openai-agents
```

## Wire it

```python
from agents import Agent, function_tool
from kiff_guard import Guard
from kiff_guard.adapters.openai_agents import kiff_tool_input_guardrail

guard = Guard(mode="observe")
kiff_gd = kiff_tool_input_guardrail(guard)

@function_tool(tool_input_guardrails=[kiff_gd])
def refund_order(order_id: str, amount_cents: int) -> str:
    ...

agent = Agent(name="support", tools=[refund_order])
```

To enforce, build the guard as in [Switch to enforce](/docs/concepts#switch-to-enforce), with a key from the [dashboard](/dashboard). A withheld call returns `reject_content(reason)`; the SDK skips the tool.

Guardrails cover `function_tool`s only; hosted tools and handoffs bypass them.

Next: [Connect an agent](/docs/connect-an-agent). Code: [`adapters/openai_agents.py`](https://github.com/kiff/kiff-guard/blob/main/packages/python/kiff-guard/src/kiff_guard/adapters/openai_agents.py).
