---
name: kiff-domains
description: "Author KIFF reusable operational domains — the contract (entity, events, states, transitions, actions, authority, approvals, evidence) an agent proposes against. Use whenever someone wants to build, extend, split, or review one or many KIFF domains (a kiff.yaml), model a governed action (refund, payout, credit, access-change, support-ops), set approval thresholds and role permissions, or wire an agent through the decision boundary. Multi-domain by design: one contract per business slice, many domains per tenant, proposals routed per domain."
homepage: https://kiff.dev
docs: https://kiff.dev/docs/domains
license: MIT
---

# KIFF domains

A **domain** is the thing you build once with KIFF: the operational contract a
slice of your business runs against. Agents, humans, and services *propose*
consequential actions; KIFF **decides against the domain before anything runs**
and records a signed, tamper-evident receipt of what it decided and why.

A domain holds five things, in one place:

- **State** — the current status of an *entity* (an `Order` is `CREATED`, `PAID`,
  `REFUNDED`), derived from the *events* KIFF has seen.
- **Actions** — the consequential operations (`REFUND_ORDER`, `ISSUE_CREDIT`,
  `CREATE_PAYOUT`) and the states each is allowed in.
- **Authority** — which roles may propose which actions.
- **Approvals** — which actions hold for a human, keyed off `risk`.
- **Evidence** — the terminal outcome of each proposal and the receipt that
  proves it.

The domain outlives any one agent: connect a new agent or replace the old one
without redefining what "a legitimate refund" means.

## Multi-domain is the default

A tenant runs **many domains at once** — one per business slice (`refund`,
`payouts`, `support-ops`, `access-change`). Each is its own `kiff.yaml`. Never
cram unrelated entities into one domain; author one contract per slice and let
routing pick the right one.

How a proposal reaches the right domain:

- **`domain` selector** — the decide call reads `domain` from the request body;
  the event/execute surfaces read `?domain=<slug>`. The name is slugified, so a
  human name and its slug both match.
- **Empty selector → the tenant's default domain.** Single-domain tenants and
  no-selector callers keep working unchanged.
- **Domain-scoped API keys** — a key bound to one domain refuses a conflicting
  selector; a tenant-wide key may address any of its domains by selector.
- Each decision is stamped with its resolved domain, so the receipt knows which
  contract it was decided against.

Give each domain a distinct `domain:` name (slugs must not collide), keep entity
types and role vocabularies scoped to the slice, and remember state lives **per
domain** — seeding an entity in one domain does not seed it in another.

## The `kiff.yaml` grammar

```yaml
domain: refund-control        # required. Names the domain; slugified for routing.
entity: Order                 # required. The thing whose state you track.

events:                       # required, >=1. Facts KIFF has observed.
  - ORDER_PLACED
  - ORDER_PAID
  - ORDER_REFUNDED

states:                       # required, >=1. What the entity can be.
  - CREATED
  - PAID
  - REFUNDED

transitions:                  # event -> state change. from:"" is the initial state.
  - on: ORDER_PLACED
    from: ""
    to: CREATED
  - on: ORDER_PAID
    from: CREATED
    to: PAID
  - on: ORDER_REFUNDED
    from: PAID
    to: REFUNDED

actions:
  - name: REFUND_ORDER              # required, unique.
    allowed_states: [PAID]          # the states this action may run from.
    required_parameters: [amount, reason]   # missing -> outcome "invalid".
    required_permissions: [orders.refund]   # actor must hold these.
    risk: high                      # low | medium | high (default low).
    approval: required              # never | required (default never).
    executor: refund.issue_refund   # a registry key — NOT arbitrary code.
    requires_authority: true        # optional. This action cannot run
                                    # unless a KIFF Card has been issued
                                    # to the proposing actor for it.

permissions:
  roles:
    ops_agent:    [orders.refund]
    ops_operator: [orders.refund, orders.approve]
```

### Field rules (enforced at domain-set time)

- `domain`, `entity` — required, non-empty.
- `events`, `states` — required, >=1 each; **duplicates are rejected**.
- `transitions[].on` — required; must be a declared event.
  `transitions[].from` — `""` (initial) or a declared state.
  `transitions[].to` — required; must be a declared state.
- `actions[].name` — required, unique within the domain.
- `actions[].allowed_states` — each must be a declared state.
- `actions[].risk` — one of `low` | `medium` | `high` (omitted -> `low`).
- `actions[].approval` — one of `never` | `required` (omitted -> `never`).
- `actions[].executor` — required; must be a known executor key. Unknown keys
  fail validation.
- Unknown YAML fields are rejected — no silent typos.
- Validation accumulates **all** errors and reports them together, each with a
  field path (e.g. `actions[2].executor: unknown executor key ...`). A deeper
  build pass then checks state reachability and action-contract completeness.

### Executors

Actions reference a pre-approved **executor key** (e.g. `refund.issue_refund`,
`refund.mark_paid`, `support.escalate`), not arbitrary code — that is the trust
seam. A generic `cloud.proxy` executor lets your own app run the real side
effect after KIFF returns `allowed`. The decide path validates the key but does
not run it, so an action can reuse a registered executor without a redeploy.

## `requires_authority` — when an action needs an issued card

By default a card constrains without conferring: an action with no card
covering it is decided on state, permissions and parameters as usual.
Setting `requires_authority: true` inverts that for one action — a
proposal with no card covering `(actor, action)` is refused with
`no_mandate_issued` rather than allowed.

Declare it per action, on the consequential ones, and only where an
operator will actually issue cards. Rules of thumb:

- It is a **precondition, not a grant**. A card can never widen what
  `allowed_states` and `required_permissions` already permit; it narrows
  and caps. Do not use it in place of a permission.
