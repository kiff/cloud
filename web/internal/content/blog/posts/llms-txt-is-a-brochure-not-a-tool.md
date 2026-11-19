---
title: "An agent can read your website. What can it actually do?"
kind: "essay"
date: "2026-09-14"
summary: "Writing KIFF's llms.txt made the product easier to discover; designing an MCP adapter exposed the separate question of what an agent must be allowed to call."
author: "Gabriel Sarmiento"
tags: ["agent-governance", "architecture"]
draft: true
---

> **AI;DR:** Our llms.txt helps an agent find the right docs. It doesn't give the agent a working tool or permission to act. We explored that next step and found a reason to pause.

You can read a restaurant menu without being able to place an order.
An agent reading a company's website has the same problem.

We publish [llms.txt](/llms.txt), a short map of KIFF's public pages, and an
[agent entry page](/agent) that explains the decision API. They help an
assistant find the right information before it writes an integration.

The [llms.txt proposal](https://llmstxt.org/) describes a Markdown file with
background, guidance, and links. That is a useful job. The file doesn't
register a tool in the reader's environment, supply credentials, or make
an API call.

## The missing step was more than a schema

We investigated an MCP adapter so assistants could discover KIFF operations
as tools. MCP gives a tool a structured description and inputs, reducing
the amount an assistant has to reconstruct from prose.

Our design reached a less convenient question: if we offer a
“may I do this?” tool, what makes the assistant use it?

Nothing about discovery does. An assistant can call the permission tool and
respect its answer. It can also use another available route to the action.
Giving it a better description doesn't remove that route.

We deferred the adapter in September. The design record puts the reason
plainly: it would offer a governance interface a cooperative agent uses.
That was a weaker property than enforcing a check on the execution path,
so runtime work came first.

## Reading, calling, and permission each need a check

Even our text file has a modest maintenance burden. It's hand-maintained,
and a test rejects private dashboard and sign-in routes. That protects the
public map from accidentally listing private surfaces; it doesn't prove
every description stays current.

A callable interface adds more obligations: validate inputs, authenticate
the caller, restrict its authority, and keep the schema aligned with the
implementation.

Those are worth doing when the interface earns its place. Our current
machine-facing pages help with discovery, and integrations can use the
documented API. They don't make browsing KIFF equivalent to connecting to it.

If you're making your own product easier for agents to use, try completing
one real task from its public documentation. Count the missing steps
between finding the endpoint and being authorized to call it. That's a more
useful backlog than simply adding another machine-readable file.

*Drafted by an assistant, reviewed by a person before anything here ships.*
