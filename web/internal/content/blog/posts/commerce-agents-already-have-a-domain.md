---
title: "The AI shopkeeper gave away the tungsten cube"
kind: "essay"
date: "2026-09-14"
summary: "An experimental AI shop sold goods below cost and gave items away; commerce agents need explicit limits on pricing, discounts, and total spending."
author: "Gabriel Sarmiento"
tags: ["agent-governance", "domain-modeling"]
draft: true
---

> **AI;DR:** An AI running an office shop got talked into discounts and gave products away. Let an agent suggest prices, but make the store's rules decide which changes can go live.

An AI shopkeeper gave away a tungsten cube. It was supposed to make money.

In the first phase of [Project Vend](https://www.anthropic.com/research/project-vend-1),
published in June 2025, Anthropic and Andon Labs put Claude in charge of a
small shop in Anthropic's office. It could research stock, talk to customers,
and change checkout prices.

Employees requested metal cubes. The agent found suppliers, but also priced
items below cost and let people talk it into discounts. It offered a 25%
employee discount in an office where, as one employee pointed out, nearly
everyone qualified. After announcing it would stop offering discounts, it
returned to them within days.

This was a small experiment with unusually mischievous customers, not a study
of typical retail performance. But the operational problem is familiar to
anyone who has managed a promotion: who is allowed to give away the margin?

## Start with the rules the store already has

A merchant doesn't need to invent an “AI policy” from scratch. Ask who can
change a price today, how far they can reduce it, and who signs off on an
exception.

Consider a proposed rule: a price below the recorded cost needs a manager's
approval. The agent can research demand and recommend that price. The system
publishing it checks the cost and approval before making it live.

KIFF's existing [refund example](/docs/framework-quickstart) uses the same pattern for
a different action: the agent proposes, the runtime checks, and a risky
request waits for a human. Cloud also provides limits across actions,
because many individually small concessions can exhaust a budget.

A pricing domain would still need work. Which cost includes shipping? Can
two promotions stack? Who may approve a loss-making clearance sale? We
haven't supplied those retail answers by shipping a refund demo.

The useful surprise in the shop experiment is how ordinary several failures
were. Discounts, bad margins, and inconsistent follow-through already have
names in retail. Put those rules on the path to the checkout system, where
a price actually changes.

That could constrain a configured class of bad changes. It wouldn't tell
the agent which products customers will buy.

*Drafted by an assistant, reviewed by a person before anything here ships.*
