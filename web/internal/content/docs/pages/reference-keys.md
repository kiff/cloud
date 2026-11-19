# Keys

Which key does what: an agent's key, a read-only account key, and when keys expire.
For admins creating keys for agents and scripts.
Give each agent its own key; use a read-only account key for reports.

## Which key can use a Card

A key issued for an agent acts only as that agent: a proposal naming
anyone else in `actor_id` is refused with `actor_not_key_holder`. Drawing
on a [Card](/docs/kiff-cards) needs such a key; a key bound to no agent
is refused with `key_not_bound_to_agent`.

**Create a key for this agent**, on a Card's page, issues the right key.
To add a role to an existing key, use `PATCH /v1/keys/{id}` with
`{"add_roles": [...]}`. Roles can't be removed: revoke the key and issue
another.

## Read-only account key

For a script or report that reads the account: on **API keys**, choose
**Create API key**, then **Read-only account key**. It is shown once and
reads only `GET /v1/me`, `/v1/me/activity`, `/v1/me/card-exceptions`,
`/v1/me/mandates` (with statements) and `/v1/me/stream`. Anything else
answers `403 read_only_key`.

Over the API: `POST /v1/keys {"label": "…", "roles": ["viewer"]}` with
no `agent_id`.

## Key expiry

Account keys always expire: in 90 days by default, at most a year. An
agent's key doesn't expire unless you set `expires_at`. An expired key
answers `401`.
