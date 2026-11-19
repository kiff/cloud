# Agent drafts

A draft is a new agent and its [Cards](/docs/kiff-cards), submitted for a person to confirm.
For runtimes that start agents and must not hold a key that issues Cards.
Submit the draft, have a person confirm it, then claim the agent's key; nothing is granted before that.

## Submit, confirm, claim

1. **Submit.** `POST /v1/me/agent-drafts` with `{"agent_id", "label",
   "cards": [...]}`. Any credential on the account can submit. The
   answer carries the draft's `version` and a `claim_secret`, shown once.
2. **Confirm.** A person who may issue Cards and mint keys confirms the
   version they saw: `POST /v1/me/agent-drafts/{id}/confirm` with
   `{"version"}`. This issues the Cards.
3. **Claim.** `POST /v1/agent-drafts/{id}/claim` with
   `Authorization: Bearer <claim_secret>`. KIFF returns the agent's
   gateway key once.

A draft expires after 24 hours. `DELETE /v1/me/agent-drafts/{id}`
discards one not yet claimed and revokes any Cards it issued.

## One fingerprint for an install

When a held call installs the version this draft describes
(`version_install`), a phone can answer both with one signature: add
`"hold": {"id", "accounting_version"}` to the confirm, signed with its
approval key ([Device signatures](/docs/reference-device-signatures)).
The answer's `hold.status` is `approved` when the call was approved;
any other status means answer the hold the usual way.

## What the owner reads

Each Card in a draft has `lines` in the owner's words, such as "Refund a
payment: up to $40 each". `POST /v1/me/agent-drafts/preview` takes a
submit body and returns how it would read, storing nothing.

A preview also answers `contract_digest`, the tool contract its Cards were
worded over. A draft submitted with it is taken only while that contract
is still in force. If the account's tools changed in between, it's refused
(`409 contract_changed`), so what the person read is what KIFF checks.

A draft never counts an action that changes something over its whole life
across every item: `count`, `lifetime`, with no `per`. Signed that way, "one
reply per ticket, ever" would stop the agent after its first ticket. It's
refused with the total to draft instead: `count` `per` the action's
resource argument, beside the day's total. If the action names no single
item, the limit belongs in the agent's instructions. A lifetime budget (a
sum), a count per item, and a read's lifetime count are drafted as asked.

## Changing an agent's Cards

A draft for an agent that already holds Cards is a revision
(`"revision": true`): a Card with an id the agent holds gets its next
revision and keeps its usage. A `409` with `"reason": "card_revised"`
means the Card changed since the draft was made: draft it again. To go
back to earlier terms, use `POST /v1/me/agent-drafts/restore`.

## Testing a version

To test a revision before installing it, confirm with
`{"version", "as": "trial"}`. KIFF issues **test Cards** with their own
counters; the live Cards stay. Only gateway calls with the header
`KIFF-Trial: <draft id>` use them. Confirm with `{"version"}` to install
it, or `DELETE /v1/me/agent-drafts/{id}/trial` to end the test. Read the
results with `GET /v1/me/agent-drafts/{id}/trial`.
