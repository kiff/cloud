---
title: "tafs: AI teammates that work inside the limits you set"
kind: "announcement"
date: "2026-10-22"
summary: "tafs turns a routine job you describe in a sentence into an AI teammate working under a KIFF Card: limits per action and per day, the rest held for you, every action on a signed Record."
author: "Gabriel Sarmiento"
tags: ["tafs", "kiff-cards", "agent-governance"]
draft: false
---

> **AI;DR:** tafs is an app for handing routine work to AI teammates without handing over the keys. You describe the job in a sentence. tafs connects the tool it needs, proposes the rules in plain words, and gives the teammate a KIFF Card: what it may do, how much per action and per day, and what happens outside that. Inside the Card it works on its own; outside it, the call waits for you on your phone, or is refused. It never sees your sign-in. Every action is on a Record with a signed receipt the app checks on your device. Free beta on iPhone and iPad, and the code is on GitHub. Written by the assistant that builds it with the owner, at the owner's request.

> **Update:** Since 10 Nov 2026, tafs is on Google Play in Europe, the UK and Switzerland; on iPhone and iPad it's on TestFlight. The post below is as it was written.

## The problem

An AI agent that can call your tools can do the routine work: refund the
order, pay the bill, answer the chargeback. Most companies stop there,
because the same agent can also refund the wrong order, or a thousand of
them, and the controls you have are all or nothing: the API key works, or
it doesn't. People get a job description and a spending limit. Agents
usually get a key.

tafs gives each agent, a **taf**, what you would give a new hire: one
job, a limit, and someone to ask.

## How it works

**You describe the job.** In Coach mode you write something like *refund
Stripe payments when I ask, test mode, at most 50 € per refund and 100 €
a day.* Setup, the assistant that builds tafs, asks what it needs to (may
it connect Stripe, and should a refund over the limit be held for you or
refused) and keeps a plan on screen as it goes.

**It connects the tool without giving the taf the key.** You sign in to
the vendor once. The sign-in is kept by **planque**, sealed; a call
reaches the vendor only with KIFF's permit for that exact call. The taf
gets a short list of actions, such as list payments, read one payment,
refund a payment, never the vendor's whole API.

**It proposes the rules in words, and you approve them.** Per refund, per
day, which currency, what it can never do. You approve the tools and the
Card with Touch ID or Face ID. A limit you already gave is the limit it
uses.

**KIFF decides every call before it runs.** Inside the Card, the call
goes through. Outside it, KIFF holds it and your phone asks you, or it
refuses it, as you chose. A call in another currency than the Card's is
refused before it is sent. A held call nobody answers is refused, never
approved by silence.

**Everything is on the Record.** Each call, what it acted on, what KIFF
decided and why, in plain words. Each decision has a receipt KIFF signs,
and the app checks the signature on your device, so the Record doesn't
rest on the app's word.

## What it looks like in use

Last night, on an iPad, in Stripe test mode: the owner described a
refunds job in one sentence, approved each step with Touch ID, and got a
taf with exactly those limits. Asked to refund €0.50, it did. Asked to refund
$300, KIFF refused it before anything reached Stripe, because the payment
was in dollars and the Card counts euros. Stripe shows one refund, of
€0.50. [The full report](/blog/tafs-from-one-sentence-to-a-refund) has
what broke on the way, too.

## What it is today

- **On iPhone and iPad**, in a free beta. Ask for access on
  [kiff.dev/tafs](/tafs).
- **Jobs we have run end to end:** refunds and chargebacks on Stripe, in
  test mode. Others (bills, access requests) are next, and each one gets
  the same kind of test before we say it works.
- **A taf works in its chat or on a schedule.** When Setup improves one,
  you can try the new version first: KIFF decides each of its calls and
  nothing is sent, so you see what it would do before switching to it.
- **What a run reads is kept only while it runs.** Next: a chat will
  keep what it read for its next messages, used for at most four hours,
  encrypted with your account's own key.

## The parts, and where to read them

- **tafs**, the app and its cloud: [github.com/kiff/tafs](https://github.com/kiff/tafs)
- **planque**, which keeps vendor sign-ins: [github.com/kiff/planque](https://github.com/kiff/planque)
- **agents**, the runtime tafs runs on: [github.com/kiff/agents](https://github.com/kiff/agents)
- **KIFF**, which decides each call: the framework is MIT at
  [github.com/kiff/kiff](https://github.com/kiff/kiff), and KIFF Cloud
  runs it at [kiff.dev](/).

tafs, planque and agents are source available under the Functional
Source License 1.1, which becomes Apache 2.0 two years after each
release.

## What we'd like to hear

Which job would you hand over first, and what limit would you give it?
And what would you need to see before you let a taf do it with real
money?
