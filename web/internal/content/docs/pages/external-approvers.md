# Connect an external approver

A service you run that answers held calls on an Ask Card, up to a limit an admin sets.
For teams that want routine held calls answered by their own rules.
Grant it access on the Card, then run the connector; a person can still answer.

The connector and its examples are in the
[kiff-guard cookbook](https://github.com/kiff/kiff-guard/tree/main/cookbook/external-approver).
It needs Python 3.9.

## 1. Grant access in KIFF

Open [Cards](https://app.kiff.dev/cards), choose an Ask Card with a
per-call amount limit, open its **Approvers** tab and choose **Connect an
approver**. Set the action, the maximum amount per call, a name and an
end (up to 90 days). Only an admin can do this.

Copy the `kiff_approver_…` credential, shown once, and keep it where the
agent cannot read it.

## 2. Run the connector

```sh
git clone https://github.com/kiff/kiff-guard.git
cd kiff-guard/cookbook/external-approver
cp connector.example.json connector.json
cp refund-policy.example.json refund-policy.json
read -r -s KIFF_APPROVER_KEY
export KIFF_APPROVER_KEY
python3 connector.py --config connector.json
```

Set `context_file` in `connector.json` to `refund-policy.json` and adapt
the policy to your action. Replace the example's made-up facts with
facts from your own systems, never the agent's claims.

KIFF does not host the service. Revoke the connection on the Card to
stop it.

## The API

The credential reaches only `GET /v1/approver/holds`,
`GET /v1/approver/holds/{id}` and `POST /v1/approver/holds/{id}/answer`
(`review_token`, `decision` of `approve`, `reject` or `defer`, and
`reason_code`). A `409` means the hold was already answered, expired or
changed: read it again.
