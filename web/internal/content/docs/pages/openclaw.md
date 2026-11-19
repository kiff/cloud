# OpenClaw

The kiff-guard hook for OpenClaw: each tool call is checked against the agent's [Card](/docs/kiff-cards) before it runs.
For builders whose agent runs on OpenClaw; an agent that calls MCP tools can use the [MCP gateway](/docs/mcp-gateway) with no code.
Register `registerKiffGuard(api, guard)` in your plugin, start in `observe`, then switch to `enforce`.

**Status: verified** against the OpenClaw plugin SDK's `before_tool_call` hook; passes the conformance suite ported from the Python guard. **Shape:** vote, in the TypeScript guard SDK ([how kiff-guard works](/docs/concepts)).

## Wire it

```ts
import { definePluginEntry } from "openclaw/plugin-sdk/plugin-entry";
import { Guard } from "@kiff/kiff-guard";
import { registerKiffGuard } from "@kiff/kiff-guard/adapters/openclaw";

const guard = new Guard({ mode: "observe" }); // switch to enforce when ready

export default definePluginEntry({
  id: "kiff-guard",
  name: "KIFF Guard",
  register(api) {
    registerKiffGuard(api, guard); // before_tool_call, priority 50
  },
});
```

In enforce mode, `allowed` runs the tool, `approval_required` returns `requireApproval` so OpenClaw
routes its own `/approve` flow (its `timeoutMs` fails closed), and anything else returns
`{ block: true }`. If the guard can't reach KIFF, the tool is blocked. Mint the key in the
[dashboard](/dashboard).

Next: [Connect an agent](/docs/connect-an-agent). Code: [`adapters/openclaw.ts`](https://github.com/kiff/kiff-guard/blob/main/packages/js/src/adapters/openclaw.ts).
