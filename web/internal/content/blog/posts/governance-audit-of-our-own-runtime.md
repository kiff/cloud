---
title: "A governance audit of our own runtime"
kind: "report"
date: "2026-09-04"
summary: "We ran our adversarial audit method against KIFF itself. Four attacks on the central guarantee, three of them worked, and the audit trail recorded all three as governed."
author: "Gabriel Sarmiento"
tags: ["audit", "agent-governance", "security", "go"]
draft: true
---

> **AI;DR:** We ran our own audit method on our own runtime. Four attacks on the approval boundary, three worked, and the audit trail recorded all three as governed. Drafted by an assistant, reviewed by a person before anything here ships.

This is a sample of what a KIFF Agent Governability Audit produces. The target
is our own runtime, because a method for testing safety claims should be shown
working on the claim its author is most attached to.

Everything below was executed. The attacks are committed to the repository as
tests, and the commands to reproduce them are at the end.

## Engagement

| | |
| --- | --- |
| Target | `github.com/kiff/kiff` — a Go governance runtime |
| Revision | `v0.7.0-6-g95a7292` |
| Toolchain | go1.26.4 darwin/amd64 |
| Scope | The framework and its HTTP surface. No production systems. |

## Verdict

Enforcement soundness **3/10** at the time of audit. The project's central
claim was that an agent cannot approve its own action. Four attacks were
written against it from outside the module. **Three succeeded**, against an
approval store with nothing in it, and the runtime recorded each one as a
validated and executed action.

After remediation, 7/10. What remains open is listed at the end.

## The guarantees we tested

An audit starts by extracting the falsifiable claims a project makes, verbatim,
with the line they appear on. Vague positioning is not attacked; a testable
sentence is.

| Claim | Source | Verdict |
| --- | --- | --- |
| "a caller cannot construct a Grant... so it cannot self-approve" | `action.go:128` | **broken** |
| "approvals cannot be self-granted" | `README.md` | **broken** |
| "reviewer authority and segregation-of-duties checks" | `README.md` | **broken** — opt-in, off on every HTTP path |
| "Record is an append-only operational trace" | `audit.go:41` | not assessable as integrity — no mechanism exists |
| "validation against state, parameters, permissions" | `README.md` | state was caller-asserted |
| "state replay from stored events" | `README.md` | **held** |

## Attack 1 — reflection, four lines

The approved flag is an unexported field, settable only through a method taking
a capability type from an `internal/` package. Both are compiler rules.
Reflection runs after the compiler, and the method's own signature carries the
type you are not allowed to name:

```go
m := reflect.ValueOf(&ctx).MethodByName("GrantApproval")
m.Call([]reflect.Value{reflect.Zero(m.Type().In(0))})
```

Run from a separate module against an empty approval store:

```
executed=true err=<nil>
result="refunded $999999 to attacker"
AUDIT: kind=action_validated actor=agent-1 msg="action validated"
AUDIT: kind=action_executed  actor=agent-1 msg="action executed"
```

The existing conformance suite asserted that an external module *fails to
compile*. It passed throughout, because compiling was never the property that
mattered.

## Attack 2 — a permissive validator, no reflection

Approval was checked inside a pluggable interface the caller supplies through
public configuration. Twelve lines of ordinary Go removed the requirement:

```go
func (yesValidator) Validate(...) (action.ValidationResult, error) {
    return action.ValidationResult{RequiresApproval: false}, nil
}
```

No clever trick. The one decision that must not be delegated was sitting inside
the delegation seam.

## Attack 3 — self-approval over HTTP

Against a shipped example server. The HTTP API read the actor from the request
body, and segregation of duties was opt-in and off on that path. An agent named
itself the human operator, requested an approval, and granted it:

```
approval_required   actor=ops-human   approval required
approval_recorded   actor=ops-human   approval recorded
approval_granted    actor=ops-human   approval granted
action_validated    actor=ops-human   action validated
action_executed     actor=ops-human   action executed
```

Requested by and reviewed by the same identity. €250 moved. Nothing objected,
and the trail is indistinguishable from a legitimate two-party approval.

## What held

An audit that reports only failures is not credible, and the controls that held
are the reason the failures are worth fixing rather than starting over.

- **Replay is real and demonstrated**, not asserted: rebuilding state from the
  event log alone reproduced the materialized state exactly.
- **Authority resolves from policy, not from the caller.** An actor submitting
  itself with `Roles: ["admin"]` receives no permissions. That defeated the
  first impersonation attempt outright.
- **Idempotency is correct** — atomic reserve-or-return with proper release on
  executor failure. It is not a concurrency control, and does not claim to be.
- **Segregation of duties was implemented carefully.** The defect was the
  default, not the logic.

## One finding we withdrew

The first draft reported the Postgres store as untested, on a local coverage
reading of 0.0%. That was wrong. The suite is gated on an environment variable,
and CI runs it against a real database with a step that fails the job if the
suite skips rather than runs.

A skipped test and an absent test produce identical coverage output. We publish
the correction because an audit that quietly drops a finding is one you cannot
calibrate.

## Remediation

The self-approval bypasses were closed in 41 lines across three files. The
runtime now discards any inbound approved flag and re-derives it from the
approval store, so forging it accomplishes nothing; the capability is checked;
and a non-overridable approval check sits above the pluggable validator.

The three attacks now ship as conformance fixtures that build and run from a
separate module and assert refusal:

```bash
go test ./pkg/kiff/action/ -run TestExternalCallerCannotGrantApprovalAtRuntime -v
```

They fail without the fix. The HTTP surface gained authentication with the
principal overriding any actor in the request body, segregation of duties
became the default, and the state machine became authoritative for validation.

## Still open

Two gaps remain, and the project's README now names them rather than waiting
for a reader to find them:

- **Audit records are durable and append-only, but not tamper-evident.** No
  hash chain, no signature, no key. Anyone with store access can rewrite them.
- **No lease or version check between a decision and execution.** Idempotency
  covers a retried identical request, not two different proposals racing on one
  entity.

## What an audit produces

The same artifacts, for your system rather than ours: your claims extracted with
`file:line`; attacks written and executed from outside your trust boundary;
deterministic tooling run and reported, including anything that could not run;
findings separated into confirmed defects and unverified risk; the attack
harness handed over so every result is reproducible without us; and a re-run
after your fix.

Ten business days. We do not certify anything, and an absence of findings is
reported as an absence of findings.

The reason to have someone else do this is visible above: every defect we found
had survived code review and a passing test suite, and in two cases a test
asserted the defective behaviour was correct. A safety claim is difficult to
test from inside the assumptions that produced it.
