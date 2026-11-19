---
title: "A refund desk for Shopify shops, within limits you set"
kind: "announcement"
date: "2026-10-13"
summary: "How a refund desk would work for one Shopify shop, followed through a whole day: the agent handles routine refunds within the owner's limits, and asks the owner about anything bigger."
author: "Gabriel Sarmiento"
tags: ["kiff-cards", "agent-governance", "shopify"]
draft: false
---

> **AI;DR:** A refund desk for Shopify shops: an agent answers refund emails
> and refunds within the owner's limits, and anything bigger waits for the
> owner on their phone. It is not built yet; this is how we want it to work.

## It should live where people already are

People love it when nothing about their day changes except the work
disappearing. So the desk has two places, one for the customer and one for
the owner, and neither is new.

**Where customers write: the shop's support inbox.** Refund requests
already arrive by email, or through the contact form, which also becomes
email. The agent reads that inbox and replies from the shop's own address.
The customer never notices anything new; they just get a fast, correct
answer.

**Where the owner answers: their phone.**

- **Once a day,** a short message: "12 requests: 5 refunded (€146), 4 asked
  to return the item, 2 asked for a photo, 1 waiting for you."
- **For a held refund,** one tap to approve, return first or refuse.

KIFF already emails the approver when a call is held, and approvals already
happen at [app.kiff.dev](https://app.kiff.dev). Day to day, the owner
doesn't open anything else.

## One shop, followed through a whole day

The shop and the owner are made up.

**The shop:** Luna Ceramics sells mugs and plates on Shopify. The owner,
Marta, gets about 25 refund requests a week at hola@lunaceramics.com. She
handles them all herself, about 4 hours a week.

### Setup (once, about 30 minutes, we do it with Marta)

1. Marta sets her email to forward a copy of support emails to us.
2. Marta authorizes the Shopify connection we provide, so the desk can look
   up orders and issue refunds. Shopify's key stays behind KIFF's gateway;
   the agent never holds it, so every refund has to go through KIFF.
3. In KIFF, Marta's agent gets its Cards:
   - **Refunds:** up to €50 each and €300 a day. Anything bigger waits for
     Marta.
   - **Store credit:** up to €30 each.
   - **Cancel an order:** only if it hasn't shipped yet.

### 10:02. A customer writes

```text
To: hola@lunaceramics.com
My mug from order #1043 arrived broken. Photo attached.
```

Within about a minute, without anyone touching it:

- The agent finds order #1043 in Shopify: €38, delivered yesterday.
- Under Marta's rules, "arrived broken, small amount" means refund now.
- It asks to refund €38. KIFF checks the refund Card: under €50 and within
  today's €300, so it's allowed.
- Shopify refunds the €38, and the customer gets a reply from
  hola@lunaceramics.com:

```text
Sorry about your mug! We've refunded €38 to your card.
```

### 11:30. A bigger request

A customer asks for €240 back on a dinner set. The agent asks to refund it.
KIFF sees it's over €50, holds it and messages Marta. Nothing is sent yet.
On her phone she sees:

```text
KIFF: Luna support desk wants to refund €240
Order #1051 · dinner set · "2 plates cracked"
[ Approve ]  [ Return first ]  [ Refuse ]
```

Marta taps **Return first**. The agent emails the customer return
instructions, and no money moves.

### 18:00. The summary in Marta's inbox

```text
Today: 6 requests
· 3 refunded (€97 in total)
· 2 asked to return the item first
· 1 waited for you (you chose: return first)
```

**If something ever looks wrong:** Marta opens KIFF on her phone and
presses **Stop**. From then on KIFF allows no new refunds. A refund it had
already allowed a moment before may still complete.

### What Marta never does

- open Claude;
- learn a new app;
- log into a dashboard every day.

Her customers see nothing new either. They just get faster replies.

## Where each part lives

| Part | Where |
|---|---|
| Customers writing in | Marta's email, as today |
| The agent | On our side. For the first shops, we run it with Claude Code, paid with our Claude credits |
| Limits, approvals, Stop, records | KIFF ([app.kiff.dev](https://app.kiff.dev)) |
| Marta's approvals | Her phone, by email for now (WhatsApp later, if owners ask) |
| What Marta pays | A monthly fee to us |

## Why not somewhere else

| Option | Why not first |
|---|---|
| **The owner's Claude account** | Most shop owners don't use Claude every day, and the desk shouldn't depend on them remembering to open it. |
| **A chat agent on the shop's website** | It talks to customers live, which is the step that needs the most trust. Helpdesk tools already offer it. |
| **WhatsApp** | A good place for the owner's approvals later, if owners ask for it. |
| **Its own page** | That is KIFF itself: where the owner sets the limits, stops the agent and checks the records. Not somewhere to visit every day. |

## What already exists

- the Cards and their limits;
- holding a call and emailing the approver;
- the approval page;
- Stop;
- the records.

## What we still need to build

1. The Shopify connection: refunds, store credit and cancellations behind
   our gateway.
2. Reading the forwarded emails and replying from the shop's address.
3. "Return first" as an answer, next to approve and refuse.
4. The daily summary.
5. Marta's rules written as the agent's instructions.

## Part 2: it ran

We built the desk and ran it on a Shopify store, six refund emails and the
owner's answer included: [part 2](/blog/a-refund-desk-for-shopify-shops-part-2).
If you sell on Shopify and would like to try it, email
[gabriel@kiff.dev](mailto:gabriel@kiff.dev).
