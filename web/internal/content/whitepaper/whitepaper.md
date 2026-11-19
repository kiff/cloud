# KIFF Cards

## Delegated work with enforceable authority {#delegated-work}

An agent can decide how to complete a job. The business giving it that job
still needs to decide which actions it may take, how much it may commit,
and when it must ask someone.

A KIFF Card records that delegation outside the agent. An owner issues it
for defined actions, with conditions, limits and an optional expiry. KIFF
checks proposed actions against the business's rules and the agent's
current Cards before execution. Requests outside the Card are held for the
owner or refused, according to its terms.

This paper explains the relationship between work, tasks, Cards and tools;
where authorization must be enforced; and what the resulting record proves.
It covers the existing Card and MCP gateway model, the newly implemented
execution permits, and the identity and delegation work that remains.

*Gabriel Sarmiento · Revision 1 · 11 October 2026 ·
[CC BY 4.0](https://creativecommons.org/licenses/by/4.0/).*

### Contents

- [Authority for work](#authority-for-work).
- [What a Card defines](#what-a-card-defines).
- [From a task to an action](#from-a-task-to-an-action).
- [A refund example](#a-refund-example).
- [Where enforcement belongs](#where-enforcement-belongs).
- [Execution permits](#execution-permits).
- [Approval, change and revocation](#approval-change-and-revocation).
- [The record and its limits](#the-record-and-its-limits).
- [Implementation and direction](#implementation-and-direction).
- [Adopting the pattern](#adopting-the-pattern).
- [Technical references](#technical-references).

## 1. Authority for work {#authority-for-work}

A support agent handling a refund needs more than a connection to a payment
tool. The business may allow refunds only on eligible orders, up to €50
each and €500 a day. It may want larger requests to wait for a person.
Those rules must continue to apply when the agent restarts, changes its
model, or runs in several workers.

Instructions in a prompt can guide the agent. They cannot establish an
independent authorization boundary if the agent can also reach the
protected system directly. Malicious content or a mistake can change what
the agent requests. The system performing the action must still require
the business's authorization.

The useful assumption is that an agent may request something it should not
do. The owner keeps the authority to decide whether that request may take
effect. A Card makes that authority explicit and gives KIFF a record to
check independently of the agent's reasoning.

This applies to refunds and credits, but also to plan changes, bookings,
access changes and production operations. The consequence may be measured
in money or in a count of actions.

## 2. What a Card defines {#what-a-card-defines}

A Card identifies its holder and the work it covers. Its terms select
actions from a business domain, narrow the states in which they may run,
and cap how much authority may be used over a window. Per-action limits
and the response to an exception complete the delegation.

| The owner decides | Refund example |
|---|---|
| Who holds the Card | The support agent |
| Which action it covers | Refund an order |
| When the action is eligible | The order is paid and eligible for refund |
| The limit for one request | €50 |
| The total over a window | €500 per day |
| What happens outside the limits | Hold for the owner |
| When the Card stops | Its expiry, or the owner's revocation |

The example assumes the integration supplies authoritative order state and
models refund eligibility. KIFF cannot establish that an order is paid
from the agent's claim alone. A gateway connection also does not
automatically synchronize the payment provider's order history.

The domain defines which actions exist and when they are legal.
Permissions establish whether the authenticated agent may request them.
A Card narrows that existing authority; it cannot make an invalid action
valid or grant permission the agent lacks.

Actions that require a Card must explicitly declare that requirement.
The MCP gateway's connected actions do. In a custom integration, an action
without that requirement can proceed under its ordinary permissions and
state rules when no Card covers it. Issuing a Card alone does not govern
every possible action in an application.

Balances live centrally. Several workers using the same Card draw on the
same total. A Card can also be shared by named agents; separate Cards keep
separate balances. If several Cards cover the same action, all applicable
constraints must hold. Adding a wider Card does not bypass a narrower one.

## 3. From a task to an action {#from-a-task-to-an-action}

A task describes the work: resolve a customer's refund request. An action
is a specific proposed consequence: refund €30 on order `8812`. A tool is
the interface that carries out that action in the customer's system.
The Card supplies the bounded authority needed for the governed action.

One task may need several actions. The agent might read the order, issue
a refund and send a confirmation. Each consequential action that requires
authority needs its own check. Permission to begin the task does not
authorize every later step, and the task's label does not replace the
rules of the actions it contains.

<figure class="card-flow">
  <div class="card-flow-owner">The owner issues, changes or revokes the Card.</div>
  <ol class="card-flow-steps">
    <li><strong>Agent requests an action.</strong><span>For example: refund €30 on order 8812.</span></li>
    <li><strong>KIFF checks the request.</strong><span>Permissions, business state and current Card authority.</span></li>
    <li><strong>The integration enforces the answer.</strong><span>Only an allowed request may reach execution.</span></li>
  </ol>
  <div class="card-flow-outcomes">
    <div><strong>Allow.</strong><span>The tool can execute.</span></div>
    <div><strong>Hold.</strong><span>The owner must answer.</span></div>
    <div><strong>Refuse.</strong><span>The action stops.</span></div>
  </div>
  <figcaption>The agent requests the action. Authority stays with the business.</figcaption>
</figure>

MCP also uses the word *task* for an asynchronous call lifecycle. KIFF's
gateway supports this: a held call can remain a working task while it
waits for the owner, then return the call's result. Only the credential
that created the task can read it. A task identifier does not confer new
authority, and polling follows the same authorization path as retrying
the held call.

These two meanings serve different purposes. Business tasks explain what
someone delegates. MCP tasks let a client wait for an outcome. Neither
implies a general workflow engine or an automatic budget for an entire
multi-step job.

## 4. A refund example {#a-refund-example}

Suppose the support agent holds the example Card above, and the refund
integration enforces it. These are illustrative requests, not reported
customer transactions.

A €30 refund on an eligible paid order fits both limits. KIFF authorizes
it and records a €30 draw. The integration then sends the request to the
refund tool. Assuming no earlier draws, €470 remains for that day.

A €200 refund exceeds the per-action limit. With the Card configured to
hold exceptions, KIFF returns a held decision. The gateway sends nothing
to the refund tool while it waits. The owner can approve that request
once, change the Card, or reject it.

An approved request still counts. Approving the €200 refund waives only
the €50 limit for that one request; its full amount is recorded against
the day's €500 total, leaving €270 after the earlier €30. An approval
never takes a day past its total. A request that would exceed the total
cannot be approved once: the owner has to give the Card more room. If
other refunds use up the room before the agent collects an approved
request, it waits, approved but not sent, until room returns.

A plan-change request has no matching authority on this refunds Card.
If that action requires issued authority and no other Card covers it,
KIFF refuses it. A larger refund allowance does not grant another kind
of work.

Now assume malicious instructions persuade the agent to request a €4,800
refund. The request still reaches the same checks. It waits for the owner
or is refused according to the Card; the agent's conviction is not an
approval. This property holds only when the agent has no alternate path
to perform that refund outside enforcement.

## 5. Where enforcement belongs {#where-enforcement-belongs}

An authorization decision protects an action only if the execution path
obeys it. KIFF supports integrations at three points:

- **In application code.** The customer's system asks KIFF before the
  action and executes only an allowed request.
- **At an agent's tool boundary.** KIFF Guard connects that check to a
  supported agent framework. Enforce mode stops disallowed calls. Observe
  mode provides visibility without establishing that boundary.
- **Through the MCP gateway.** The agent calls connected tools through
  KIFF, which checks the request before forwarding it.

The business tool continues to live in the customer's service, or in an
external service reached through an adapter. Registering it in KIFF records
the connection and its governed action; it does not move the business
implementation into KIFF. KIFF does not process the payment itself.

For enforcement to hold, the agent must lack a second route to the same
capability. A direct API key, an ungoverned tool or a browser session that
can perform the action bypasses the Card. An agent under attacker control
must also lack owner credentials that could change its authority.

For code integrations, the check must run in a component the potentially
compromised agent cannot alter. A hook inside a runtime whose code and
credentials the attacker controls cannot provide that separation by itself.

## 6. Execution permits {#execution-permits}

The gateway's default connection mode, `sealed`, stores the tool's
credential encrypted and forwards it with allowed calls. This keeps the
credential away from the agent, but KIFF still holds it.

RFC 046 adds two ways to move the downstream credential out of KIFF:

- In **verify mode**, a customer-owned tool verifies a signed execution
  permit before acting.
- In **relay mode**, a customer-run relay verifies the permit and uses
  the downstream credential from the customer's environment. This works
  with services that do not understand KIFF permits themselves.

A permit authorizes one exact operation. It binds the account, intended
recipient, tool, arguments and operation identifier to the Card decision.
The verifier checks that binding before execution and uses a durable
operation record to prevent another send for the same operation.

The Card holds the owner's ongoing delegation. The permit carries the
authorization for one particular call. A receipt records the decision
afterwards; presenting a receipt does not authorize execution.

A held call gets no permit. When the owner approves and the client resumes
the call, KIFF rechecks authority before issuing one. Approval cannot be
reused for different arguments. Version 1 issues at most one permit per
operation and does not automatically reissue an expired permit.

If the verifier loses the downstream result, recovery looks for the
outcome without sending the operation again. An unresolved result remains
unknown. The original authorization can still count against the Card
even if execution cannot be established.

These modes change credential custody per connection. A relay may still
require a transport credential that KIFF holds, but that credential alone
cannot execute an action without a permit. Connections remaining in
`sealed` mode still give KIFF their downstream credentials.

KIFF remains trusted to authorize calls. The gateway sees requests and
can request authorization within the Cards of agents whose credentials
it carries. The signing key also needs protection: someone who steals it
can forge permits without a Card decision. Signature verification does
not remove those trust assumptions.

## 7. Approval, change and revocation {#approval-change-and-revocation}

An owner decides the ordinary terms once, then answers the exceptions.
Approving one held request creates an exception for that request, without
raising the ordinary limit for subsequent work. A refusal remains a
refusal; only a held request follows the approval path. Unanswered holds
expire rather than executing on silence.

The owner can also raise or lower a Card's limits, change its scope or
revoke it. Recorded draws continue to count when a ceiling changes.
The agent's runtime credential cannot issue its own Card, widen it or
answer its own approval.

Future requests use the changed authority. Revocation stops new permits;
it does not undo an action already performed or cancel every request
already authorized. An approval given before revocation no longer
applies. An execution permit issued before revocation or expiry can
still start its operation while it remains valid. How long that is
depends on how the tool is connected and how its verifier is configured;
the [tool permits documentation](/docs/tool-permits#when-revoking-or-expiring-a-card-takes-effect)
gives the figures.

## 8. The record and its limits {#the-record-and-its-limits}

The Card's statement records draws against its balance. Its history records
who issued or changed its terms. Decision records explain what KIFF
allowed, held or refused, with signed, tamper-evident receipts as evidence
of those decisions.

A draw occurs at authorization. It is not proof that money moved or that
the customer's system completed the work. A receipt proving KIFF allowed
a refund must not be presented as proof that the payment provider made it.
Execution results need their own evidence from the integration or provider.

Concurrent workers draw on the same ledger. Idempotent retries of a
recognized operation do not create another draw or another gateway send.
An unreadable balance fails closed. Identifying operations consistently
remains an integration responsibility: sending the same business request
under a new identifier can make it a new operation.

Through the gateway, a lost response can leave an authorized call with an
unknown execution outcome. KIFF does not automatically resend it. The
owner or integration must reconcile it with the downstream system before
attempting the same effect again.

## 9. Implementation and direction {#implementation-and-direction}

As of this revision, Cards support named holders, shared balances,
cumulative limits, expiry, owner exceptions, revocation and statements.
The MCP gateway governs connected calls and supports asynchronous tasks.
Code integrations can use Guard or the decision API.

The execution-permit implementation includes issuance, connection custody
modes and references for a verifying tool and a customer-run relay.
Using it requires a deployment with permit signing enabled and a configured
verifier at the execution boundary. An existing sealed connection does not
migrate automatically. Consult the
[execution-permit guide](/docs/tool-permits) for setup and operational limits.

Further work concerns whose authority an agent represents, how a company
identity provider connects to it, and how authority is delegated to another
agent without widening it. Task-specific delegated authority belongs to
that direction. A shared Card currently grants its holders the same terms;
it is not a narrower child Card with a budget linked to its parent.

Those identity and delegation capabilities are design work, not features
this paper claims are available. Nor does the current task support imply
a transaction spanning every step of a job, automatic rollback, or a
global budget across independent Cards.

## 10. Adopting the pattern {#adopting-the-pattern}

Begin with one job whose consequences the business can describe, such as
issuing refunds. Name the actions it needs, establish trustworthy business
state, and choose the amounts or counts the owner is willing to delegate.

Connect the existing tool or put a verifier at its execution boundary.
Register the agent with its own credential. Issue a Card for the work and
remove alternate paths to the protected capability.

Exercise an allowed request, a held exception, a refused action, a retry
and a revoked Card. Check both the KIFF decision and whether the downstream
system actually performed the action. Confirm who receives a hold and
what happens when nobody answers.

The result should let the agent handle ordinary work while the business
retains a direct way to change or withdraw its authority. The owner can
make that decision without changing the agent's prompt or redeploying it.

## Technical references {#technical-references}

- [Give an agent a Card](/docs/quickstart).
- [Card rules and the decision API](/docs/kiff-cards).
- [MCP gateway](/docs/mcp-gateway).
- [Held calls and owner approvals](/docs/approvals).
- [Execution permits and credential custody](/docs/tool-permits).
- [Recorded MCP setup and tool call](/proof/videos/mcp-from-zero-en), using
  the production KIFF gateway and Stripe test mode.
- [Refund integration tutorial](/blog/from-a-refund-tool-to-a-governed-mcp-agent).
- [Framework technical paper](https://github.com/kiff/kiff/blob/main/docs/whitepaper.md)
  and [open-source framework](https://github.com/kiff/kiff), for domain
  definitions, state, validation and implementation details.

The framework paper remains a separate technical reference. This paper
describes the Cards product and the authorization model it builds on.
