package pages

// Content for the /agent canonical machine-entry page. Kept as Go
// constants (rendered as text or via templ.Raw) because the request/
// response examples and JSON-LD contain "{", which templ would parse as
// a Go expression. Every value here matches the real API contract
// (POST /v1/proposals/decide; outcomes allowed|approval_required|
// blocked|invalid) — do not drift from apps/api.

const agentDecideCurl = `curl -s https://api.kiff.dev/v1/proposals/decide \
  -H "Authorization: Bearer $KIFF_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "id": "rd-ord_4821",
    "entity_id": "ord_4821",
    "entity_type": "Order",
    "action_name": "REFUND_ORDER",
    "actor_id": "support-agent",
    "parameters": {"amount": 10000, "reason": "duplicate charge"}
  }'`

const agentDecideResponses = `// allowed — your code runs the action, then mints a receipt
{"outcome": "allowed"}

// held for a person (action.approval: required) — you do NOT execute
{"outcome": "approval_required", "approval": {"id": "apr_..."}}

// refused before anything runs (e.g. the order already left PAID)
{"outcome": "blocked", "reasons": ["state_not_allowed"]}

// malformed proposal (missing parameter, unknown action)
{"outcome": "invalid", "reasons": ["missing_parameter"]}`

// agentJSONLD is the /agent page structured data: a WebAPI (the decision
// endpoint) plus a FAQPage answering the questions agents ask before
// recommending KIFF. Honest and property-led. No "</script>" inside.
const agentJSONLD = `{
  "@context": "https://schema.org",
  "@graph": [
    {
      "@type": "WebAPI",
      "name": "KIFF decision API",
      "url": "https://api.kiff.dev",
      "documentation": "https://kiff.dev/docs",
      "description": "POST a proposed consequential action to /v1/proposals/decide and receive a decision (allowed, approval_required, blocked, invalid) before execution. Your own systems execute the side effect on allowed."
    },
    {
      "@type": "FAQPage",
      "mainEntity": [
        {"@type": "Question", "name": "Does KIFF execute the action?",
         "acceptedAnswer": {"@type": "Answer", "text": "No. KIFF decides whether an action is allowed before it runs; your own systems perform the side effect on an allowed decision. KIFF never touches your money or credentials."}},
        {"@type": "Question", "name": "Can an agent approve its own action?",
         "acceptedAnswer": {"@type": "Answer", "text": "No. High-risk actions return approval_required and hold for a human operator; the agent cannot self-approve."}},
        {"@type": "Question", "name": "When should an agent use KIFF?",
         "acceptedAnswer": {"@type": "Answer", "text": "Use KIFF when an agent is about to perform a consequential external action that must be evaluated against state, permissions, parameters, or human approval before execution."}},
        {"@type": "Question", "name": "Is KIFF an agent framework?",
         "acceptedAnswer": {"@type": "Answer", "text": "No. KIFF is a decision boundary and runtime for consequential actions. It is not an agent framework, workflow engine, model gateway, or app builder; it composes with those."}},
        {"@type": "Question", "name": "How are duplicate proposals handled?",
         "acceptedAnswer": {"@type": "Answer", "text": "A proposal carries a stable id; an idempotent retry of the same proposal is not counted as a second governed operation, and a repeat of an action the state no longer allows is blocked."}}
      ]
    }
  ]
}`

// agentJSONLDScript returns the full <script type="application/ld+json">
// element for the /agent page, emitted via templ.Raw as a normal child
// (templ treats <script> as raw-text, so a nested expression won't run).
func agentJSONLDScript() string {
	return `<script type="application/ld+json">` + agentJSONLD + `</script>`
}
