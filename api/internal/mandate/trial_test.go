package mandate_test

import (
	"context"
	"errors"
	"testing"

	"github.com/kiff/cloud/api/internal/mandate"
)

func (r *rig) trial(run string, amount int64) mandate.Decision {
	r.t.Helper()
	d, err := r.a.Trial(context.Background(), tenantID, run, refund(amount))
	if err != nil {
		r.t.Fatalf("trial: %v", err)
	}
	return d
}

// Invariant: a trial of an agent's next version is decided against the
// Cards as a real queue would be, its own earlier allowed writes counting
// toward the limits, and draws nothing real, holds nothing and asks no
// owner.
// Category: authority
// Attack: run a trial's refunds over and over, inside and outside the Card, to use up its total or fill the owner's queue
// Refused: each trial write gets the decision the same queue of real calls would get, and the Card's total and the owner's queue are unchanged
func TestTrialDecidesAndRecordsNothing(t *testing.T) {
	r := newRig(t, homeCard("card-a", 50000, 100000))
	// 1,000 a day, 500 a call: three refunds of 400 → allowed, allowed, held.
	for i, want := range []mandate.Outcome{mandate.OutcomeWithinLimit, mandate.OutcomeWithinLimit, mandate.OutcomeOwnerRequired} {
		if d := r.trial("run-1", 40000); d.Outcome != want || d.ExceptionID != "" {
			t.Fatalf("refund %d in the trial = %+v, want %s", i+1, d, want)
		}
	}
	// A held write draws nothing in the queue: one of 200 still fits.
	if d := r.trial("run-1", 20000); d.Outcome != mandate.OutcomeWithinLimit {
		t.Fatalf("200 after the hold = %+v", d)
	}
	if r.used("card-a") != 0 {
		t.Fatal("a trial drew on the card")
	}
	if es, _ := r.store.Exceptions(context.Background(), tenantID, ""); len(es) != 0 {
		t.Fatalf("a trial held an exception: %+v", es)
	}
	// Another trial run starts from the Card's real usage alone.
	if d := r.trial("run-2", 40000); d.Outcome != mandate.OutcomeWithinLimit {
		t.Fatalf("a second trial = %+v", d)
	}
	// What real calls drew counts too: after two real refunds of 400,
	// 200 is left, so a fresh trial's 400 would be held.
	r.decide("p1", 40000)
	r.decide("p2", 40000)
	if d := r.trial("run-3", 40000); d.Outcome != mandate.OutcomeOwnerRequired {
		t.Fatalf("trial past what is left = %+v", d)
	}
	if r.used("card-a") != 80000 {
		t.Fatalf("used = %d, want only the two real draws", r.used("card-a"))
	}
}

// A Card set to refuse refuses a trial too, and refuse takes precedence
// over a Card that comes home, as for a real call. A trial call that
// names no run is not decided.
func TestTrialRefusesWhereACallWouldBe(t *testing.T) {
	refuse := homeCard("card-refuse", 50000, 500000)
	refuse.OnExceed = mandate.OnExceedRefuse
	r := newRig(t, homeCard("card-home", 50000, 500000), refuse)
	if d := r.trial("run-1", 150000); d.Outcome != mandate.OutcomePerActionExceeded || d.MandateID != "card-refuse" {
		t.Fatalf("trial = %+v", d)
	}
	if _, err := r.a.Trial(context.Background(), tenantID, "", refund(100)); !errors.Is(err, mandate.ErrNoTrialRun) {
		t.Fatalf("a trial without its run: %v", err)
	}
	r = newRig(t)
	if d := r.trial("run-1", 100); d.Outcome != mandate.OutcomeNotCovered {
		t.Fatalf("trial with no cards = %+v", d)
	}
}

// A trial against a restore leaves out the Cards the restore retires
// (kiff/tafs#107): what it allowed is no longer there to cover the call.
func TestTrialWithoutARetiredCard(t *testing.T) {
	r := newRig(t, homeCard("card-a", 50000, 100000))
	ctx := context.Background()
	if d, err := r.a.TrialWith(ctx, tenantID, "run-r", refund(40000), nil, nil); err != nil || d.Outcome != mandate.OutcomeWithinLimit {
		t.Fatalf("with card-a: %+v %v", d, err)
	}
	d, err := r.a.TrialWith(ctx, tenantID, "run-r2", refund(40000), nil, []string{"card-a"})
	if err != nil || d.Outcome != mandate.OutcomeNotCovered {
		t.Fatalf("with card-a retired: %+v %v, want not covered", d, err)
	}
}
