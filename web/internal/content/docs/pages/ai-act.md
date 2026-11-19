# EU AI Act and KIFF

What KIFF Cloud gives a provider or deployer of a high-risk AI system for
Article 12 (logging) and Article 14 (human oversight), and what it doesn't.
You stay responsible for your own conformity assessment (Article 43).

Mapping v0.1, against the AI Act text of 12 July 2024 (Regulation (EU)
2024/1689). Where this page and
[EUR-Lex](https://eur-lex.europa.eu/eli/reg/2024/1689) differ, EUR-Lex
controls.

## Article 12, logging

KIFF Cloud records every governed action (`event_ingested`,
`state_changed`, `decision_recorded`, `action_validated`,
`action_executed`, `approval_required`, `approval_granted`,
`approval_denied`, `action_blocked`), and signs a Receipt for each one that
holds as evidence outside KIFF's own database.

It doesn't decide what you must log, set your retention periods, report to
supervisory authorities (Article 73), or tell you whether your system is
high-risk under Annex III.

## Article 14, human oversight

An action that needs approval stops until a person you designate reviews
it, and a denial is final for that proposal in that context.

It doesn't train your reviewers, choose which actions need oversight, set
your organisation's policy, or stop the AI system as a whole.

## Other articles

- **Article 15 (cybersecurity):** KIFF's own posture is on
  [Security](/security).
- **Article 26 (deployers):** the audit chain and Receipts are meant as
  evidence of your monitoring and oversight.
- **Articles 10 and 17:** out of scope.

## A Receipt as evidence

One Receipt carries, for one governed action, the chain of events, state,
decision and validation behind it (Article 12); who approved it, when
approval was needed (Article 14); and a signature over the chain you can
check without KIFF's database (Article 15). See
[Receipts and proof](/docs/receipts-and-proof).

## Limits

KIFF is not a notified body, an accredited conformity assessor or an
authorised representative under the AI Act. If your assessment finds
obligations this page doesn't cover, KIFF isn't the tool for them.
Questions: [legal@kiff.dev](mailto:legal@kiff.dev).