- A domain where **every** action declares it is almost always wrong.
  The author's job is to say which consequences are worth an instrument.
- The first run after activating such a domain refuses until someone
  issues a card. That is intended — nothing is auto-issued — but say so
  in the domain's own notes, or it reads as broken.

## Decision outcomes

A proposal resolves to exactly one terminal outcome:

- **`allowed`** — contract satisfied; your executor runs, state advances, a
  receipt is minted.
- **`approval_required`** — held for a human (driven by `approval: required`);
  the executor does not run until an operator grants it, then it re-decides.
- **`blocked`** — refused before anything runs (`state_not_allowed` on a
  duplicate, `permission_denied` for a role without authority,
  `no_mandate_issued` when the action declares `requires_authority` and
  nothing has been issued to that actor, `key_not_bound_to_agent` when an
  API key not issued for any agent sends an action that requires a card or
  that a card of the named agent covers,
  `actor_not_key_holder` when a key issued for one agent names another,
  `mandate_limit_reached` when the
  action would exceed what the card has left in the window — not the same
  as the card being empty, since a smaller action may still fit).
- **`invalid`** — malformed proposal (missing required parameter, unknown action).

The no-self-approval rule holds: an agent cannot approve its own action.

## How you actually build one

You rarely start from a blank file:

1. **Attach the guard in observe mode** to an agent you already run. It watches
   real tool calls and derives a **starter domain** from actual traffic.
2. **Refine** the starter: name states, set `allowed_states`, mark the one or
   two actions you'd never let run unsupervised as `risk: high` /
   `approval: required`, assign role permissions.
3. **Activate** the domain, then point the guard at it in **enforce mode**.

Start from the single action you'd least like taken unsupervised, then grow the
domain around it.

## Connect it to KIFF Cloud

Authoring the YAML is half the job — the domain has to reach a runtime. KIFF
Cloud is the hosted runtime: base URL `https://api.kiff.dev`, every call
authenticated with `Authorization: Bearer <api_key>` (mint a key in the
dashboard). Self-hosting the framework is the same contract without the account.

Get the contract onto the tenant:

- `POST /v1/me/domain/validate` — check YAML (`Content-Type: application/yaml`)
  and get every grammar error back, with field paths, **without** storing.
- `PUT /v1/me/domain` — set the tenant's first (default) domain from YAML.
- Draft flow (what the authoring UI uses): `PUT /v1/me/domain/draft` then
  `POST /v1/me/domain/draft/activate`.
- `POST /v1/me/domain/template` — apply a prebuilt template by slug.
- **Multi-domain**: `POST /v1/me/domains` adds an additional (non-default)
  domain from validated YAML; `PUT /v1/me/domains/{slug}` re-authors one in
  place; `GET /v1/me/domains` lists them; `GET /v1/me/domains/{slug}/yaml`
  reads one back. The default domain is protected (delete the others first).

Propose and run against it:

- `POST /v1/proposals/decide` — the decision. Body:
  `{id, entity_id, entity_type, action_name, actor_id, parameters}`, plus
  `"domain": "<slug>"` to route on a multi-domain tenant.
- `POST /v1/entities/{id}/actions/{name}/execute` — run an allowed action and
  mint a signed receipt. Reach a non-default domain with `?domain=<slug>`.
- `POST /v1/events/raw` — seed / advance entity state (`?domain=<slug>`).
- `POST /v1/approvals/{id}/grant` — an operator grants a held action; the guard
  then re-decides.

A **domain-scoped API key** is pinned to one domain and refuses a conflicting
`domain` selector; a tenant-wide key routes by selector. In practice you rarely
POST `decide` by hand — attach **kiff-guard** to your agent (observe → derive a
starter domain → enforce) and it speaks this surface for you. The YAML above is
what the guard's derived starter draft becomes.

## Worked example — the two-door refund domain

Govern an `Order` with two "money doors" both allowed only from `PAID`; once the
order leaves `PAID`, every door returns `state_not_allowed`:

- `MARK_PAID` (low, never) — `CREATED -> PAID`.
- `AUTO_REFUND` (medium, never) — a small refund runs straight through.
- `REFUND_ORDER` (high, **required**) — a large refund holds for a human.
- `ISSUE_CREDIT` (medium, never) — an alternate PAID-only door.

The good refund runs, the risky one waits for approval, and the duplicate on an
already-refunded order is refused — before money moves.

## Self-check before delivering a domain

1. **One slice, one domain.** Unrelated entities -> separate files with distinct,
   non-colliding `domain:` names.
2. **State machine closes.** Every `on` is a declared event; every `from`/`to` is
   a declared state (or `""`); the entity can reach every state an action needs.
3. **Actions grounded.** Each `allowed_states` entry exists; each `executor` is a
   real key; each `required_parameter` is one your executor/app reads.
4. **Risk drives approval.** The actions you'd never let run unsupervised are
   `approval: required`. Don't gate the cheap, reversible ones.
5. **Authority explicit.** Every permission in `required_permissions` is granted
   to at least one role.
6. **Multi-domain safety.** A domain-scoped key matches the domain it addresses;
   a tenant-wide key names the domain per request.
7. **Reuse over rewrite.** Extend an existing domain's actions rather than
   spinning up a near-duplicate contract.

## Voice / honesty

Domain and evidence copy is property-led: *tamper-evident*, *verifiable*,
*signed receipt*, *decided before it runs*. A domain is the tenant's own
contract — KIFF checks conformance to authority the tenant declared; it never
invents authority and never becomes the executor. Never claim fabricated
customers, funding, metrics, or compliance.

_Learn more: https://kiff.dev/docs/domains · https://kiff.dev/docs/framework-quickstart_
