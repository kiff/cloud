// Package usecases is the data registry for the homepage "use cases"
// section — the jobs a team could hand to an agent once KIFF is in
// front of the risky action. It replaced the separate /solutions
// pages (folded into the homepage as one inspiring section, not a
// taxonomy): each card is a domain plus a few action keywords, meant
// to spark "we could launch an agent for that" — not to explain the
// product again.
//
// Per the web binary is hermetic Go + templ + native CSS — data +
// templ, no JS, no build step.
package usecases

// UseCase is one domain card: a short title and the consequential
// actions a team could let an agent take there, once KIFF checks each
// one before it runs.
type UseCase struct {
	Title   string   // domain label, e.g. "Money & payments"
	Actions []string // inspiring action keywords rendered as chips
}

// useCases is the ordered registry — the card order on the homepage.
//
// Two, not six. The earlier list ran across money, procurement,
// security, infrastructure, insurance and customer operations, which
// explained the platform and sold nothing: a reader deciding whether
// this is for them reads six markets as "general-purpose", and a
// general-purpose answer is one they have to do the work to apply.
//
// These two are where an agent is already taking the action today and
// the unit is countable. Customer operations first because the money is
// small enough that a team will let a boundary near it — refunds and
// credits, not payouts and wires, which is where the friction lives.
// Infrastructure second because the unit is a count rather than a
// currency, which shows the same primitive without the word "spend".
//
// Insurance, procurement and payments are real and are not here. Add one
// back when a customer in it has paid, not before.
var useCases = []UseCase{
	{
		Title:   "Customer operations",
		Actions: []string{"Issue refunds", "Apply credits", "Cancel orders", "Change plans"},
	},
	{
		Title:   "Cloud & infrastructure",
		Actions: []string{"Restart services", "Scale resources", "Roll back a deploy", "Rotate credentials"},
	},
}

// All returns the ordered registry (homepage card order).
func All() []UseCase { return useCases }
