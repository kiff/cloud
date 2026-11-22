# Receipts: step words

A receipt's `causal_chain` is signed. Each step's `message` is KIFF's English, and it stays exactly as signed. Every receipt already issued still verifies.

**Next to each receipt**, `GET /v1/receipts` and `GET /v1/receipts/{trace_id}` add `step_words`. It sits **outside the signature**: one entry for each step KIFF worded, in the reader's language (`Accept-Language`; English when KIFF doesn't have that language).

```json
"step_words": [
  {"sequence": 1, "code": "decision.allowed", "words": "Erlaubt"},
  {"sequence": 3, "code": "decision.allowed", "again": true, "words": "Erneut angefragt: Erlaubt"},
  {"sequence": 4, "code": "settled.rejected_next_step", "detail": "ask the customer", "words": "Zurückgewiesen. Nächster Schritt: ask the customer"}
]
```

- **`sequence`** is the step's own sequence number.
- **`words`** are what to show. Put the signed `message` under it, as the record.
- **`detail`** is the free part the label carries (a person's reason or next step, a tool's status), exactly as signed. KIFF never translates what a person or a vendor wrote.
- **`again`**: the call was asked again and got the same decision.
- **A step with no entry**: a business's own audit record, or a message KIFF doesn't recognise. Show its signed `message`.

KIFF reads `code` from the step's kind and its signed message (`StepCode`). So it works for receipts issued before `step_words` existed too.

## Step codes

A code never changes meaning. A new label gets a new code.

| code | the signed message it's read from |
|---|---|
| `decision.allowed` | `allowed` |
| `decision.refused` | `refused` |
| `decision.held_for_owner` | `held for its owner` |
| `decision.held_for_approval` | `held for approval` |
| `decision.approved_no_room` | `approved, waiting for room` |
| `decision.invalid` | `invalid` |
| (any decision, `again`) | `asked again: …` |
| `settled.approved_once` | `approved once` |
| `settled.rejected` | `rejected` |
| `settled.rejected_next_step` | `rejected, next step: <detail>` |
| `settled.card_changed` | `the card was changed instead` |
| `settled.fit_on_retry` | `nobody answered: on a retry it fit the card as it then stood` |
| `settled.expired` | `nobody answered before the hold expired` |
| `settled.withdrawn` | `withdrawn` |
| `settled.withdrawn_reason` | `withdrawn: <detail>` |
| `tool.reported` | `the tool reported: <detail>` |
| `handoff.handed` | `the taf handed this step to its owner; KIFF sends nothing for it` |
| `handoff.done` | `the owner says they did it` |
| `handoff.skipped` | `the owner chose not to` |
| `handoff.no_answer` | `nobody answered before it expired: never read as done` |
| `handoff.checked` | `the taf read the vendor after the owner's answer; the tool reported: <detail>` |
| `owner.halted` | the owner's halt |
| `owner.resumed` | the owner's resume |
| `state.not_allowed` | `state_not_allowed: <detail>` |

**Where the words live:** KIFF's words table, `api/internal/words/receiptsteps_<lang>.go`. A test checks that each language KIFF lists has words for every code, and another builds each step with the real builders and reads its code back.
