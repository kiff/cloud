# KIFF Cards

A Card is what you give one AI agent: what it may do, how much, and until when.
For owners and admins who set what an agent may do.
Set the limits once; raise or revoke the Card when you need to.

For examples, see [Cards in practice](/docs/cards-in-practice).

## What a Card says

> `refund-agent` may run `REFUND_ORDER`, only while the order is `PAID`,
> up to a total of `500000` summed over `amount_cents`, each calendar
> day, until it is revoked.

- **Who holds it.** One agent, matched by the `actor_id` it sends. A Card
  can also be **shared** with up to 20 agents: they draw on the same totals.
- **What it may do.** The actions it covers, optionally only in some
  entity states, and optionally only on up to 100 named values of one
  parameter (refund only orders `1043` and `1044`).
- **How much.** `count` or `sum(<parameter>)`, with a ceiling and a
  window ([Limits, warnings and extra room](/docs/card-limits)). Each
  limit counts `attempts` (default for `count`) or `effects` (default
  for `sum`).
- **When it stops.** An optional expiry, or when an owner revokes it.
- **Past the limit.** **Ask** holds the call for an approver
  ([Held calls and approvals](/docs/approvals)). **Stop** refuses it with
  `mandate_limit_reached`.

## A Card narrows, never grants

A Card can only narrow what the domain already allows. Where several
Cards cover one action, the tightest one decides. A Card that cannot be
read refuses, and a revoked Card refuses with `mandate_expired`.

An agent with no Card for an action is unaffected, unless the action
declares `requires_authority: true` in the domain. Then it is refused
with `no_mandate_issued`. Drawing on a Card needs a key issued for its
holder ([Keys](/docs/reference-keys#which-key-can-use-a-card)).

## When a draw is recorded and given back

A draw is recorded when the action is authorized, not when it runs. A
retry of the same operation does not draw twice.

- An **attempt** limit keeps the draw even when the tool refuses.
- An **effect** limit gives it back only when a trusted executor proves
  the call had no effect
  ([trusted no-effect outcomes](/docs/mcp-gateway#trusted-no-effect-outcomes)).
  A timeout or a generic error is not proof.

A Card is drawn on even when the domain is in `observe`, so you can
issue one high, run for a week, and set the real limit from the
statement.

## The statement

**History → Statement** shows the ceiling, what was drawn, what remains,
and every movement. Refusals are not listed; they are in the domain's
activity. **History → Changes** shows who changed the terms. The API
fields are on [API fields](/docs/reference-api-fields#cards).

## Setting one

Use **Give an agent a Card** on a connected tool (see the
[quickstart](/docs/quickstart)) or **Cards → Issue a card**. Over HTTP,
with a management credential:

```bash
curl -sS -X POST https://api.kiff.dev/v1/me/mandates \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
        "id": "refund-agent-card",
        "title": "Refunds on paid orders",
        "subject": "refund-agent",
        "domain": "card-refund",
        "grants": [{"action": "REFUND_ORDER", "entity_states": ["PAID"]}],
        "aggregates": [
          {"quantity": "sum(amount_cents)", "limit": 500000, "window": "calendar_day"}
        ],
        "valid_until": "2027-01-01T00:00:00Z"
      }'
```

Only the signed-in owner or an admin can issue or revoke a Card.

## Starting an agent from a draft

A runtime can submit a draft of an agent and its Cards for a person to
confirm. See [Agent drafts](/docs/reference-agent-drafts).

## What a Card is not

Not a payment card: no money moves through KIFF. Not a rate limit: it
counts actions in their own units. Not a spend tracker: it counts what
KIFF authorized, not what your bank shows.
