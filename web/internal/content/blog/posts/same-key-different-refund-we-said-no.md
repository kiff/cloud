---
title: "A retry gets one decision. A different refund gets refused."
kind: "essay"
date: "2026-09-14"
summary: "A reused idempotency key could have made KIFF answer one refund with the decision it made for a different one. The fix isn't a smarter cache. It's a flat refusal."
author: "Gabriel Sarmiento"
tags: ["agent-governance", "audit", "go"]
draft: true
---

> **AI;DR:** An idempotency key is supposed to make a retry safe: submit the same refund twice, get the same answer, one audit row. The obvious way to build that is "if the key matches, return the cached decision." We don't build it that way, because a key match alone can't tell you the second call is actually a retry of the first one — it could be a different refund that happens to reuse the key. Answering that with someone else's decision would be wrong in a way that looks exactly like working correctly.

Submit the same refund proposal twice with the same idempotency key, and
KIFF answers once: the second call gets the first call's decision back,
verbatim, with no new audit row. That's the whole point of an idempotency
key, and it's not the interesting part.

The interesting part is what happens when the key matches but the refund
doesn't. Same key, different order, different amount, different action
entirely. The naive version of dedup, "if the key matches, return the
cached answer," would hand back a decision that was never actually made
about this request. It would look identical to a correct retry from
outside, and it would be wrong.

## What a key match doesn't prove

A key collision has an obvious innocent cause, a client reusing a
counter, a hardcoded string, a UUID generator seeded wrong, and a less
innocent one that matters more: nothing about the key itself proves the
two calls are proposing the same thing. Trusting the key alone means
trusting the caller's bookkeeping to be correct, for a system whose job
is deciding whether to trust the caller at all.

So the dedup check doesn't stop at the key. It also fingerprints the
request: the entity, the action, the parameters. Both have to match. Key
matches and payload matches, you get the cached decision, free, no new
evaluation. Key matches and payload doesn't, you get a flat refusal:
invalid, key already used for a different request. Not a best guess. Not
the old decision with a warning attached. A refusal.

That's a harder answer to ship than it sounds. The easy version returns
something for every input. This version returns an error for a case
that, from the caller's side, might look like its own legitimate retry
with slightly different data, and tells them to go check their code
instead of quietly doing what they probably meant. We chose the version
that fails loudly over the version that's occasionally wrong in a way
nobody would notice, because a decision here is a claim, this action, on
this entity, was evaluated and allowed, and a claim that's sometimes
about the wrong request isn't a claim you can stand behind.

## Where the guarantee still depends on something optional

The part of this that isn't fully closed: the dedup lookup and the
decision it protects are supposed to run inside a lock, one tenant, one
key, so two retries arriving at the same instant serialize instead of
racing. That lock is optional. Without it, dedup degrades to a
read-then-write check with nothing holding the two operations together,
which collapses retries that arrive one after another but not ones that
arrive at the same moment, both reading "no decision yet" before either
has written one.

The code says so, plainly, in a comment written for whoever reads it
next rather than for whoever's evaluating whether to ship it. What it
doesn't say is how often that degraded path is the one actually running
in production, on deployments where the lock exists but a specific
request happens to land in the gap anyway. That's the number we don't
have yet, and it's the honest next thing to go measure, not a caveat to
bury in a footnote.
