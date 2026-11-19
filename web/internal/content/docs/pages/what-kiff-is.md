# What KIFF is

KIFF decides what each AI agent may do before it acts.
For a business that runs agents against its tools and data.
Give each agent a Card; change it any time without redeploying.

Each agent gets a [KIFF Card](/docs/kiff-cards): which actions, how
much, and until when. Before an action runs, KIFF answers `allowed` or
why not, and signs a receipt
([the answers](/docs/connect-an-agent#call-the-decision-api-directly)).

Only an owner or admin can issue, change or revoke a Card. The agent's
own key can only ask.

## What KIFF is not

KIFF is the **decision, not the runner.**
It does **not** execute your action. With the decision API, your system
runs the action after `allowed`. With the [MCP gateway](/docs/mcp-gateway), KIFF forwards an allowed call to
the connected tool ([Keep tool keys out of KIFF](/docs/tool-permits)).

## Limits to know

- **A Card constrains; it never confers.** It cannot grant an action the
  [domain](/docs/domains) does not have.
- **A Card is required only where the action says so.** An action that
  declares `requires_authority` is refused with `no_mandate_issued` when
  the agent holds no Card for it. Through the MCP gateway, a call no Card
  covers is refused.
- **A draw is recorded when the action is authorized, not when it
  runs.** See [when a draw is recorded and given back](/docs/kiff-cards#when-a-draw-is-recorded-and-given-back).
- **Only calls routed through KIFF are checked.** If the agent can reach
  the same tool directly, through a shell or a browser, that path is
  outside its Card. Remove that access.
- **Receipts are evidence, not a legal opinion.**

Next: [Quickstart](/docs/quickstart).
