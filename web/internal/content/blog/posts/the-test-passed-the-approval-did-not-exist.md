---
title: "The test passed. The approval did not exist."
kind: "essay"
date: "2026-09-15"
summary: "Building a control that holds an agent's action for a human, we wrote the same bug four times: something that read as present and was not. Here is each one."
author: "Gabriel Sarmiento"
tags: ["agent-governance", "security", "go"]
draft: false
---

> **AI;DR:** We added a control that holds an agent's action for a human when the run has consumed untrusted content. Building it, we wrote the same defect four separate times, in four different places, and the shape was identical every time: a thing that reads as present and is not. One of them had a passing test sitting on top of it.

An agent proposes an action. The only two fields on that proposal
describing *why* it is being proposed are `ReasoningSummary` and
`Confidence`, and both are written by the model. The runtime has a
trustworthy answer to who is asking and what for, and no answer at all to
what happened earlier in the run that should change the answer.

That gap is what makes indirect prompt injection hard to see from inside a
decision boundary. Untrusted content enters the model's context, the plan
changes, and the next proposal is byte-for-byte what a legitimate one
looks like: permitted actor, permitted action, valid entity. The only
thing that differs is causation, which is precisely the thing nothing
recorded.

So we added a `run_context` the harness asserts and the model cannot, and
let an action declare that its answer depends on it. That part took an
afternoon. The rest of this post is the four times we got it wrong.

## The same defect, four times

**One: a revoked limit made the agent less bounded.** This one is older
than the current work and it is the reason we recognised the rest. Our
cumulative-authority check pre-filtered a subject's mandates to the active
ones, so a *revoked* mandate produced an empty set, an empty set read as
"no mandate covers this action", and by the constrains-not-confers rule
that meant allowed. Revoking a limit widened authority. The guard read as
safety and produced its opposite.

**Two: absence rendered as a clean run.** The first version of the new
recording path stamped `untrusted_input: false` whenever nothing had been
asserted. That is a claim about the run, manufactured out of silence. A
decision record has to distinguish three states, not two: nothing was
asserted, a clean run was asserted, a tainted run was asserted. Collapsing
the first two produces evidence that says a run was clean when what
actually happened is that nobody looked.

**Three: the rules were unreadable and that counted as no rules.** The
gate looked up an action's declared dependency lazily, and treated a
lookup failure as "this action declares nothing". The comment we wrote at
the time argued the failure was unreachable in practice and picked the
lesser of two bad options. It was still a fail-open at the centre of a
control whose entire purpose is failing closed. The fix was to stop
choosing: the rules are now resolved beside the runtime that answers the
proposal, one parse of one document, so there is a single place it can
fail and that place already refuses.

**Four: the hold nobody could grant.** This is the one with a passing test
on it. When the gate holds an action for a human it returns
`approval_required` and a handle with an approval id. The framework,
correctly, refuses to create an approval record for an action whose
contract says approval is never required — which is exactly the contract
these actions carry, because the hold comes from the run-context rule
rather than the action's own risk tier. So the record was never created,
the error was swallowed as a warning, and the caller received a handle
pointing at nothing. A human could not grant it because there was nothing
to grant.

Our test for this asserted that the response carried an approval handle.
It did. The test passed the entire time.

## What the fourth one cost us to find

It was found by regressions we only wrote because a review asked for them
about a *different* bug. The review pointed out that run context was
recorded but not bound into the request fingerprint, the value that
decides whether a retry is the same request and that approval ids are
built from. That meant a decision made under an
asserted-clean run could be replayed under a tainted one and the earlier
answer returned before the gate ever ran, and it meant an approval granted
in one run could satisfy the same action in another.

Writing the regressions for that is what surfaced the empty approval
record. So one review comment, about a defect that is not even among the
four above, is what turned up the fourth.

The fix for the fourth is small and says what the gate has already
determined: the contract handed to the approval path is marked as
requiring approval for this proposal. The static "never" describes the
ordinary case. The run-context rule is what makes this instance different,
and the contract has to say so or the framework is right to reject it.

## What this does not do

Run context is asserted by the harness, not proven. A compromised
integrator can assert a clean run for a run that consumed untrusted
content, exactly as one can decline to call the gate at all. This defends
against the model being manipulated. It does not defend against the
integrator being compromised, and we would rather say that here than have
it discovered in front of someone evaluating us.

The taint is also coarse. It is tracked per run, not per value, so an
action genuinely unrelated to the untrusted content still trips the rule.
We do not know the false-positive rate. Nobody has run it against enough
real traffic to produce that number, and until someone has, "how often
does this hold something it should not" is an open question rather than a
tuned parameter.

One more, which is uncomfortable in a useful way. We verified the
recording path against production directly and it behaves. We could not
verify the *gate* the same way, because exercising it requires authoring a
domain, and a runtime API key is forbidden from modifying the domain it is
governed by. That refusal is correct and we would not remove it. It also
means the enforcing half of this feature is covered by tests and not yet
by a production check, which is a weaker claim than the one we would
prefer to be making.
