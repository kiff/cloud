# Receipts & proof packs

Every decision leaves a **receipt**: a signed record of what was authorized, on what basis, before
the action ran. A **proof pack** exports that evidence for one control over a window.
For anyone who has to show later what an agent was allowed to do.

## What a receipt proves

That at a given moment this action on this entity was evaluated against the recorded state and your
policy, and reached `allowed`, `blocked`, `approval_required` or `invalid` **before** it ran. KIFF
makes it, not the actor, and it's signed: a change breaks the signature.

## What it does not prove

- That the action executed. Your system runs `allowed` actions; KIFF doesn't.
- The action's outcome, such as money moving or an email arriving.
- Reviewer attribution beyond what the product records.

See [What KIFF is](/docs/what-kiff-is) for the other limits.

## Proof packs

The [Govern](/docs/govern) view and the exported pack are built from the same evidence, so they
can't differ. A pack holds the control's identity, decisions by outcome, the signed receipts and
whether they verify, and the posture. It opens with what it does and doesn't assert; a sampled check
never reads as the whole window.

![A receipt detail in the dashboard: the decision it records and its signature status, shown as Signed.](/static/img/docs/receipt-detail.png)

See also: [Decisions](/docs/decisions).
