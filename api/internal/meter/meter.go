// Package meter keeps the v1 offer's two meters: tafs
// tasks, the calls a taf made, and KIFF actions, the calls the account's
// own agents made.
//
// A call counts only when it got something done: KIFF allowed it, the
// gateway sent it, the tool reported success, the tool isn't read-only,
// and the agent isn't Setup (its Coach mode included). It counts once, by
// its call key, so a retry of the same call counts once. Everything else is
// free, and the meter says why: refused, held then refused, halted,
// invalid, failed (a tool error, a lost answer, or never sent), a read, or
// Setup's. A trial decides and sends
// nothing, so it never reaches the gateway's record: its calls are free
// and read from the decisions.
//
// The gateway's call record (gateway.invocations) isn't kept for billing:
// a test account's rows are pruned after 48 hours, and results after 24.
// So each call is classified once, when it has finished, into
// cloud_meta.billable_calls, which the usage read, the limits and the
// Stripe reporter read.
//
// Calls decided by KIFF's API without the gateway (a kiff-guard runtime)
// have no record of being done, so they never count.
package meter

import (
	"strings"
	"time"

	"github.com/kiff/cloud/api/internal/tooloutcome"
)

// Unit is which meter a call is on.
type Unit string

const (
	// UnitTask is a call a taf made (an agent the tafs app made).
	UnitTask Unit = "task"
	// UnitAction is a call one of the account's own agents made.
	UnitAction Unit = "action"
)

// Why a call is free, as the usage read names it.
const (
	FreeRefused     = "refused"
	FreeHeldRefused = "held_refused"
	FreeHalted      = "halted"
	FreeFailed      = "failed"
	FreeInvalid     = "invalid"
	// FreeRetry is a retry of a call already made. A retry reaches the
	// gateway under the call's own key and is answered from it, so it
	// never makes a second record: it is never counted, and the meter has
	// no record of it to show, so it reads 0.
	FreeRetry = "retry"
	FreeRead  = "read"
	FreeTrial = "trial"
	// FreeSetup is a call Setup's key made, reads and writes alike: setting
	// tafs up and running them for the owner (Coach) isn't billed
	// (kiff/tafs#206). A task Setup hands a taf is the taf's own run, under
	// its own key, and counts as the taf's.
	FreeSetup = "setup"
)

// FreeKinds are every free case, in the usage read's order. A case with
// nothing is 0, never missing.
var FreeKinds = []string{FreeRefused, FreeHeldRefused, FreeHalted, FreeFailed, FreeInvalid, FreeRetry, FreeRead, FreeTrial, FreeSetup}

// Call is one gateway call, as the meter reads it.
type Call struct {
	Tenant, Key, Agent, Tool string
	// ToolTitle is the tool's title for people, when the gateway has one.
	ToolTitle string
	// Taf: the agent is a taf's (agents.KindTaf).
	Taf bool
	// ReadOnly: the connected tool is marked read-only.
	ReadOnly bool
	// SetupReader: the key that made it carries setup_reader, Setup's.
	SetupReader bool
	// ExceptionID: it was held for someone to answer.
	ExceptionID string
	// Reasons are why it was refused.
	Reasons []string
	// Record is how it ended, as the gateway recorded it.
	Record tooloutcome.Record
	// AskedBy is the person whose run made the call, as KIFF checked
	// their run grant when it decided it; "" for a call no person's run
	// made (a schedule, a door, an agent of the account's own).
	AskedBy string
}

// Entry is one classified call.
type Entry struct {
	Tenant, Key, Agent, Tool string
	// ToolTitle is the tool's title for people as it was when the call
	// was recorded; "" when it had none.
	ToolTitle string
	Unit      Unit
	// Counted: it got something done, and is on its meter.
	Counted bool
	// Free is why it isn't counted, one of FreeKinds; empty when Counted.
	Free string
	// At is when it ended.
	At time.Time
	// AskedBy is the call's person, as Call.AskedBy says.
	AskedBy string
}

// Classify is how c counts, once it has finished. False while it hasn't
// (held, being decided or being sent): it is classified later.
func Classify(c Call) (Entry, bool) {
	e := Entry{Tenant: c.Tenant, Key: c.Key, Agent: c.Agent, Tool: c.Tool, ToolTitle: c.ToolTitle, Unit: UnitAction, At: c.Record.At, AskedBy: c.AskedBy}
	if c.Taf {
		e.Unit = UnitTask
	}
	switch c.Record.State {
	case "refused":
		e.Free = refusal(c)
		return e, true
	case "forwarded", "unknown", "failed":
	default:
		return Entry{}, false
	}
	o, ok := tooloutcome.FromRecord(c.Record)
	if !ok || o.Status == tooloutcome.Sending {
		return Entry{}, false
	}
	switch {
	case c.SetupReader:
		e.Free = FreeSetup
	case c.ReadOnly:
		e.Free = FreeRead
	case o.Status == tooloutcome.Done:
		e.Counted = true
	default:
		// A tool error, a lost answer, never sent, or how it ended
		// wasn't recorded: nothing is known to be done, so it's free.
		e.Free = FreeFailed
	}
	return e, true
}

// refusal is why a refused call was refused, as the meter names it.
func refusal(c Call) string {
	for _, r := range c.Reasons {
		if r == "halted" {
			return FreeHalted
		}
	}
	if c.ExceptionID != "" {
		return FreeHeldRefused
	}
	for _, r := range c.Reasons {
		if strings.HasPrefix(r, "invalid") {
			return FreeInvalid
		}
	}
	return FreeRefused
}
