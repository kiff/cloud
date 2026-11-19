# Decisions

A decision is KIFF's answer to "is this action allowed, right now?", given **before** the action runs.
For builders wiring their runtime to KIFF.
Run the action only on `allowed`; anything else means don't run it.

## The flow

```
your runtime proposes an action
        │
        ▼
KIFF evaluates: the entity's state (from events), the policy,
                required parameters, the actor's authority, approvals,
                and, only where the action declares requires_authority,
                the card issued to this actor and what it has left
        │
        ▼
allowed            → run it
blocked / invalid  → don't run it
approval_required  → hold until a person resolves it
        │
        ▼
one signed receipt recorded, either way
```

## The four outcomes

- **`allowed`**: the proposal fits the policy in this state. Your runtime runs it.
- **`blocked`**: a valid request KIFF refused (wrong state, over a threshold, no authority).
  Respect it.
- **`approval_required`**: held for a person's sign-off. See [Held calls and approvals](/docs/approvals).
- **`invalid`**: a broken request, not a decision. A `4xx` (missing or bad parameter, invalid
  contract, no executor) is yours to fix; a `5xx` is a brief outage to retry.

In enforce mode through the guard, an error fails closed. See [How kiff-guard works](/docs/concepts).

## Where a card is required

An action without `requires_authority` is decided without a card. Where it's declared:

- **`no_mandate_issued`**: nothing has been issued to this actor for this action.
- **`key_not_bound_to_agent`**: the API key isn't issued for an agent. Use the key from the
  agent's card page.
- **`actor_not_key_holder`**: the key is issued for one agent and the proposal names another.
  Checked on every action.
- **`mandate_limit_reached`**: the action would exceed what the card has left in this window. The
  card need not be empty; a smaller action may still pass. Refused, not sent for approval.

The draw is recorded when the action is authorized, not when your runtime runs it. See
[KIFF Cards](/docs/kiff-cards).

## What is evaluated

The entity's state from the events KIFF recorded, never a value the caller asserts; your policy; and
the actor's identity, resolved server-side. An action is attributed to the API key that sent it, so
give each agent its own key.

## Example

```text
proposal:   action=ISSUE_REFUND  entity=order/ord_4821  amount_cents=2500
decision:   outcome=blocked  reason=state_not_allowed  (order already FULLY_REFUNDED)
```

The request and response contract lives in [`kiff/kiff-guard`](https://github.com/kiff/kiff-guard).
KIFF never executes the action or undoes anything downstream: see
[Receipts & proof packs](/docs/receipts-and-proof).
