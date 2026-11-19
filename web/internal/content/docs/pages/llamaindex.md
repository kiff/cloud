# LlamaIndex Agents

The kiff-guard hook for LlamaIndex: each tool call is checked against the agent's [Card](/docs/kiff-cards) before it runs.
For builders whose agent runs on LlamaIndex; an agent that calls MCP tools can use the [MCP gateway](/docs/mcp-gateway) with no code.
Add `GuardedAgentWorkflow` in place of `AgentWorkflow`, start in `observe`, then switch to `enforce`.

**Status: verified** against `llama-index-core`; passes the conformance suite. **Shape:** middleware, overriding the `call_tool` step ([how kiff-guard works](/docs/concepts)).

## Install

```bash
pip install "git+https://github.com/kiff/kiff-guard.git#subdirectory=packages/python/kiff-guard"
pip install "kiff-guard[llama-index]"
```

## Wire it

```python
from llama_index.core.agent.workflow import FunctionAgent
from llama_index.llms.openai import OpenAI
from kiff_guard import Guard
from kiff_guard.adapters.llama_index import GuardedAgentWorkflow

guard = Guard(mode="observe")

workflow = GuardedAgentWorkflow(
    agents=[FunctionAgent(tools=[refund_order], llm=OpenAI(model="gpt-4o-mini"))],
    guard=guard,
)
```

To enforce, build the guard as in [Switch to enforce](/docs/concepts#switch-to-enforce), with a key from the [dashboard](/dashboard). A withheld call raises `kiff_guard.Hold` inside `call_tool`; the tool never runs.

Next: [Connect an agent](/docs/connect-an-agent). Code: [`adapters/llama_index.py`](https://github.com/kiff/kiff-guard/blob/main/packages/python/kiff-guard/src/kiff_guard/adapters/llama_index.py).
