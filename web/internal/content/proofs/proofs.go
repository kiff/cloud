// Package proofs is the data registry for the six cookbook proof
// infographics served at /proof and /proof/{slug}.
//
// Each proof mirrors one runnable recipe in the public guard repo
// (kiff/kiff-guard/cookbook). The narrative + numbers here are the
// same ones the sales deck cites; the runnable code lives in the
// guard repo and is linked, not duplicated. The source of truth for
// the recipe roster is docs/integration/cookbook-recipes.md.
//
// These pages are reached from the sales deck (one clickable card per
// proof) and explain a single risk shape visually: the ungoverned
// loss, the state-aware block, and why the state forbade the repeat.
// Per the web binary is hermetic Go + templ + native CSS — the
// infographics are data + templ, no JS, no build step.
package proofs

// Proof is one cookbook risk shape rendered as an infographic. The
// fields drive a single shared visual template, so every proof reads
// the same way: the loop, the without/with money contrast, and the
// state-machine reason the repeat was refused.
type Proof struct {
	// Slug is the URL stem (/proof/{slug}) and the card target.
	Slug string
	// Recipe is the cookbook recipe directory name in kiff-guard.
	Recipe string
	// Title is the short risk label shown on the card + page head.
	Title string
	// Tagline is the one-line "what it proves" blurb.
	Tagline string

	// Framework is the agent framework the recipe runs in.
	Framework string
	// Regulation is the named rule the block maps to, or "" when the
	// risk is purely economic (no specific regime cited).
	Regulation string

	// Entity / Action / BlockState describe the governed action and
	// the state that forbids the repeat (the heart of the proof).
	Entity     string
	Action     string
	BlockState string

	// Without / With are the ungoverned-vs-governed outcome. Unit is
	// "money" (render with the loss styling) or "count" (plain).
	Unit          string // "money" | "count"
	WithoutValue  string
	WithoutDetail string
	WithValue     string
	WithDetail    string

	// Scenario is the 2-3 sentence story; Why explains the refusal in
	// state-machine terms.
	Scenario string
	Why      string

	// Accent picks the card/hero color: "loss" amounts use danger,
	// counts use warn. Cosmetic only.
	Accent string // "money" | "count"
}

