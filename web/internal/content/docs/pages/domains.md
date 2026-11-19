# Reusable operational domains

A **domain** is what you build once with KIFF: the state, the valid actions, the authority and
approvals, and the evidence your agents act under.
For builders connecting their own agents to KIFF Cloud.
A [Card](/docs/kiff-cards) can only narrow what the domain declares, so the domain comes first.

## What a domain holds

- **State**: an entity's current status (an invoice is `PENDING` or `PAID`), from the events KIFF
  has seen.
- **Actions**: the consequential operations (`ISSUE_REFUND`, `PAY_INVOICE`) and the states each is
  allowed in.
- **Authority and approvals**: who may propose what, and what needs a person's sign-off.
- **Outcomes and evidence**: each proposal's result and the signed
  [receipt](/docs/receipts-and-proof) of what was decided, before execution.

A Card can never grant an action the domain doesn't have, or allow it in a state the domain forbids.

## Reuse

A domain outlives any one agent. Connect a new agent, replace one, or put a person or a service
through the same contract, without rewriting what "a legitimate refund" means. Reuse here means
across your own agents and services.

## Build one

A domain is a **`kiff.yaml` in your own repository**, reviewed in git like any code. Two ways in:

- **From what your agent already does.** Attach the guard in observe mode and it derives a starter
  domain from real traffic. See [Connect an agent](/docs/connect-an-agent).
- **With your coding agent.** Install the [kiff-domains skill](/skills/kiff-domains.md) and let it
  write the `kiff.yaml`, then `kiff verify` it locally.

Bring the finished `kiff.yaml` to KIFF Cloud to validate and activate it. The dashboard's **Build**
view only shows the running version; the source stays in your repo. Try it locally:
[Build a domain with the framework](/docs/framework-quickstart).

## More than one domain

Each domain has its own lifecycle and its own posture (`observe` or `enforce`, see
[Govern](/docs/govern)). A draft domain doesn't stop the others. An approval belongs to the domain
that required it and never authorizes an action in another, even one with the same name.

KIFF decides whether a proposed action fits the contract you declared. It never invents authority
and never executes. See [What KIFF is](/docs/what-kiff-is).
