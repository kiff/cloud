// Package reviewload measures the work a team is doing by hand that
// they would not have to.
//
// Every other read in this system answers what happened. This one
// answers what a person had to look at, which is the question the
// engagement is sold on: give us a week and we will tell you what share
// of the approvals your team does today can stop happening.
//
// A number offered as a reason to buy has to be able to say no. The
// arithmetic here is deliberately conservative in three places — the
// numerator, the denominator, and the refusal to answer at all on a
// small sample — because a ratio that flatters us on a thin week is
// worth less than nothing: it is the one number a customer will check
// against their own records.
package reviewload

import (
	"github.com/kiff/cloud/api/internal/proposals"
)

// MinimumSample is the number of decisions below which no ratio is
// reported.
//
// Thirty, so one unusual action cannot move the figure by a quarter.
// It doubles as a qualification signal and that is the more useful
// half: a team whose agent proposes fewer than thirty consequential
// actions in the window is not a team with an approval queue worth
// dismantling, and the honest thing to tell them is that this will not
// pay for itself yet.
const MinimumSample = 30

// Load is one window of decisions, grouped by what each one cost a
// person.
type Load struct {
	// WouldRun is the count that needed nobody: the contract was
	// satisfied, the authority was there, the total was under the
	// ceiling. This is the only numerator.
	WouldRun int `json:"would_run"`

	// WouldReachAPerson is the count that routed to a human. This is
	// the review that remains, and the argument is that it is the
	// review that was always worth doing.
	WouldReachAPerson int `json:"would_reach_a_person"`

	// WouldBeRefused is the count stopped before anyone spent time on
	// it. Deliberately NOT in the numerator. A refusal does remove a
	// person's attention, so counting it would raise the headline
	// figure — and it also means the work did not happen, which is a
	// different thing to sell and sometimes a cost. Conservative is
	// what makes the number survive being checked.
	WouldBeRefused int `json:"would_be_refused"`

	// Metered is our own billing limit turning someone away. It is not
	// a fact about their operation and is excluded from every other
	// figure here, reported only so the totals reconcile against the
	// raw counts.
	Metered int `json:"metered"`

	// Enforcing says whether these decisions were acted on or only
	// recorded. In observe mode every figure is a counterfactual and
	// the surface has to say "would have"; in enforce they happened.
	// Carried rather than inferred, because the tense of every
	// sentence around the number depends on it.
	Enforcing bool `json:"enforcing"`
}

// Total is the decisions that say something about their operation.
func (l Load) Total() int {
	return l.WouldRun + l.WouldReachAPerson + l.WouldBeRefused
}

// Sufficient reports whether the window holds enough decisions for the
// ratio to mean anything.
func (l Load) Sufficient() bool { return l.Total() >= MinimumSample }

// Percent is the share of consequential actions that needed nobody,
// rounded to a whole number.
//
// Returns 0 on an insufficient sample, and callers must check
// Sufficient first: zero here means "not answerable", which is a
// different claim from "nothing could be automated", and rendering one
// as the other is the failure this whole package is shaped to avoid.
func (l Load) Percent() int {
	if !l.Sufficient() {
		return 0
	}
	return int(float64(l.WouldRun)*100/float64(l.Total()) + 0.5)
}

// FromCounts groups the per-outcome counts the decisions reader
// returns.
//
// Unknown outcomes are ignored rather than bucketed. A future outcome
// this package has not been taught about would otherwise land in
// whichever group it was defaulted to and quietly move the number.
func FromCounts(counts map[string]int, enforcing bool) Load {
	l := Load{Enforcing: enforcing}
	for outcome, n := range counts {
		switch outcome {
		case "allowed":
			l.WouldRun += n
		case "approval_required":
			l.WouldReachAPerson += n
		case "blocked", "invalid":
			l.WouldBeRefused += n
		case "limit_exceeded":
			l.Metered += n
		}
	}
	return l
}

// Query is the window this is measured over.
type Query = proposals.DecisionQuery