// proofs is the ordered registry. Order is the card order in the
// deck and on the /proof index — the headline duplicate-payment proof
// first, then the five more risk shapes the deck names.
var proofs = []Proof{
	{
		Slug:          "duplicate-payment",
		Recipe:        "duplicate-payment-guard",
		Title:         "Duplicate payment",
		Tagline:       "An AP agent retries a $10K payment ten times on a flaky connection.",
		Framework:     "OpenClaw",
		Regulation:    "",
		Entity:        "Invoice",
		Action:        "PAY_INVOICE",
		BlockState:    "PAID",
		Unit:          "money",
		Accent:        "money",
		WithoutValue:  "$100,000",
		WithoutDetail: "10 debits on one invoice. Every retry pays again.",
		WithValue:     "$10,000",
		WithDetail:    "1 debit allowed. 9 retries refused.",
		Scenario:      "An agent pays a $10,000 invoice. A flaky connection drops the success response, so the transport retries the same call ~10 times. Each call is individually legitimate — same invoice, same amount — but only the first should ever debit.",
		Why:           "After the first debit the invoice advances to PAID. PAY_INVOICE is allowed only while the invoice is PENDING, so every retry is refused with state_not_allowed. A state-aware gate stops the emergent repeat that no single call looks guilty of.",
	},
	{
		Slug:          "over-refund",
		Recipe:        "refund-ceiling-guard",
		Title:         "Over-refund",
		Tagline:       "A support agent refunds a $100 order past its ceiling.",
		Framework:     "LangGraph",
		Regulation:    "",
		Entity:        "Order",
		Action:        "ISSUE_REFUND",
		BlockState:    "FULLY_REFUNDED",
		Unit:          "money",
		Accent:        "money",
		WithoutValue:  "$250",
		WithoutDetail: "Refunds keep issuing past the order total.",
		WithValue:     "$100",
		WithDetail:    "Refund capped at the order. 3 over-refunds refused.",
		Scenario:      "A support agent issues refunds against a $100 order. Nothing in the prompt stops it issuing a second and third refund — the model just keeps helping.",
		Why:           "Once cumulative refunds reach the order total the order is FULLY_REFUNDED. ISSUE_REFUND is allowed only while refundable headroom remains, so every refund past the ceiling is refused.",
	},
	{
		Slug:          "collections-promise",
		Recipe:        "collections-promise-guard",
		Title:         "Collections contact",
		Tagline:       "A collections agent contacts a customer after a promise to pay.",
		Framework:     "Agno",
		Regulation:    "FDCPA / CONC",
		Entity:        "Account",
		Action:        "CONTACT_CUSTOMER",
		BlockState:    "PROMISE_TO_PAY",
		Unit:          "count",
		Accent:        "count",
		WithoutValue:  "5 contacts",
		WithoutDetail: "Repeated outreach while a promise is in effect.",
		WithValue:     "1 contact",
		WithDetail:    "4 contacts refused while the promise holds.",
		Scenario:      "A collections agent works an account. The customer makes a promise to pay, then the agent — following its script — keeps reaching out anyway.",
		Why:           "A recorded promise-to-pay moves the account into a protected window. CONTACT_CUSTOMER is not allowed in that state, so further contact is refused — the line between routine follow-up and an FDCPA / CONC breach.",
	},
	{
		Slug:          "chargeback-dispute",
		Recipe:        "chargeback-dispute-guard",
		Title:         "Chargeback resubmit",
		Tagline:       "A disputes agent resubmits the same chargeback.",
		Framework:     "Strands",
		Regulation:    "",
		Entity:        "Dispute",
		Action:        "SUBMIT_DISPUTE",
		BlockState:    "SUBMITTED",
		Unit:          "money",
		Accent:        "money",
		WithoutValue:  "$125",
		WithoutDetail: "Each resubmit racks up another scheme fee.",
		WithValue:     "$25",
		WithDetail:    "Submitted once. 4 resubmits refused.",
		Scenario:      "A disputes agent files a chargeback, then resubmits the same dispute when it doesn't see an immediate result. Each submission carries a scheme fee.",
		Why:           "Once filed, the dispute is SUBMITTED. SUBMIT_DISPUTE is allowed only from the open state, so every resubmit of the same dispute is refused before it bills another fee.",
	},
	{
		Slug:          "vulnerability-escalation",
		Recipe:        "vulnerability-escalation-guard",
		Title:         "Vulnerability escalation",
		Tagline:       "A customer-ops team keeps acting after a vulnerability signal.",
		Framework:     "Agno Teams",
		Regulation:    "FCA Consumer Duty",
		Entity:        "Case",
		Action:        "TAKE_ACTION",
		BlockState:    "VULNERABLE_FLAGGED",
		Unit:          "count",
		Accent:        "count",
		WithoutValue:  "6 actions",
		WithoutDetail: "A whole team keeps acting on a flagged customer.",
		WithValue:     "1 action",
		WithDetail:    "Team halted the moment the flag lands.",
		Scenario:      "A customer-ops team — several agents on one case — keeps taking routine actions after a vulnerability signal is raised. No single agent sees the whole picture.",
		Why:           "The vulnerability flag moves the case into VULNERABLE_FLAGGED. Routine actions are not allowed in that state, so the gate halts the entire team at once — the kind of cross-agent stop FCA Consumer Duty expects.",
	},
	{
		Slug:          "kyb-verification",
		Recipe:        "kyb-verification-guard",
		Title:         "Repeated KYB check",
		Tagline:       "A KYB workflow re-runs a paid bureau check.",
		Framework:     "Agno Workflows",
		Regulation:    "",
		Entity:        "Business",
		Action:        "RUN_BUREAU_CHECK",
		BlockState:    "VERIFIED",
		Unit:          "money",
		Accent:        "money",
		WithoutValue:  "$60",
		WithoutDetail: "The workflow re-bills the bureau on every pass.",
		WithValue:     "$12",
		WithDetail:    "Checked once. 4 re-runs refused.",
		Scenario:      "A KYB onboarding workflow re-runs the same paid bureau check on every pass through the graph. Each run bills the bureau again for an answer already on file.",
		Why:           "After a successful check the business is VERIFIED. RUN_BUREAU_CHECK is allowed only while verification is pending, so the workflow's re-runs are refused before they re-bill.",
	},
}

// All returns the ordered registry (for the index and the deck cards).
func All() []Proof {
	out := make([]Proof, len(proofs))
	copy(out, proofs)
	return out
}

// Lookup returns a proof by slug and whether it exists.
func Lookup(slug string) (Proof, bool) {
	for _, p := range proofs {
		if p.Slug == slug {
			return p, true
		}
	}
	return Proof{}, false
}
