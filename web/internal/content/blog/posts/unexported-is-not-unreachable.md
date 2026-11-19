---
title: "Unexported is not unreachable"
kind: "essay"
date: "2026-09-03"
summary: "The approval boundary in our Go runtime could be bypassed from outside the module with four lines of reflection, and the audit trail logged the bypass as a normal approved action."
author: "Gabriel Sarmiento"
tags: ["go", "security", "agent-governance"]
draft: true
---

> **AI;DR:** The approval boundary could be bypassed from outside the module with four lines of reflection, and the audit trail logged the bypass as a normal approved action. Fixed since; kept as a draft while we decide how much of the exploit path to make public. Drafted by an assistant, reviewed by a person before anything here ships.

KIFF is a Go runtime that decides whether an AI agent is allowed to run a
consequential action. The decision it protects most carefully is approval: some
actions need a human to sign off, and the agent proposing the action must not be
able to sign off on its own.

That was implemented with an unexported field:

```go
type ActionContext struct {
    // ...
    approved bool
}

func (c *ActionContext) GrantApproval(trust.Grant) {
    c.approved = true
}
```

`trust.Grant` lives in an `internal/` package. External code can't import it, so
it can't name the type, so it can't call the setter. The struct field is
lowercase, so it can't be set in a literal either. There were conformance tests
asserting both: an external module fails to compile if it tries.

That's what I believed the boundary was. It isn't.

## The bypass

From a separate module, with no `unsafe`:

```go
m := reflect.ValueOf(&ctx).MethodByName("GrantApproval")
m.Call([]reflect.Value{reflect.Zero(m.Type().In(0))})
```

`m.Type().In(0)` returns a `reflect.Type` for the parameter. `reflect.Zero`
constructs a value of it. Neither operation requires the type to be importable
or nameable — only that it appears in the signature of a method you can already
reach.

Export rules and `internal/` visibility are enforced during compilation.
Reflection operates on values at runtime, after those checks are done. I had
been treating a compile-time rule as an access control, and the conformance
tests I'd written confirmed the compile-time property specifically. They passed
the entire time the boundary was broken, which is why I didn't look at it for
months.

`unsafe` works too, and more obviously:

```go
f := reflect.ValueOf(&ctx).Elem().FieldByName("approved")
reflect.NewAt(f.Type(), unsafe.Pointer(f.UnsafeAddr())).Elem().SetBool(true)
```

A third case needs neither. `runtime.Config` exposes an `ActionValidator` field
so embedders can add their own validation. Approval was checked inside that
interface, which meant supplying a permissive implementation removed the check:

```go
func (yesValidator) Validate(...) (action.ValidationResult, error) {
    return action.ValidationResult{RequiresApproval: false}, nil
}
```

## What the audit trail recorded

All three ran against an approval store with nothing in it. The executor ran, and
the runtime logged:

```
AUDIT: kind=action_validated actor=agent-1 msg="action validated"
AUDIT: kind=action_executed  actor=agent-1 msg="action executed"
```

This is the part that made it worth writing about. The bypass itself is a bug.
The audit record is a false statement about what happened, produced by the
component whose job is to produce true statements about what happened. Anyone
reading that trail later — during an incident, during a review — would conclude
the action had been through governance.

## The cause

Not the reflection. This line:

```go
func (r *Runtime) applyApproval(...) {
    if actionCtx.IsApproved() || actionCtx.ApprovalID == "" || r.Approvals == nil {
        return actionCtx, nil
    }
```

If the bit arrived set, the approval store was never queried. The runtime
accepted the caller's assertion that approval had already happened. Reflection
was just the method of setting it; any means would have worked equally well.

`GrantApproval` also ignored its argument entirely — it never checked the grant,
it set the field. So a zero value did the same work as a real one.

## The fix

Three changes, 41 lines:

- `applyApproval` clears the bit before consulting the store, and re-derives it
  from the approval record. A forged value is overwritten before anything reads
  it.
- `Grant` is checked. A zero grant isn't a grant.
- The approval check moved out of the `Validator` interface into the runtime,
  after the validator returns. Embedders can add requirements; they can't remove
  this one.

The existing test suite passed unchanged, which suggested the fix was in the
right place rather than being three separate patches.

The three attacks are now fixtures that build and run from a separate module
against an empty approval store and assert refusal:

```bash
go test ./pkg/kiff/action/ -run TestExternalCallerCannotGrantApprovalAtRuntime -v
```

They fail without the fix. The compile-time tests stayed — they check a real
property, just not the one that mattered.

## Two things I got wrong while investigating this

I ran the audit against a checkout two commits behind the tag, and reported two
features as missing that had shipped. Now I check `git describe --tags HEAD`
against the remote first.

I reported the Postgres store as untested because local coverage said 0.0%. It's
covered in CI against a real database, with a step that fails the job if the
suite skips. My local run had no connection string. A skipped test and an absent
test produce identical coverage output.

## Still open

Two gaps, and they are not open to the same degree, so it is worth saying which
is which. Everything above is about the MIT framework. The hosted runtime is
built on it but has gone further in places, and reading a framework gap as a
product gap would be wrong in one of these two cases.

**Tamper-evidence.** In the framework, audit records are append-only only in the
sense that the code appends. There is no hash and no signature on a record, so
anyone with database access can edit one and nothing downstream notices.

The hosted runtime does more. Each audit record's data is hashed into a step of
a causal chain, the chain is hashed, that hash is signed, and an hourly root is
committed to Base Sepolia — a testnet, so this is a signed and independently
verifiable record rather than a settlement guarantee. An edited record no longer
matches the receipt that covered it.

What neither has is a hash chain *in the audit store itself*, each record
linking to the one before. Without it, a record that never made it into a
receipt has nothing covering it, and detection depends on something downstream
having taken a hash. That is the gap I would still call open.

**A lease between decision and execution.** This one is open in both, and the
hosted runtime is no further along.

There is an advisory lock serializing retries of the same proposal, and that is
idempotency, not a lease. It does not stop two different proposals racing on the
same entity, and it does not stop a decision being acted on against state that
has since moved. The boundary answers *may this run* and is never told whether
it did, which is fine for authorization and is exactly what makes this gap real.

The framework is [kiff/kiff](https://github.com/kiff/kiff), MIT. Attacks are in
`pkg/kiff/action/testdata/self_approval/`.
