package gateway_test

import (
	"testing"

	"github.com/kiff/cloud/gateway/internal/gateway"
)

// Invariant: a person's run grant goes with each call KIFF decides, and a
// grant KIFF refuses sends nothing. The gateway never decides on the grant
// itself: only KIFF can say who it is for.
// Category: authority
// Attack: a run whose person may no longer use the agent, or with a forged grant, issues a refund
// Refused: KIFF refuses the call on the grant and the tool receives nothing
func TestARunGrantGoesWithEachDecision(t *testing.T) {
	r := newRig(t)
	r.runGrant = "krg_ana"
	got := r.call(t, "agent-key", amount(500, "op-grant-1"))
	if got.isError || r.tool.count() != 1 {
		t.Fatalf("ana's refund: %+v, tool calls %d", got, r.tool.count())
	}
	r.runGrant = "krg_bad"
	got = r.call(t, "agent-key", amount(500, "op-grant-2"))
	if !got.isError || r.tool.count() != 1 {
		t.Fatalf("a refused grant: %+v, tool calls %d", got, r.tool.count())
	}
	r.runGrant = ""
	got = r.call(t, "agent-key", amount(500, "op-grant-3"))
	if got.isError || r.tool.count() != 2 {
		t.Fatalf("no grant: %+v, tool calls %d", got, r.tool.count())
	}
	r.api.mu.Lock()
	defer r.api.mu.Unlock()
	want := map[string]string{"krg_ana": "", "krg_bad": "", "": ""}
	for _, g := range r.api.runGrants {
		delete(want, g)
	}
	if len(want) != 0 || len(r.api.runGrants) != 3 {
		t.Fatalf("decided with grants %v", r.api.runGrants)
	}
}

// Invariant: the run a call's KIFF-Run names goes with each call KIFF
// decides, as sent, for KIFF to sign into its permit; a call without the
// header names no run.
// Category: authority
// Attack: a tool can't tell which run asked, so a step from another run of the same agent is taken as this one's
// Refused: the permit names the run that made the call, and none when it named none
func TestTheRunThatMadeACallGoesWithItsDecision(t *testing.T) {
	r := newRig(t)
	r.headers = map[string]string{gateway.AgentRunHeader: " run_ana_1 "}
	if got := r.call(t, "agent-key", amount(500, "op-run-1")); got.isError {
		t.Fatalf("with a run: %+v", got)
	}
	r.headers = nil
	if got := r.call(t, "agent-key", amount(500, "op-run-2")); got.isError {
		t.Fatalf("without a run: %+v", got)
	}
	r.api.mu.Lock()
	defer r.api.mu.Unlock()
	if len(r.api.agentRuns) != 2 {
		t.Fatalf("decided %d calls: %v", len(r.api.agentRuns), r.api.agentRuns)
	}
	var withRun, without int
	for _, run := range r.api.agentRuns {
		switch run {
		case "run_ana_1":
			withRun++
		case "":
			without++
		}
	}
	if withRun != 1 || without != 1 {
		t.Fatalf("decided with runs %v", r.api.agentRuns)
	}
}
