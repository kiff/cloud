# Hermes Agent (Nous Research)

The kiff-guard hook for Hermes: each tool call is checked against the agent's [Card](/docs/kiff-cards) before it runs.
For builders whose agent runs on Hermes; an agent that calls MCP tools can use the [MCP gateway](/docs/mcp-gateway) with no code.
Add `register_kiff_guard(ctx, guard)` in a plugin, start in `observe`, then switch to `enforce`.

**Status: verified** against the `NousResearch/hermes-agent` source; passes the conformance suite. **Shape:** vote, via the `pre_tool_call` plugin hook ([how kiff-guard works](/docs/concepts)).

## Install

```bash
pip install "git+https://github.com/kiff/kiff-guard.git#subdirectory=packages/python/kiff-guard"
```

## Wire it

Create the plugin at `~/.hermes/plugins/kiff-guard/` (a `plugin.yaml` and this `__init__.py`):

```python
from kiff_guard import Guard
from kiff_guard.adapters.hermes import register_kiff_guard

_GUARD = Guard(mode="observe")

def register(ctx):
    register_kiff_guard(ctx, _GUARD)
```

To enforce, build the guard as in [Switch to enforce](/docs/concepts#switch-to-enforce), with a key from the [dashboard](/dashboard). A withheld call returns Hermes' `{"action": "block", "message": ...}`; the tool never runs.

Next: [Connect an agent](/docs/connect-an-agent). Code: [`adapters/hermes.py`](https://github.com/kiff/kiff-guard/blob/main/packages/python/kiff-guard/src/kiff_guard/adapters/hermes.py).
