# Limits, warnings and extra room

How a [Card](/docs/kiff-cards)'s limits count, and how to change them.
For owners and admins; only they can change a limit or add room.
Pick a window, raise a limit with **Limits**, or add room with **Extra room**.

| Window | In the API | Starts again |
|---|---|---|
| Each day | `calendar_day` | At midnight in the Card's time zone. |
| Any 24 h | `rolling_24h` | Always the last 24 hours. |
| Any hour | `rolling_1h` | Always the last 60 minutes. |
| Any 7 days | `rolling_7d` | Always the last 7 days. |
| Whole life | `lifetime` | Never: one total until the Card ends. |

## Day boundaries and time zone

Set the account's time zone in **Settings → Account** (such as
`Europe/Paris`; UTC if unset). It applies to new Cards only; existing
Cards keep theirs.

## A total for each item

To count each value of an argument apart ("one reminder a week for each
invoice"), add `per`:

```json
{"quantity": "count", "limit": 1, "window": "rolling_7d", "per": "invoice_id"}
```

A call with no value is held.

## Changing a limit

**Limits** on the Card's page re-issues it under the same name. Draws
already recorded still count, so raising a limit adds only the
difference.

## Extra room

**Extra room** on the Card's page opens **Review extra room**. Choose how
long: **Until the current window ends** (the default), **Until a date**,
or **For good (raises the base limit)**. Recorded usage stays when the
room ends. Over HTTP, `POST /v1/me/mandates/{id}/extra-room` with a
management identity.

## Budget warnings

KIFF sends a notice when a total is likely to run out soon, when it is
spent, on unusual usage, and when a new Card uses a limit fast. Change
or switch them off in the Card's **Settings → Budget warnings**.

Notices go to **Needs you**, the bell, email, and a
[webhook](/docs/reference-webhooks#card-warnings) that opts in. A
warning never blocks, approves or adds room.
