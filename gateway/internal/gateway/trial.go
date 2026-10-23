package gateway

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"

	"github.com/kiff/cloud/gateway/internal/kiffapi"
	"github.com/kiff/cloud/gateway/internal/mcp"
)

// Trials (kiff/tafs#107). A trial of an agent's next version runs with
// the KIFF-Trial header on its requests, and the gateway declares that it
// understands it (TrialCapability), so a client never sends a trial to a
// gateway that would send its calls for real.
//
// A trial's call is decided by apps/api against the agent's Cards (POST
// /v1/gateway/trial) and never sent: it creates no invocation, asks for no
// proposal, and reaches no tool. Its result says what KIFF would do, as a
// plain result the agent reads and goes on from. Reads are no exception: a
// tool's read-only flag may be a model's claim that no vendor confirmed
// (kiff/agents#139), and a "read" that writes must not run in a trial.

// TrialHeader carries a trial's draft: "live" for the agent's Cards as
// they stand, or the id of the draft whose Cards decide.
const TrialHeader = "KIFF-Trial"

// TrialRunHeader names a trial's run: KIFF counts its earlier allowed
// writes toward the Cards' limits, as a live queue would.
const TrialRunHeader = "KIFF-Trial-Run"

// TrialCapability is the experimental capability that says this gateway
// decides trials without sending them.
const TrialCapability = "dev.kiff/trial"

// TrialSendsCapability says this gateway sends the calls of a trial its
// owner started (kiff/tafs#173): with KIFF-Trial naming a draft being
// tried, a call goes the normal path, decided on the trial's own Cards,
// and its result's _meta carries trial_draft. A client must not start such
// a trial through a gateway without it, which would only decide its calls.
// KIFF-Trial-Run is ignored on those calls: the trial Cards keep their own
// counters.
const TrialSendsCapability = "dev.kiff/trial-sends"

// trialSends reports whether the request's trial is one its owner started,
// whose calls are sent: KIFF-Trial names a draft KIFF says is being tried.
// "live", or a draft only waiting, is a trial that sends nothing.
func (a *agent) trialSends(ctx context.Context) (bool, error) {
	if !strings.HasPrefix(a.trial, "drf_") {
		return false, nil
	}
	return a.g.API.TrialOpen(ctx, a.key, a.trial)
}

// TrialDecision is KIFF's decision on a trial's write, in the call's
// result _meta.
type TrialDecision struct {
	// Decision is "allowed", "held" or "refused".
	Decision string `json:"decision"`
	Reason   string `json:"reason,omitempty"`
	// Title is the tool's name for people.
	Title string `json:"title,omitempty"`
}

// trialResult decides a trial's write and sends nothing. When KIFF can't
// be asked, the write counts as refused; it is never sent either way.
func (a *agent) trialResult(ctx context.Context, t Tool, params map[string]any) json.RawMessage {
	var d kiffapi.TrialDecision
	var err error
	if a.trialRun == "" {
		// Without its run its earlier writes can't be counted: each would
		// look like the first, overstating what the version can do.
		d = kiffapi.TrialDecision{Decision: "refused", Message: "this trial names no run (" + TrialRunHeader + "), so its earlier writes can't be counted"}
	} else {
		d, err = a.g.API.Trial(ctx, a.key, kiffapi.TrialCall{ActionName: t.Name, Parameters: params, Draft: a.trial, Run: a.trialRun})
	}
	td := TrialDecision{Decision: d.Decision, Reason: d.Message, Title: t.Title}
	if err != nil {
		td = TrialDecision{Decision: "refused", Reason: "KIFF couldn't decide this call", Title: t.Title}
		if a.g.Logger != nil {
			a.g.Logger.WarnContext(ctx, "gateway: trial call not decided", slog.String("tool", t.Name), slog.String("err", err.Error()))
		}
	}
	var text string
	switch td.Decision {
	case "allowed":
		text = "Trial: KIFF would allow this call."
	case "held":
		text = "Trial: KIFF would hold this call for its owner's approval: " + td.Reason + "."
	default:
		td.Decision = "refused"
		text = "Trial: KIFF would refuse this call: " + strings.TrimSuffix(td.Reason, ".") + "."
	}
	if t.ReadOnly {
		text += " In a trial nothing is sent, reads included, so there is no data: go on as if it had answered."
	} else {
		text += " Nothing was sent; go on as if it were done."
	}
	return withCall(mcp.TextResult(text, false),
		Call{State: "trial", Sent: "no", Retry: "none", Trial: &td})
}
