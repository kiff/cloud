# API fields

The fields an app reads to show Cards, held calls and activity in KIFF's own words.
For developers building an app on the KIFF API.
Show these fields as given rather than writing your own text; the API calls a Card a **mandate**.

## Cards

`GET /v1/me/mandates?include=use` lists the Cards with their `limits`;
`GET /v1/me/mandates/{id}/statement` returns one Card's statement. Each
limit, in the current window, has:

- `limit`, `used`, `remaining` (extra room included) and `window_started`;
- `window_end`, for a day's limit only;
- `quantity` (`"sum(amount_cents)"`) and `quantity_of: {kind, parameter}`;
- for a summed limit, `unit` and `scale`: `20000`, `EUR`, `2` reads €200.00.

When reissuing with `POST /v1/me/mandates`, send each grant's
`resources` as it stands: leaving it out removes the limit. `title` is
the Card's name (60 characters at most); `PUT /v1/me/mandates/{id}/title`
renames it.

`draft_id` names the [agent draft](/docs/reference-agent-drafts) whose
confirmation or trial issued the Card's current terms, to group Cards by
draft; a Card last issued directly has none.

## Held calls

`GET /v1/me/card-exceptions` and `/{id}` describe a held call:

- `summary`: the tool's title ("Refund a payment");
- `amount` (`{minor, unit, scale}`) and `amount_text` ("$45");
- `reason_text`: why it's held ("$45 is over the $40 limit for one call");
- `details`: every argument written out in full, never shortened: a plain value on its line (`charge: ch_1`), a list's items (`1`, `2`, ...) and an object's fields below its name, two spaces deeper each level, keys sorted. A line break in a value is a space. A held browser call's steps are listed one by one;
- `expires_at`, `answer_url` and `owner_answer`;
- `taf_about` and `taf_reason`: the agent's own words. Label them as the
  agent's, never in place of KIFF's text;
- `kind`: `hold`, or `card_unit` for calls refused because the Card has
  no limit in their unit. A `card_unit` can't be approved once: change
  the Card or dismiss it.

## Activity

In `GET /v1/me/activity`, each call has `name`, `kind` (`done`,
`approved`, `not_done`, `sending`, `unknown`, `held` or `refused`),
`detail` and `amount_text`. `tool_outcome` is what the tool reported;
with `refused_by: "connection"`, nothing reached the tool. Show `detail`.
`tool_outcome.counted` is `false` when the tool refused the call (or it was
never sent) and the Card gave its room back, so the call does not count
toward the day's limit. It is absent for every other call and when KIFF
can't say: treat absent as "counts".

## Allow this from now on

`POST /v1/me/card-exceptions/{id}/allow-draft` with `{}` drafts the
narrowest Card change that would have let a held call through, with
`before` and `after` for each change. Nothing changes until the owner
confirms the [draft](/docs/reference-agent-drafts). It answers
`{"none": ...}` when nothing narrow fits. A confirm can also answer the
held call; see
[One fingerprint for an install](/docs/reference-agent-drafts#one-fingerprint-for-an-install).
