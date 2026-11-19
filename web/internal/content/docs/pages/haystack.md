# Haystack Agents

The kiff-guard hook for Haystack: each tool call is checked against the agent's [Card](/docs/kiff-cards) before it runs.
For builders whose agent runs on Haystack; an agent that calls MCP tools can use the [MCP gateway](/docs/mcp-gateway) with no code.
Add `kiff_confirmation_strategy(guard)` for each tool, start in `observe`, then switch to `enforce`.

**Status: verified** against the `haystack-ai` package; passes the conformance suite. **Shape:** vote, via a `ConfirmationStrategy` ([how kiff-guard works](/docs/concepts)).

## Install

```bash
pip install "git+https://github.com/kiff/kiff-guard.git#subdirectory=packages/python/kiff-guard"
pip install haystack-ai
```

## Wire it

```python
from haystack.components.agents import Agent
from kiff_guard import Guard
from kiff_guard.adapters.haystack import kiff_confirmation_strategy

guard = Guard(mode="observe")

agent = Agent(chat_generator=..., tools=[refund_order],
              confirmation_strategies={"refund_order": kiff_confirmation_strategy(guard)})
```

To enforce, build the guard as in [Switch to enforce](/docs/concepts#switch-to-enforce), with a key from the [dashboard](/dashboard). A withheld call returns `execute=False` with the reason; the tool never runs.

Next: [Connect an agent](/docs/connect-an-agent). Code: [`adapters/haystack.py`](https://github.com/kiff/kiff-guard/blob/main/packages/python/kiff-guard/src/kiff_guard/adapters/haystack.py).
