package gateway_test

import "testing"

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
