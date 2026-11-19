---
title: "Every refund passed. The total was still too much."
kind: "essay"
date: "2026-09-14"
summary: "A per-refund limit can work perfectly while the day's total exceeds your budget; this is why KIFF added limits across multiple decisions."
author: "Gabriel Sarmiento"
tags: ["agent-governance", "audit"]
draft: true
---

> **AI;DR:** A hundred allowed 40-dollar refunds still add up to 4,000 dollars. Checking each refund won't protect a daily budget of 1,000 dollars unless you write that rule down too.

Suppose you let a support agent refund up to 50 dollars without asking. It issues
100 refunds of 40 dollars. Every order is eligible. Every amount is within the
limit. You had 1,000 dollars in mind for the day.

The agent has authorized 4,000 dollars without breaking the rule you gave it.

This is an example, not a customer incident. It's also the arithmetic behind
a real addition to KIFF Cloud: limits that span several decisions.

## The rule was missing a total

Our refund quickstart demonstrates checks on individual actions: allow the
eligible refund, hold a risky one for approval, refuse a repeat on an
already-refunded order. Those checks are worth having. They don't answer how
much authority the agent has used across different orders.

Cloud's [limits](/docs/limits) now let an operator cap a count or a sum over a
time window. For the example above, the first 25 refunds use the 1,000-dollar
allowance. The next is blocked, assuming no other draws and all the other
checks pass.

One implementation choice makes the tradeoff tangible: we count when KIFF
authorizes an action. The customer's system executes it, so we don't have
confirmed spending to count instead. An authorized refund that never runs
still uses allowance in that window.

That can leave an agent with less room than it actually spent. The statement
says what it measures: authorized amounts.

## The system cannot supply the rule you forgot

Even with a daily ceiling, imagine an order under fraud review. If that fact
never reaches the domain, the runtime cannot use it to refuse a refund.
A signed receipt faithfully recording “allowed” won't repair the omission.

This is testable once someone identifies the case. Give the domain an order
under review and assert that the refund is refused. The difficult part is
getting that case from the people who handle exceptions every day.

Before increasing an agent's authority, take a handful of past decisions
your operations team disputed and run them against the rules. Inspect the
ones that pass. A green check is useful evidence about the rule you wrote;
it still leaves you responsible for deciding whether that rule is enough.

*Drafted by an assistant, reviewed by a person before anything here ships.*
