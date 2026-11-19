---
title: "tafs: from one sentence to a refund, inside a Card"
kind: "report"
date: "2026-10-22"
summary: "A taf set up from one sentence on an iPad, with 50 € per refund and 100 € a day, refunded a €0.50 test payment and had a $300 one refused for its currency."
author: "Gabriel Sarmiento"
tags: ["tafs", "kiff-cards", "agent-governance"]
draft: false
---

> **AI;DR:** tafs is our iPhone and iPad app for AI teammates, each called a taf. Last night the owner typed one sentence into it: *refund Stripe payments when I ask, test mode, at most 50 € per refund and 100 € a day, no schedule.* tafs set up the taf, connected Stripe without the taf ever seeing the sign-in, and gave it a KIFF Card with exactly those limits. Asked to refund €0.50, it did. Asked to refund $300, KIFF refused it before anything reached Stripe: the payment was in dollars and the Card counts euros. Stripe test mode shows one refund of €0.50 and nothing else. This post says what worked, what broke on the way, and what we changed. Written by the assistant that ran the test with the owner, at the owner's request.

> **Update:** Since 10 Nov 2026, tafs is on Google Play in Europe, the UK and Switzerland; on iPhone and iPad it's on TestFlight. The post below is as it was written.

## What a taf is

A taf does one routine job for a company: refunds, bills, chargebacks,
access requests. It works in its chat, or on a schedule. What it may do is
written on a **KIFF Card**: which actions, how much per action, how much
in a day, and what happens to anything outside that. Outside the Card, an
action is either held for a person to answer or refused. KIFF checks every
call against the Card before it reaches the tool, and records the
decision.

Three pieces do the work:

- **tafs**, the app, where you describe the job, answer what needs you,
  and read the Record of everything your tafs did.
- **KIFF**, which decides each call against the taf's Card, holds or
  refuses what is outside it, and signs a receipt for each decision.
- **planque**, which keeps the vendor sign-in (here, Stripe) sealed, and
  sends a call to the vendor only with KIFF's permit for that exact call.
  The taf never holds a key.

## The run

The owner opened **Coach mode** on the iPad, started a new conversation
and typed the job in one sentence. Setup, the assistant that builds tafs,
asked two questions together: may it connect Stripe, and should a refund
over the limits be held or refused? *Hold it for me.*

Then it worked through a plan it kept on screen, six steps from
*Connect your Stripe* to *Hand you the taf*:

1. The owner approved the connection and signed in to Stripe, once.
2. Setup asked for two small read-only tools that only it uses while
   setting up: one that reads Stripe's documentation for an operation's
   fields, and one that lists the accounts the sign-in covers, so the taf
   is pinned to one account and to test mode.
3. It chose three tools for the taf: list payments, read one payment, and
   refund a payment. It set the refund's amount and the payment it acts
   on as required arguments, so no call can leave them out.
4. It proposed the rules in plain words (50 € per refund, 100 € a day, a
   day ending at midnight in the account's time zone, euros only, never
   payouts or charges) and the owner agreed.
5. The owner approved the tools, then the Card. Each approval asks for
   Touch ID.

Then, in the new taf's chat:

| Asked | What happened |
|---|---|
| Refund `pi_3USzxZ…` in full (€0.50) | The taf read the charge (0,50 €, nothing refunded yet) and refunded it. KIFF allowed it inside the Card, and the Card showed 99,50 € left today. |
| Refund `pi_3USzr7…` in full ($300) | Refused, not sent. KIFF read the payment's currency before deciding: US dollars, while the Card counts euros. The taf said so and suggested changing the Card. |

Stripe test mode afterwards: one refund on the first payment, 50 cents,
succeeded. Nothing on the second.

Both calls are on the Record, with what they acted on and why. The
allowed one carries KIFF's receipt, and the app checks its signature on
the iPad itself: *the signature matches KIFF's address and none of its
steps has changed since.* You don't have to take the app's word, or ours,
for what happened.

## What broke on the way

The same setup failed the night before, and not on anything Stripe did.

**Setup forgot the setup.** A taf's chat is a series of runs: every
message, answer and approval is one. The agent runtime gave the model the
last ten runs and nothing before them. A setup with a few retries passed
ten, and Setup lost the owner's request in the middle of it. It now gets
its earlier runs up to a size, and summarises the part that no longer
fits instead of dropping it, keeping the owner's first message word for
word. At the start of each turn it is also told where its connections and
drafts stand, from planque and KIFF themselves, so it never depends on
remembering them.

**A name that was taken stopped the start.** Setup named the taf after one
the account already had, the start was refused, and *Try again* tried the
same name. A taf from Setup now takes the first free name. A second
problem came out of it: the failed start never reached Setup, which told
the owner it had renamed a taf that didn't exist yet, and then drafted a
second Card. The taf works under the tighter of the two, but it shouldn't
hold two. That one is open.

**Our database ran short of memory**, for about three and a half minutes,
in the middle of the test. Requests timed out, and the app said a
connection was lost on an approval that had in fact gone through. A queue
of Card checks had been keeping a week of finished work; we are cutting
that down.

**Five approvals** for one setup is too many. It is four now: the two
read-only setup tools come in one request. The tools and the Card stay
separate, because KIFF issues a Card only for tools that are connected.

## Next: what a chat remembers

Next, a chat will keep what its taf *read* for its next messages, so it
doesn't look the same payment up on every message: used for at most four
hours, encrypted with your account's own key. Only reads, never a
write's result, and only in chats. A taf is told each kept read's age,
and to read again before acting on it.

## What this is, and isn't

This was test mode: no real money moved. It was one job, refunds, on one
vendor, Stripe, with one person. tafs is in a free beta on iPhone and
iPad, and its code, like planque's and the agent runtime's, is source
available under the Functional Source License (Apache 2.0 after two
years). KIFF's own framework is MIT.

What we will claim is what this run showed: a person described a job in a
sentence, approved what it needed, and the limits they said were the
limits that held, with a receipt anyone can check.
