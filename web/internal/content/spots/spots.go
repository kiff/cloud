// Package spots is the data registry for the enablement proofs served at
// /spot and /spot/{slug}.
//
// Where the /proof gallery shows the boundary *stopping the unsafe repeat*
// (loss prevention), /spot shows the other half of the same mechanism: the
// boundary is what lets you *put an agent on a money-making flow in the first
// place*. Each spot mirrors one runnable enablement recipe in the public guard
// repo (kiff/kiff-guard/cookbook) and leads with the agent doing the work, not
// the block. The source of truth for the recipe roster is
// docs/integration/cookbook-recipes.md.
//
// Per the web binary is hermetic Go + templ + native CSS — these are data +
// templ, no JS, no build step.
package spots

// Spot is one enablement flow rendered as an infographic. The fields drive a
// single shared visual template, so every spot reads the same way: the flow
// you ship, the line it crosses, and why the boundary makes it shippable.
type Spot struct {
	// Slug is the URL stem (/spot/{slug}) and the card target.
	Slug string
	// Recipe is the cookbook recipe directory name in kiff-guard.
	Recipe string
	// Flow is the value-creating flow you can put an agent on (card + head).
	Flow string
	// Tagline is the one-line enablement blurb.
	Tagline string

	// Framework is the agent framework the recipe runs in.
	Framework string
	// Model is the live model the proof was run on (honest: what we ran).
	Model string

	// Entity / Action describe the consequential action the agent performs.
	Entity string
	Action string
	// OpenState is the state the action is allowed from; DoneState is where the
	// entity lands after, where the repeat becomes a no-op.
	OpenState string
	DoneState string

	// Line is the consequential line the action crosses (why it mattered before).
	Line string
	// Before is what the flow used to require (manual / off-limits to agents).
	Before string
	// After is what you ship now (agent on the flow, boundary holds).
	After string

	// HowItShips is the 2-3 sentence flow story.
	HowItShips string
	// WhyConfident explains, in state-machine terms, why the team can ship it.
	WhyConfident string
}

// spots is the ordered registry. Order is the card order on the /spot index.
var spots = []Spot{
	{
		Slug:         "close-deals",
		Recipe:       "deal-close-enablement-guard",
		Flow:         "Close deals with a discount",
		Tagline:      "Put an agent on closing. It applies the real discount to win the deal; it can't stack margin away.",
		Framework:    "Agno",
		Model:        "Bedrock · Nova Pro",
		Entity:       "Deal",
		Action:       "APPLY_DISCOUNT",
		OpenState:    "OPEN",
		DoneState:    "DISCOUNTED",
		Line:         "APPLY_DISCOUNT commits real margin to win the deal.",
		Before:       "Closing discounts went through a human — an agent that stacks a second discount when it doesn't see a result gives margin away every retry.",
		After:        "The agent applies the closing discount itself. The boundary makes it exactly once.",
		HowItShips:   "A sales agent applies a 15% closing discount on an OPEN deal. Nothing in the prompt stops it applying a second and third — the model just keeps closing. You hand it the flow anyway, because the boundary holds the deal to one discount.",
		WhyConfident: "Once a discount is applied the deal advances to DISCOUNTED, and APPLY_DISCOUNT is allowed only while OPEN — so every repeat is a no-op (state_not_allowed). The agent owns closing; the margin stays intact.",
	},
	{
		Slug:         "issue-refunds",
		Recipe:       "refund-enablement-guard",
		Flow:         "Issue customer refunds",
		Tagline:      "Give a support agent the refund button. Customers get their money back; the order can't be paid out twice.",
		Framework:    "Agno",
		Model:        "Bedrock · Claude Haiku 4.5",
		Entity:       "Order",
		Action:       "ISSUE_REFUND",
		OpenState:    "PAID",
		DoneState:    "REFUNDED",
		Line:         "ISSUE_REFUND sends real money back to a customer.",
		Before:       "Refunds sat in a human queue — an agent that retries on a flaky connection pays the same refund out again and again.",
		After:        "The agent issues the refund directly. The boundary clears the real one and absorbs the retries.",
		HowItShips:   "A support agent issues a refund on a PAID order and it goes through. A flaky connection makes it retry the same call; each retry is individually legitimate, but only the first should ever pay out. You put the agent on refunds, because the boundary makes the rest no-ops.",
		WhyConfident: "After the refund the order advances to REFUNDED, and ISSUE_REFUND is allowed only from PAID — so every retry is declined (state_not_allowed) before it double-pays. The agent owns refunds; the money moves once.",
	},
	{
		Slug:         "recover-payments",
		Recipe:       "payment-recovery-enablement-guard",
		Flow:         "Recover failed payments",
		Tagline:      "Put a dunning agent on past-due invoices. It charges once to recover; it can't hammer the card on every retry.",
		Framework:    "Agno",
		Model:        "Bedrock · Nova Pro",
		Entity:       "Invoice",
		Action:       "RETRY_PAYMENT",
		OpenState:    "PAST_DUE",
		DoneState:    "RECOVERED",
		Line:         "RETRY_PAYMENT hits the customer's card to recover a missed payment.",
		Before:       "Dunning ran in batch windows or needed a human to approve each retry — an agent left unguarded hits the card five times on the same past-due invoice.",
		After:        "The agent retries the charge on its own. The boundary makes it recover once and stop.",
		HowItShips:   "A dunning agent retries the charge on a PAST_DUE invoice and the payment is recovered. The same agent then retries again and again — each attempt looks legitimate — but only the first should ever hit the card. You put the agent on recovery; the boundary holds the limit.",
		WhyConfident: "After recovery the invoice advances to RECOVERED, and RETRY_PAYMENT is allowed only from PAST_DUE — so every subsequent charge attempt is declined (state_not_allowed). The agent owns recovery; the customer's card is charged once.",
	},
	{
		Slug:         "instant-payouts",
		Recipe:       "instant-payout-enablement-guard",
		Flow:         "Ship instant seller payouts",
		Tagline:      "Drop the manual review gate. The moment escrow clears, the payout ships — one time, with a verifiable record.",
		Framework:    "Agno",
		Model:        "Bedrock · Nova Pro",
		Entity:       "Escrow",
		Action:       "DISBURSE_PAYOUT",
		OpenState:    "CLEARED",
		DoneState:    "DISBURSED",
		Line:         "DISBURSE_PAYOUT sends the seller's money — real money, instantly.",
		Before:       "Payouts waited for a human review gate to prevent double-disbursement — the gate that slowed every legitimate payout down.",
		After:        "The agent disburses the moment escrow clears. The boundary holds the one-payout-per-escrow invariant, so you can drop the gate.",
		HowItShips:   "A payout agent disburses to the seller the moment escrow transitions to CLEARED — no batch window, no manual sign-off. Without a boundary, the same event fires the payout repeatedly. The state machine is what makes the review gate unnecessary.",
		WhyConfident: "Once disbursed the escrow advances to DISBURSED, and DISBURSE_PAYOUT is allowed only from CLEARED — so every duplicate attempt is declined (state_not_allowed). The agent owns disbursement; each escrow pays out exactly once.",
	},
}

// All returns the ordered registry (for the index).
func All() []Spot {
	out := make([]Spot, len(spots))
	copy(out, spots)
	return out
}

// Lookup returns a spot by slug and whether it exists.
func Lookup(slug string) (Spot, bool) {
	for _, s := range spots {
		if s.Slug == slug {
			return s, true
		}
	}
	return Spot{}, false
}
