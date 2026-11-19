# candidate — runtime-grounded candidate controls

Derives **candidate controls** from a **connected `kiff-guard` runtime**'s
stored observation, grounded in the same `ToolMap` bindings the adapter uses
to intercept execution (`guard.evaluate`) — never an uploaded manifest.

> Origin: this began as a throwaway spike, now closed and proven. The
> derivation core graduated into a production
> path; the dev CLI and fixtures below remain dev-only.

## Production endpoint — `GET /v1/guard/candidates`

Authenticated, tenant-scoped, read-only. Derives candidates from the tenant's
**stored guard observations** (`POST /v1/guard/observations`) by pairing each
connected runtime with its observation and running `DeriveForObservation`
(`derive.go`) — the promoted spike core (`DeriveFromRuntime` + `Validate`),
callable from API code with no dependency on the dev CLI or fixtures.

Optional query params scope to a single runtime: `project`, `environment`,
`agent_id`, `workflow`. Absent params derive across every connected runtime
that has pushed an observation.

Response shape (stable for Studio onboarding):

```json
{
  "source": "kiff-guard runtime",
  "runtimes": [
    {
      "runtime": { "agent_id": "...", "adapter": "...", "project": "...",
                   "environment": "...", "workflow": "...", "mode": "..." },
      "candidates": [
        {
          "tool": "pay_invoice", "action": "PAY_INVOICE",
          "entity_type": "Invoice", "entity_arg": "invoice_id",
          "fields": [ { "name": "amount_cents", "type": "integer", "required": true } ],
          "risk": "high", "risk_reasons": ["matched signal: pay"],
          "evidence": { "source": "kiff-guard runtime", "tool": "pay_invoice",
                        "adapter": "openclaw", "project": "cookbook",
                        "environment": "aws", "workflow": "duplicate-payment",
                        "mode": "enforce" },
          "observed_call_count": 4,
          "policy_thresholds": null,
          "states": null
        }
      ],
      "grounding": { "ok": true, "checks": [ /* per-check report */ ] }
    }
  ]
}
```

Honesty boundary (enforced by `validate.go`, reused verbatim): evidence cites
the runtime; `policy_thresholds` and `states` are always `null` — KIFF derives
the action surface, never thresholds or the state machine. `risk_reasons` are
deterministic Path A labels (Bedrock Path B is a follow-up). Empty state:
`{"source":"kiff-guard runtime","runtimes":[]}`.

The web SDK consumer is `apiclient.Client.ListGuardCandidatesBearer`
(the web app's dashboard client), the integration point feeding the
action-first onboarding flow.

---

## Spike provenance (dev-only)


## What it proves

```
connected kiff-guard runtime
→ ToolMap / runtime metadata (guard.Connection + guard.Observation)
→ candidate control derived from those bindings  (DeriveFromRuntime)
→ candidate cites source: "kiff-guard runtime"   (Evidence)
→ grounding gate confirms nothing was invented    (Validate)
```

The candidate is derived from the **same binding the adapter uses to
intercept execution** (`guard.evaluate`), not from an uploaded
`tools/list` manifest.

## Run it

```bash
# bundled Fixture A (refund-ceiling-guard, single tool)
go run ./cmd/spike-candidate

# Fixture B (observe-quickstart, multi-tool ranking)
go run ./cmd/spike-candidate internal/candidate/testdata/observe_quickstart.json

# any captured runtime fixture (e.g. from a live demo instance)
cat runtime.json | go run ./cmd/spike-candidate -

# tests
go test ./internal/candidate/...
```

## Fixture A — single-tool anchor

`testdata/refund_ceiling_guard.json` — hand-derived from
the guard's refund-ceiling-guard cookbook example:
`ToolMap().bind("issue_refund", action="ISSUE_REFUND", entity_type="Order",
entity_arg="order_id")`, the `issue_refund(order_id, amount_cents)` signature,
and the decision engine's domain (`RequiredParameters: ["amount_cents"]`,
`Risk: RiskHigh`). Mode is `observe` (observe-first).

## Fixture B — multi-tool ranking

`testdata/observe_quickstart.json` — the `observe-quickstart` tool set as a
connected runtime would report it (grounded in
`cookbook/observe-quickstart/agent/support_agent.py`): `refund_order`,
`send_email`, `escalate_ticket` with their real docstrings + signatures.
observe mode has **no ToolMap**, so action is derived from the tool name and
entity bindings stay empty (never fabricated). Deterministic Path A ranking
puts the money-moving `refund_order` (high) above the comms / escalation tools
(medium). This cookbook has no read-only tool, so the proof here is
high-over-medium; a `low` tier appears when a read-only tool is present.

## Scope vs the full spike

| In this slice | Follow-up |
|---------------|-----------|
| Path A (deterministic) risk + **mechanical** risk_reasons | Path B (Bedrock) readable risk_reasons + comparison matrix |
| Single-tool Fixture A + multi-tool Fixture B ranking | — |
| Grounding validator, evidence, null thresholds/states, governance-seam match | Full live loop (connect → confirm → real call → cloud receipt) |
| Derivation + fixture validation | Studio candidate-card UI |

## Map to the spike and its mockup

- **The spike:** this is the smallest runtime-first slice of the
  hypothesis — kiff-cloud derives candidate controls from the exact
  `ToolMap` bindings a connected runtime uses, grounded and honest.
- **The mockup:** the onboarding review screen is the UX for this data. The candidate shape here (tool / action / entity / fields /
  risk / risk_reasons / evidence + null policy) is what that Review card
  renders. Evidence `source: kiff-guard runtime` matches the mockup's
  evidence chip.
