package meter

import "time"

// Credits are what a run of a taf uses: its AI cost in euro cents, times
// CreditMultiplier, rounded up once per run. They're worked out here, once,
// as a run's row is kept, so every screen and invoice reads the same number.
const (
	// CreditMultiplier is how many credits a euro cent of a run's AI uses.
	CreditMultiplier = 4
	// usdPerEURHundredths is the fixed rate a run's AI cost, in US dollars,
	// is read in euros at: 1.08, as tafs cloud reads the AI bound.
	usdPerEURHundredths = 108
	// SetupAllowanceMicroUSD is Setup's (Coach's) fair use: the AI its runs
	// may cost in an account's period, about 10 euros at that rate, before
	// they use credits too.
	SetupAllowanceMicroUSD = 10_800_000
)

// Credits is what a run whose AI cost costMicroUSD (millionths of a US
// dollar) uses, rounded up: a run that cost anything uses at least one.
func Credits(costMicroUSD int64) int64 {
	if costMicroUSD <= 0 {
		return 0
	}
	// cost / 1e6 dollars / 1.08 dollars per euro * 100 cents * multiplier.
	num := costMicroUSD * 100 * 100 * CreditMultiplier
	den := int64(1_000_000 * usdPerEURHundredths)
	return (num + den - 1) / den
}

// RunReport is one ended run, as its runtime reported it with its agent's
// key. Agent and Setup come from the key; the rest is the runtime's.
type RunReport struct {
	Tenant string
	// Run is the run's id in its runtime.
	Run string
	// Agent is KIFF's agent, the reporting key's; RuntimeAgent the run's
	// agent in its runtime (a taf's id in tafs).
	Agent, RuntimeAgent string
	// Setup: the key is Setup's (Coach), whose runs are free within its
	// fair use.
	Setup bool
	// Trigger is what started it (owner, schedule, event, trial, eval),
	// Source what within it, RequesterKind who asked (member, door, manager,
	// agent), AskedBy the person whose run it was: a member's or manager's
	// subject, else "".
	Trigger, Source, RequesterKind, AskedBy string
	Started, Ended                          time.Time
	// CostMicroUSD is what its model turns cost, in millionths of a US
	// dollar.
	CostMicroUSD int64
	Turns        int
	// Status is how it ended (COMPLETED, ERROR, CANCELLED), Stop why a
	// completed run stopped; Failed that it failed on the runtime's side.
	Status, Stop string
	Failed       bool
	// Refused and Declined count its calls KIFF refused outright, and those
	// it held that the owner refused. They don't change what it used.
	Refused, Declined int
	// KeptUntil is when the runtime may delete the run; nil, kept.
	KeptUntil *time.Time
	// NoPlan: it ended while the account had no plan. Setup's runs then
	// are free: KIFF pays for them, within NoPlanSetupMicroUSD.
	NoPlan bool
}

// RunEntry is a kept run: its report, how it counts, and its credits.
type RunEntry struct {
	RunReport
	// Counted: it uses credits. Free is why not, one of FreeSetup,
	// FreeTrial or FreeFailed; "" when counted.
	Counted bool
	Free    string
	// Credits are what it uses; 0 when free.
	Credits int64
}

// ClassifyRun is how r counts. setupSpent is what Setup's runs already
// cost in the period, for a run of Setup's. Free, in order: a trial of a
// new version, which sends nothing; a run that failed on the runtime's
// side; a run of Setup's on an account with no plan, or while its
// period's runs, this one included, stay within its fair use. Everything
// else counts, a run with calls KIFF refused or the owner declined
// included: its AI was used.
func ClassifyRun(r RunReport, setupSpent int64) RunEntry {
	e := RunEntry{RunReport: r}
	switch {
	case r.Trigger == "trial":
		e.Free = FreeTrial
	case r.Failed:
		e.Free = FreeFailed
	case r.Setup && (r.NoPlan || setupSpent+r.CostMicroUSD <= SetupAllowanceMicroUSD):
		e.Free = FreeSetup
	default:
		e.Counted, e.Credits = true, Credits(r.CostMicroUSD)
	}
	return e
}

// RunFreeKinds are the reasons a run is free, in the credits read's order.
var RunFreeKinds = []string{FreeSetup, FreeTrial, FreeFailed}

// AgentRuns are the counted runs of one agent in a period.
type AgentRuns struct {
	Agent, RuntimeAgent string
	Credits             int64
	Runs                int
}

// RunTotals are an account's runs in a period: what the counted ones used,
// in all and per agent (Setup's apart), and how many were free, and why.
type RunTotals struct {
	Credits int64
	Runs    int
	// ByAgent are the tafs' counted runs, most credits first.
	ByAgent []AgentRuns
	// Setup are Setup's counted runs, past its fair use.
	Setup AgentRuns
	// Free counts the free runs by RunFreeKinds, each present.
	Free map[string]int
	// Over is Solo's free overshoot: credits the period's runs used past
	// its balance, which nothing pays for.
	Over int64
}

// UnsettledRun is a counted run whose credits aren't yet taken from the
// balance.
type UnsettledRun struct {
	Run     string
	Ended   time.Time
	Credits int64
}

// RunPage is where a page of runs continues: after the run that ended at
// At with id Run, newest first.
type RunPage struct {
	At  time.Time
	Run string
}

// RunDay is one account's runs of one agent that ended on one UTC day.
type RunDay struct {
	Tenant              string
	Agent, RuntimeAgent string
	Setup               bool
	// Day is the UTC day's start.
	Day time.Time
	// Runs are all of them, Counted those that use credits; Credits what
	// they used, Over Solo's free overshoot among them, and CostMicroUSD
	// what their AI cost.
	Runs, Counted int
	Credits, Over int64
	CostMicroUSD  int64
	// Last is when the latest of them ended.
	Last time.Time
}
