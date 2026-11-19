---
title: "Putting KIFF between an agent and its tools"
kind: "changelog"
date: "2026-10-01"
summary: "The KIFF MCP gateway checks every tool call an agent makes against its Card. Since 2 October it is set up from the dashboard, held calls are emailed to the owner, and a hold nobody answers is refused."
author: "Gabriel Sarmiento"
tags: ["agent-governance", "mcp", "kiff-cards"]
draft: false
---

> **AI;DR:** The MCP gateway puts KIFF between an agent and the remote MCP tools it uses, and checks each call against the agent's Card before forwarding it. Since 2 October the whole setup is in the dashboard, the owner is emailed when a call is held, and a hold nobody answers in time is refused. We tested the route in production with Claude Code and Codex.

*Updated 2 October: setup moved from the API to the dashboard, holds now expire, and held calls are emailed to the owner.*

An agent can ask KIFF whether an action is allowed. But if the agent can also reach the tool directly, it can take that route without asking. We care about closing that gap more than about any single feature, so we built the gateway around it.

The gateway is live at `mcp.kiff.dev`. The agent connects to KIFF over MCP, and you connect your tools to KIFF instead of to the agent. KIFF keeps each tool's credential, encrypted, and the agent gets its own key that can only ask KIFF to use those tools. Once you take away its direct access to the tool, it has no other path to take.

```text
agent ──MCP──▶ KIFF gateway ──MCP──▶ your connected tool
                     │
                     └── checks the agent's Card first
```

KIFF does not run the agent. The agent still decides what to do, and the tool still does it. What changes is that every call passes through a Card on the way: inside it, the call goes through; outside it, the call waits for you or is refused, and nothing reaches the tool.

## Setting it up

On 1 October this was a developer's job: you connected a tool with a hand-written JSON body and an API key. Since 2 October it is three screens in KIFF Cloud, for an account owner or admin.

1. **Connect a tool.** Open Tools and give KIFF the server's address and, if it needs one, its bearer credential. KIFF lists the server's tools with that credential before anything is saved, so a wrong address or credential fails here rather than later. You pick the tool, the name your agents will see, the whole-number argument a Card should limit, and the idempotency key if the tool takes one. You can also mark a tool as one that only reads.
2. **Connect the agent.** On the agent's page, an admin has KIFF create a key for it that can ask for decisions and nothing else, and shows it once inside the configuration to paste into Claude Code or Codex. One key per agent, so every decision names the agent and each key can be revoked on its own.
3. **Give the agent a Card for the tool.** In the tool's own terms: the most per call, a total, a number of calls, a period, whether a call outside the Card waits for you or is refused, and how long a held call waits.

Some things did not make it in. Tools must be remote MCP servers at a public HTTPS address; KIFF refuses private and internal ones. Credentials are bearer tokens: OAuth is not supported yet, and neither are ordinary web APIs. The [MCP gateway guide](/docs/mcp-gateway) has the details, and the API route is still there for anyone who prefers it.

## When the Card says wait

A call outside the Card is held. The agent is told it is waiting for you, with the link where you answer and the time after which it will be refused. Nothing is sent to the tool while it waits.

You are emailed once, when the call is first held: what the agent asked for, the Card it is outside of, and when the hold expires. The email has three buttons, Approve once, Change the Card to allow it, and Reject, and each one opens the action in KIFF, where you sign in and confirm. Nothing can be approved from the email itself, so a forwarded email cannot approve anything.

A hold does not wait forever. Each Card sets how long, 10 minutes unless you choose otherwise, from a minute to a week. The time is fixed when the call is first held, and retries do not extend it. If nobody answers in time, the call is refused and nothing is sent. We added this because an approval given days later should not let a stale request run.

When you approve, you authorize that one request; you do not run the tool. The agent asks again, KIFF answers allowed, and only then does the gateway forward the call. Agents retry, and clients lose responses, so the gateway records every call and forwards each one at most once. If a tool's answer is lost, KIFF marks the result unknown and does not send it again on its own. That is at most once from KIFF, not a promise of exactly once at every remote service; a tool's own idempotency key adds that safeguard.

Tools marked as read-only are treated differently. Identical calls to a tool that acts, made within ten minutes without an operation id, are done once. For a read-only tool that would hand the agent a stale answer, so a read that comes after the previous one has finished reaches the tool again. It is still checked against the Card.

## Tested on the live gateway

On 1 October we connected Claude Code and Codex to the production gateway, each with its own agent key and Card, and pointed them at a temporary MCP test tool. Claude Code sent €400, which was allowed, and €4,500, which was held. Codex sent €300, allowed, and €6,000, held. For both held calls, after the owner approved once, two retries produced a single call at the tool, and the Cards' statements matched the decisions. No customer transaction took place.

The same path is now a smoke test we re-run after changes: an allowed call, a held call answered under ten concurrent retries, repeats without an operation id, read-only tools, a hold that expires, and both MCP protocol revisions. It counts the calls at the tool itself rather than trusting what the agent was told.

## The boundary still matters

The Card applies to calls that go through KIFF. If the agent still has the tool's credential, or a shell or browser that reaches the same service, KIFF cannot govern that route. Connect the tool to KIFF and remove the direct path if you want the gateway to be the boundary.

The gateway is one way in. If your team owns the agent's code, [kiff-guard](/docs/connect-an-agent) sits at its tool-call hook. If you are wiring one action into your own service, the [decision API](/docs/connect-an-agent#call-the-decision-api-directly) shows the decision path.

To try it: sign in to [KIFF Cloud](https://app.kiff.dev/tools), connect a tool, connect your agent from its page, and give it a Card for that tool. Then ask it for something just outside the Card and watch the email arrive.
