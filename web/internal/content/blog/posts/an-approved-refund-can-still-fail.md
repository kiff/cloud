---
title: "An approved refund can still fail"
kind: "changelog"
date: "2026-10-09"
summary: "KIFF records the tool’s response beside its authorization decision and brings pending approvals back into the agent’s session through MCP."
author: "Gabriel Sarmiento"
tags: ["mcp", "kiff-cards", "agent-governance"]
related: ["from-a-refund-tool-to-a-governed-mcp-agent", "outside-the-card-the-work-comes-home"]
draft: false
---

> **AI;DR:** A person can approve a refund that the payment provider later refuses. KIFF keeps those two facts separate, lists pending operations through MCP, and returns the owner’s answer to the agent through the optional Claude Code plugin. The example below is illustrative. Drafted by an assistant, reviewed by a person before publication.

A support agent asks to refund €25. Its KIFF Card requires a person to review that request, so KIFF holds the call. The person approves it. The agent collects the approval and the refund tool receives the call.

Then the payment provider refuses it: only €20 remains refundable on the charge.

What should the person see? “Approved once, done” would be wrong. They approved the request; the provider did not perform the refund. Those are different facts, and a useful record needs to show both.

That distinction is the starting point for three changes to KIFF's MCP workflow: recording the tool's answer alongside authorization, recovering pending calls from the gateway, and bringing the owner's answer back into the agent's session.

## Approval records authority; the tool reports the outcome

KIFF's decision answers whether the agent is allowed to request the action under the authority it was given. The tool's response tells us what the tool reports happened afterward.

Needs you and Activity now have separate fields for those facts. A consumed approval can read “Approved once, refused by the tool,” with the tool's error shown beside it. A successful response is described as the tool reporting the call done. A lost response remains an unknown outcome.

This distinction also matters for older records. Where an older gateway kept the tool response, KIFF can reconstruct what it reported. Where that response has already been purged and no outcome was recorded, the record says so. Missing evidence cannot establish success.

## Pending work survives the conversation

A held request can outlast an agent's current conversation. The owner might approve it while the agent is disconnected, or the agent might return after losing its local context.

The gateway's `kiff_pending` tool lists that credential's held calls, calls being sent and calls with an unknown outcome. Held calls include the owner's answer once available, and the original arguments needed to collect the operation. Reading the list sends nothing to the business tool and draws nothing from the Card.

For an MCP `tools/call` request, the parameters are:

```json
{
  "name": "kiff_pending",
  "arguments": {}
}
```

When the response includes `next_cursor`, the client passes it as `arguments.cursor` to read the next page. The gateway returns 50 entries at a time; the next page reaches older pending work.

The original arguments matter. A retry that changes them is a different request. KIFF lists what the agent originally sent, including any tool idempotency argument it supplied, while the downstream tool still receives KIFF's own idempotency value.

For a call made without `kiff_operation_id`, the pending list supplies KIFF's operation key so the original held call can be collected after the usual identical-call retry window. Collection still checks the credential, tool and arguments.

## The owner's answer comes back to the agent

The optional KIFF plugin for Claude Code checks `kiff_pending` while this session has a held call. When it reads an approval or refusal, it shows a notice and asks Claude Code to start a turn conveying that answer. The agent decides whether to collect the approved call.

The plugin does not approve the request or invoke the held business tool itself. Authority remains in KIFF, and execution follows the agent's own retry of the same operation.

An approval given in time still counts when the plugin reads it late. If a permission refusal stopped background reads, `/kiff` provides a way to resume checking. If the connection was unavailable, a later read can apply the owner's answer. Local polling expiry controls how long the plugin keeps checking; it cannot invalidate an authoritative approval it subsequently reads.

Each check reads a bounded number of pages and keeps its cursor for the next check. Older holds can therefore be found even behind more than 500 newer pending calls. An unavailable owner answer does not complete the final check: recovery continues until KIFF can read the answer. An approval is announced once, however late it is read.

## A record you can act on

For the refund example, the useful sequence is: the request was held, a person approved it, the agent collected that operation, and the provider refused the refund. The owner can then investigate the charge without mistaking approval for payment.

Outcome recording does not itself refund a Card's budget. The RFC 038 amendment sets out what each limit measures and when trusted evidence of no effect could restore an effect amount. That implementation is separate work; a generic tool error is not enough to prove that nothing happened.

If you are building a governed agent workflow, keep the authorization decision, the pending operation and the tool's reported result connected. They answer three practical questions: who allowed this, what still needs attention, and what did the tool report?

Read the [MCP integration guide](https://kiff.dev/docs/mcp-gateway) and [KIFF plugin instructions](https://github.com/kiff/kiff-plugins#optional-see-the-card-in-claude-code).
