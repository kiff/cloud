# Held calls and approvals

A held call is a call outside an agent's [Card](/docs/kiff-cards) that waits for a person.
For owners and approvers who answer held calls.
Open it on **Needs you** and choose **Approve once**, **Review extra room** or **Reject** before it expires.

A Card that says **Ask** holds the call; one that says **Stop** refuses
it. Nothing is sent to the tool until someone answers.

## How long a call waits

Each Card sets the wait: **10 minutes** by default, from 1 minute to 7
days. Silence never approves: if nobody answers in time, the call is
refused, a late answer is not accepted, and the agent must ask again.

## Where you hear about it

- **Needs you** in KIFF Cloud, and the bell.
- **Email** to the account owner, one per held call.
- **The agent's chat** in Claude Code, if on in **Settings → Account →
  Held calls**.
- Your own app, through a [webhook](/docs/reference-webhooks).

All of them link to the same Needs you page. A forwarded link answers
nothing without a signed-in session that may approve. The agent's key
cannot answer, and a "yes" typed in the chat is not an approval.

## Who can answer

A signed-in person who may approve, or a service an admin connected as
an [external approver](/docs/external-approvers). A phone app answers
with a [device signature](/docs/reference-device-signatures).

The page shows the call from KIFF's own record: the agent, the tool, the
Cards it is outside of and by how much, and every argument. Whether it
is the right order is for you to judge. The API fields are on
[API fields](/docs/reference-api-fields#held-calls).

## The three answers

- **Approve once** lets this exact call (same tool and arguments) past
  the per-call limit. It still counts toward every total on the Card;
  approving cannot lift a total.
- **Review extra room** adds room to a total, until the current window
  ends by default. See [Extra room](/docs/card-limits#extra-room).
- **Reject** refuses the call, and the agent is told not to retry it.

To change the Card for good from a held call, see
[Allow this from now on](/docs/reference-api-fields#allow-this-from-now-on).

## What the agent does

The agent retries **the same call** to collect the answer
([how repeats are matched](/docs/mcp-gateway#operation-ids-and-held-calls)).
An approved call is sent once; a rejected or expired one is refused. If
the Card was revoked or other calls used the room meanwhile, it is not
sent: it shows **Approved, waiting for room** until the budget is raised.

If the run is cancelled, the agent withdraws the call with its own key:

```bash
curl -sS -X POST https://api.kiff.dev/v1/me/holds/$EXCEPTION_ID/withdraw \
  -H "Authorization: Bearer $AGENT_KEY"
```

`exception_id` is in the held result's `_meta`.

## Approval and the tool's outcome

Approval records who authorized the call, not that the tool did it. KIFF
records the tool's reported outcome beside it: **Tool reports success**,
**Tool reports an error**, **Outcome unknown** (check with the tool
first) or **Outcome not recorded**.
