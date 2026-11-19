---
title: "What happens when the agent skips the check?"
kind: "essay"
date: "2026-09-14"
summary: "A separate policy service only helps if the action has to pass through it; an agent with the original credentials may still have another way in."
author: "Gabriel Sarmiento"
tags: ["agent-governance", "go", "architecture"]
draft: true
---

> **AI;DR:** An agent can follow every rule in your prompt and still have too much access. Before trusting a safety check, find out whether the agent can do the same thing without it.

Imagine a refund tool that asks for approval before sending money. Now give
the same agent a terminal and the payment API key. It has a second route to
the money, even if the refund tool works perfectly.

No clever prompt is needed to create this problem. Two ways to do the same
job are enough.

The software around a model is often called its *harness*: it supplies tools,
keeps track of work, and decides how to handle retries and errors. Good
harnesses matter. Anthropic's [work on long-running agents](https://www.anthropic.com/engineering/effective-harnesses-for-long-running-agents)
describes using progress files, feature lists, and testing to help agents
continue reliably across sessions.

That solves a real problem: getting work done consistently. Access control
adds another question: what work can happen without permission?

## We had a list of tools, but no picture of the gaps

Connected KIFF runtimes already reported their tools and call counts. We
used that data to suggest rules worth writing. It could also answer a more
urgent question: which reported tools had no rule at all?

We added a coverage report by comparing that catalog with active controls.
For matched actions, it also compares reported calls with recorded decisions.
That gives an integrator a way to investigate calls the gate may have missed.

We put the limits in the response itself: it only measures instrumented
tools. A shell command using a separate payment key is outside that picture.
An empty gap in the report cannot establish that no other route exists.

KIFF Cloud makes decisions outside the agent's process. Guard integrations
check wrapped tool calls before execution in enforce mode. Both are useful.
Neither removes a payment key sitting elsewhere in the agent's environment.

## Test the other route

For a refund integration, review the refund tool and the credentials together.
Can the agent call the payment provider directly? Can it change the wrapper?
What happens if the decision service times out?

A separate service gives the policy its own deployment and authority. The
integration must make refusal stick: withhold execution, restrict credentials,
and close alternative routes where that level of protection is required.

That's the part to demonstrate before calling an action governed. A successful
“blocked” response only shows that one request reached the check.

*Drafted by an assistant, reviewed by a person before anything here ships.*
