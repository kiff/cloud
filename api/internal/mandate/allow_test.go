package mandate_test

import (
	"context"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/mandate"
)

// held is the exception the rig's last decision held.
func (r *rig) held(proposalID string) mandate.Exception {
	r.t.Helper()
	es, err := r.store.Exceptions(context.Background(), tenantID, "")
	if err != nil {
		r.t.Fatal(err)
	}
	for _, e := range es {
		if e.ProposalID == proposalID {
			return e
		}
	}
	r.t.Fatalf("%s wasn't held", proposalID)
	return mandate.Exception{}
}

func (r *rig) cards() []mandate.Mandate {
	r.t.Helper()
	ms, err := r.store.ForSubject(context.Background(), tenantID, "agent://refund-agent")
	if err != nil {
		r.t.Fatal(err)
	}
	return ms
}

// "Allow this from now on": the narrowest lasting change to the hold's Card,
// from what the hold recorded, and never past the held call.
func TestAllowIsTheNarrowestChangeForTheHeldCall(t *testing.T) {
	// A per-call limit: €150 each, a call of €320.
	r := newRig(t, homeCard("c1", 15000, 1_000_000))
	if d := r.decide("p1", 32000); d.Outcome != mandate.OutcomeOwnerRequired {
		t.Fatalf("not held: %+v", d)
	}
	e := r.held("p1")
	cards, changes, roundAfter, why := mandate.Allow(r.cards(), e, false, r.clock)
	if why != "" || len(cards) != 1 || len(changes) != 1 {
		t.Fatalf("per call: %q %+v %+v", why, cards, changes)
	}
	c := changes[0]
	if c.Kind != mandate.AllowPerAction || *c.Before != 15000 || *c.After != 32000 || c.Rounded || roundAfter != 35000 {
		t.Fatalf("per call, exact: %+v round %d", c, roundAfter)
	}
	if got := cards[0].Grants[0].PerAction[0].Max; got != 32000 || cards[0].Aggregates[0].Limit != 1_000_000 {
		t.Fatalf("the Card after: per call %d, total %d", got, cards[0].Aggregates[0].Limit)
	}
	if r.cards()[0].Grants[0].PerAction[0].Max != 15000 {
		t.Fatal("Allow changed the Card in force")
	}
	// Rounded: one step up, and it says so.
	_, changes, roundAfter, _ = mandate.Allow(r.cards(), e, true, r.clock)
	if c := changes[0]; *c.After != 35000 || !c.Rounded || roundAfter != 0 {
		t.Fatalf("per call, rounded: %+v round %d", c, roundAfter)
	}

	// A total: €600 a day, €500 used, a call of €200 takes it to €700, as the hold recorded it.
	r = newRig(t, homeCard("c2", 1_000_000, 60000))
	r.decide("p0", 50000)
	r.decide("p2", 20000)
	e = r.held("p2")
	r.clock = r.clock.Add(time.Hour) // later draws don't change what the call needed
	r.decide("p3", 1000)
	_, changes, roundAfter, why = mandate.Allow(r.cards(), e, false, r.clock)
	if why != "" || len(changes) != 1 || changes[0].Kind != mandate.AllowTotal || *changes[0].Before != 60000 || *changes[0].After != 70000 || changes[0].Window == "" || roundAfter != 0 {
		t.Fatalf("total: %q %+v round %d", why, changes, roundAfter)
	}

	// A Card the agent shares, or one no longer in force: no change.
	r = newRig(t, homeCard("c3", 15000, 1_000_000))
	r.decide("p4", 32000)
	e = r.held("p4")
	shared := r.cards()
	shared[0].Holders = []string{"agent://refund-agent", "agent://other"}
	if _, _, _, why := mandate.Allow(shared, e, false, r.clock); why != mandate.AllowNoneNoChange {
		t.Fatalf("shared Card: %q", why)
	}
	if _, _, _, why := mandate.Allow(r.cards(), e, false, base.Add(48*time.Hour)); why != mandate.AllowNoneNoChange {
		t.Fatalf("lapsed Card: %q", why)
	}
	// A unit hold: Coach's to fix.
	unit := e
	unit.Kind, unit.Breaches = mandate.KindCardUnit, []mandate.Breach{{MandateID: "c3", Outcome: mandate.OutcomeUnitMismatch, CardUnit: "USD", Unit: "EUR"}}
	if unit.KindOf() != mandate.KindCardUnit {
		t.Fatalf("not a unit hold: %q", unit.KindOf())
	}
	if _, _, _, why := mandate.Allow(r.cards(), unit, false, r.clock); why != mandate.AllowNoneUnit {
		t.Fatalf("unit hold: %q", why)
	}
	// A breach whose limit isn't the one the hold saw (the Card changed since): no change.
	moved := e
	moved.Breaches = append([]mandate.Breach(nil), e.Breaches...)
	moved.Breaches[0].Limit = 12345
	if _, _, _, why := mandate.Allow(r.cards(), moved, false, r.clock); why != mandate.AllowNoneNoChange {
		t.Fatalf("a limit that moved: %q", why)
	}
}

func TestRoundUpIsOneStepAndNeverBelow(t *testing.T) {
	for _, c := range []struct {
		in   int64
		unit string
		want int64
	}{
		{32000, "EUR", 35000}, {35000, "EUR", 35000}, {124000, "EUR", 130000}, {1_234_500, "EUR", 1_300_000},
		{320, "JPY", 350}, {1, "EUR", 5000}, {32000, "", 35000},
	} {
		if got := mandate.RoundUp(c.in, c.unit); got != c.want {
			t.Errorf("RoundUp(%d, %q) = %d, want %d", c.in, c.unit, got, c.want)
		}
	}
}

// The replay decides the held calls again against the draft's Cards, in
// order, each on the totals as they stood then, and never counts a held
// call's draw twice.
func TestReplayDecidesHeldCallsAsTheyStood(t *testing.T) {
	// €150 each, €600 a day. Three calls held as too large: €200, €320, €500.
	r := newRig(t, homeCard("c1", 15000, 60000))
	for i, a := range []int64{20000, 32000, 50000} {
		r.clock = base.Add(time.Duration(i) * time.Minute)
		if d := r.decide([]string{"h1", "h2", "h3"}[i], a); d.Outcome != mandate.OutcomeOwnerRequired {
			t.Fatalf("not held: %+v", d)
		}
	}
	var held []mandate.Exception
	for _, p := range []string{"h1", "h2", "h3"} {
		e := r.held(p)
		for i := range e.Breaches {
			e.Breaches[i].Unit = "EUR" // the handler sets each breach's unit from the Card's domain
		}
		held = append(held, e)
	}
	draft := r.cards()
	draft[0].Grants = append([]mandate.Grant(nil), draft[0].Grants...)
	draft[0].Grants[0].PerAction = []mandate.PerActionLimit{{Quantity: amountQ, Max: 50000}}
	draws, err := r.store.Draws(context.Background(), tenantID, "c1", base.Add(-24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	got, err := mandate.Replay(draft, held, draws)
	if err != nil {
		t.Fatal(err)
	}
	// €200 and €320 fit (€520 of €600); €500 would pass the day's total, so it's still held.
	if got.Calls != 2 || len(got.ByQuantity) != 1 || got.ByQuantity[0].Total != 52000 || got.ByQuantity[0].Unit != "EUR" {
		t.Fatalf("replay: %+v", got)
	}
	// The Cards in force let none go.
	if got, _ := mandate.Replay(r.cards(), held, draws); got.Calls != 0 {
		t.Fatalf("replay on the Cards in force: %+v", got)
	}
}
