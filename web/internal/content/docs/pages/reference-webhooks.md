# Webhooks

An alert target is a URL KIFF posts to when a call is held or a Card warns.
For developers who want held calls and warnings in their own app.
Create one with `POST /v1/me/alert-targets` and check each delivery's signature.

## Held calls

A target with outcome `owner_required` receives each held call: `hold`
with its `id`, `review_url` and `expires_at`. Add `hold_answered` to
also get `kiff.hold.answered` with the new `status`. Payloads never carry
the call's arguments; read them through
[API fields](/docs/reference-api-fields#held-calls).

## Card warnings

A target opts in to [budget warnings](/docs/card-limits#budget-warnings):

```json
{"url": "https://example.com/kiff-warnings", "action": "card_budget", "outcomes": ["card_warning"]}
```

## Signing

Creating a target returns its `signing_secret` once. Each request
carries `KIFF-Signature: t=<unix seconds>,v1=<hex>`, the HMAC-SHA256 of
`<t>.<body>`. Recompute it, compare in constant time, and refuse an old
`t`. `POST /v1/me/alert-targets/{id}/rotate-secret` replaces the secret.

## Retries

A failed delivery is retried for about a day, then the target is
switched off; `POST /v1/me/alert-targets/{id}/enable` turns it back on.
Drop duplicates by `event_id`.

A phone app registers its own target; see
[Device signatures](/docs/reference-device-signatures#7-held-call-alerts-for-a-phone-app).
