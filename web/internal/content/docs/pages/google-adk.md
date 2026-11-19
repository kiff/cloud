# Google ADK

The kiff-guard hook for Google ADK: each tool call is checked against the agent's [Card](/docs/kiff-cards) before it runs.
For builders whose agent runs on Google ADK; an agent that calls MCP tools can use the [MCP gateway](/docs/mcp-gateway) with no code.
Add `kiff_before_tool_callback(guard)` as `before_tool_callback`, start in `observe`, then switch to `enforce`.

**Status: verified** against the `google-adk` package; passes the conformance suite. **Shape:** vote ([how kiff-guard works](/docs/concepts)).

## Install

```bash
pip install "git+https://github.com/kiff/kiff-guard.git#subdirectory=packages/python/kiff-guard"
pip install google-adk
```

## Wire it

```python
from google.adk.agents import LlmAgent
from kiff_guard import Guard
from kiff_guard.adapters.google_adk import kiff_before_tool_callback

guard = Guard(mode="observe")

agent = LlmAgent(model=..., tools=[refund_order],
                 before_tool_callback=kiff_before_tool_callback(guard))
```

To enforce, build the guard as in [Switch to enforce](/docs/concepts#switch-to-enforce), with a key from the [dashboard](/dashboard). A withheld call returns a `dict`, so ADK skips the tool and hands the reason to the model.

Next: [Connect an agent](/docs/connect-an-agent). Code: [`adapters/google_adk.py`](https://github.com/kiff/kiff-guard/blob/main/packages/python/kiff-guard/src/kiff_guard/adapters/google_adk.py).
