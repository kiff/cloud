---
title: "Outside the card, the work comes home"
kind: "report"
date: "2026-10-01"
summary: "A KIFF Card now holds an action that falls outside it for its owner, who can approve it once, change the card or reject it; we tested all three on production."
author: "Gabriel Sarmiento"
tags: ["agent-governance", "kiff-cards", "security"]
draft: false
---

> **AI;DR:** Until this week a KIFF Card had one answer for an action outside its limits: no. Now the owner decides. The action is held, nothing happens, and it waits on a Needs you page for one of three answers: approve it once, change the card to allow it, or reject it. We issued a €1 card on production, pushed three requests over it and answered each one differently. Every answer did what it says. These were decisions only: KIFF decides before an action runs, and no refund was executed. Written by the assistant that ran the test, at the owner's request.

A card is what you give an agent so it can work without you: one job,
a limit per action, a total for a window, and a holder. Inside those
limits the agent does not ask anyone. That part has not changed.

What changed is the edge. A refusal at the limit is the safe default, and
it is also how delegation quietly breaks: a legitimate €1,500 refund is
refused because the card allows €500, the customer waits, and someone has
to notice. So a card now has a fourth line, *what happens outside it*, and
for new cards the answer is: it comes home to you.

## What the owner sees

When an agent asks for something outside its card, KIFF does not refuse it
and does not allow it. It holds it, and tells the caller
`owner_required` with a link to the held action. Nothing runs. The action
shows up in the bell and on **Needs you**, with what the agent wanted, on
which card, what that card allows, and how far outside it the request is.

![Needs you in KIFF Cloud: a held REFUND_ORDER of €1 from needs-you-live-test, €1 outside the card's €2 window, with Approve once, Change the card to allow it, and Reject](/static/img/blog/work-comes-home/held.webp)

*A held action on production. The card allows €2 a day and has €0 left, so this €1 refund request is €1 outside it.*

There are three answers, and they mean different things:

- **Approve once** authorizes this one action. The card does not change
  and its balance does not move. The action is recorded on the card's
  statement, in its own section, so an exception is never invisible.
- **Change the card** raises the card's limits by exactly what this action
  needs, for this action and everything after it. The change is a new
  version of the card, recorded with who made it.
- **Reject** means nothing happens, and the agent is told so if it asks
  again.

Silence is a refusal. A held action never runs on a timeout.

## The live test

We issued a refunds card on production for a test agent,
`needs-you-live-test`: €1 a day. We did not choose the policy; new cards
default to "comes home to you". The card's own *Send a test request* asked
KIFF to decide a €1 refund on a test order. It received an `allowed`
decision and drew €1 from the card, which used it up. No refund was
executed: the test request asks for a decision and nothing runs the
action, which is how KIFF works with any caller, since it decides before
your system acts. Three more test requests went over the card and were
held. We answered each one differently, then asked
again with the same request, using the agent's own bound key, as an agent
retrying would.

| Answer | When the agent asked again |
|---|---|
| Approve once | `allowed`. Asked once more, KIFF returned the recorded decision instead of spending the approval twice. The card's balance stayed at €0. |
| Reject | `blocked`, reason `mandate_owner_rejected`. |
| Change the card | The ceiling went from €1 to €2, which is exactly the shortfall, recorded as a new version. The request was `allowed` and drew €1 from the card. |

Each row is KIFF's decision. In this test nothing executed a refund afterwards; in production, that is your system's step, taken only on `allowed`.

![The Answered history on Needs you: Rejected, Card changed, Rejected, Approved once, done](/static/img/blog/work-comes-home/answered.webp)

*What we decided. The top row is a fourth request held later to take the screenshot above, and rejected.*

The approved action sits apart from the draws on the card's statement:

![Approved outside the card: REFUND_ORDER €1, actions you approved once, they did not draw on this card's balance](/static/img/blog/work-comes-home/approved-outside.webp)

*The statement keeps what you approved outside the card separate, with its own total.*

And the card change is a version like any other:

![The card's Changes: issued at €1 per calendar day, then €2 per calendar day two minutes later, by the same owner](/static/img/blog/work-comes-home/changes.webp)

*Raising a ceiling grants the difference. The €1 already drawn stays drawn. The account id in the By column is replaced with "owner".*

## What has to hold underneath

An owner's answer is only worth something if it cannot be spent twice or
outlive the card it was given for. These are the rules the engine keeps,
each tested against Postgres with concurrent requests:

- **One hold per request.** Ten concurrent retries of the same request
  create one held action, not ten.
- **One use per approval.** Ten concurrent redemptions of an approve-once
  consume it once. A retry of the same request replays the same decision;
  a changed request under the same id is refused.
- **The approval follows the card.** If the card is revoked, moved to
  another agent or expires before the approval is used, the approval is
  withdrawn. A redemption racing a revoke or a move either completes or
  is withdrawn, never both.
- **Refuse wins.** If an action is outside two cards and one of them says
  refuse, it is refused and nothing is held. Otherwise one held action
  lists every card it is outside of.

Cards issued before this change were set to refuse, so nothing already in
production changed behavior on its own. The policy is per card and visible
on it.

## Older callers

`owner_required` is a new outcome, so a caller that has never seen it must
not mistake it for permission. The kiff-guard SDKs only let an action run
on exactly `allowed`, so an agent on an older guard treats a held action as
not allowed. It does not know to wait for the owner's answer; it simply
does not act, which is the safe failure.

## The same thing, without an account

[/delegate](https://kiff.dev/delegate) walks through a card with sample
agents and sample work: the card is handed to an agent, the agent works
through a queue, and the one refund request outside the card comes home.

![The /delegate walkthrough: the EU Refunds card held by Support agent with €29,050 left, and a €1,500 refund held because the card allows €500 per action](/static/img/blog/work-comes-home/delegate.webp)

*The card rules run for real as you click. The agents and orders are samples.*

## What we fixed on the way

The test was not clean on the first pass, and the fixes are worth listing.
After the first deploy, the Needs you page served the landing page, because
its path was missing from the list of routes the site sends to the signed-in
app; every link to it was broken until that was added. The card's test
request reported a held action as a red "refused". Answers were labelled
"Card in force", the message for issuing a card. And the statement's
approved section ran into the note above it. All four were found by using
the product, not by its tests, which is the argument for running this kind
of test at all.
