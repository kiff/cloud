package mandate_test

import (
	"context"
	"testing"

	"github.com/kiff/cloud/api/internal/mandate"
)

const ownerAskWhy = "this tool moves money on a connection whose key KIFF holds, so every call waits for approval"

func (r *rig) decideAsking(proposalID string, amount int64) mandate.Decision {
	r.t.Helper()
	req := refund(amount)
	req.OwnerAsk, req.OwnerAskWhy = "sealed_money", ownerAskWhy
	d, err := r.a.Authorize(context.Background(), tenantID, proposalID, req)
	if err != nil {
		r.t.Fatalf("authorize %s: %v", proposalID, err)
	}
	return d
}

// Invariant: a request that asks its owner (the gateway's kiff_owner_ask, sent for a money tool on a connection where KIFF holds the key) is held for the owner on every call, however much room the Card has; approving it approves that one call, drawn on the Card's real totals; a Card set to refuse refuses it.
// Category: authority
// Attack: send a small refund through a sealed money tool, well inside the Card, hoping the Card's room lets it through without a person; or reuse one approval for the next call
// Refused: each call is held as owner_required with the gateway's reason; an approved one draws once on the Card and the next is held again; a Card set to refuse refuses it and holds nothing
func TestAnOwnerAskHoldsEveryCallWhateverTheCardsRoom(t *testing.T) {
	r := newRig(t, homeCard("card-a", 50000, 500000))
	if d := r.decide("p0", 1000); d.Outcome != mandate.OutcomeWithinLimit {
		t.Fatalf("without the ask, the Card allows it: %+v", d)
	}
	d := r.decideAsking("p1", 1000)
	if d.Outcome != mandate.OutcomeOwnerRequired || d.ExceptionID == "" {
		t.Fatalf("an owner ask is held for the owner: %+v", d)
	}
	e, _ := r.store.Exception(context.Background(), tenantID, d.ExceptionID)
	if len(e.Breaches) != 1 || e.Breaches[0].Reason != ownerAskWhy || e.Breaches[0].OwnerAsk != "sealed_money" || !e.CanApproveOnce() {
		t.Fatalf("the hold names the reason and can be approved once: %+v", e.Breaches)
	}
	if r.used("card-a") != 1000 {
		t.Fatalf("only the allowed call drew: %d", r.used("card-a"))
	}
	if _, err := r.store.ApproveOnce(context.Background(), tenantID, d.ExceptionID, "owner@x", r.clock); err != nil {
		t.Fatal(err)
	}
	if got := r.decideAsking("p1", 1000); got.Outcome != mandate.OutcomeWithinLimit {
		t.Fatalf("the approved call: %+v", got)
	}
	if r.used("card-a") != 2000 {
		t.Fatalf("the approved call draws on the Card: %d", r.used("card-a"))
	}
	if next := r.decideAsking("p2", 1000); next.Outcome != mandate.OutcomeOwnerRequired {
		t.Fatalf("the next call asks again: %+v", next)
	}

	// Over the Card too: both are on the hold, the limit and the ask.
	over := r.decideAsking("p3", 150000)
	oe, _ := r.store.Exception(context.Background(), tenantID, over.ExceptionID)
	if over.Outcome != mandate.OutcomeOwnerRequired || len(oe.Breaches) != 2 {
		t.Fatalf("over the Card and asking: %+v %+v", over, oe.Breaches)
	}

	refuse := homeCard("card-r", 50000, 500000)
	refuse.OnExceed = mandate.OnExceedRefuse
	rr := newRig(t, refuse)
	if got := rr.decideAsking("q1", 1000); got.Allowed() || got.Outcome != mandate.OutcomeLimitReached || got.Reason != ownerAskWhy {
		t.Fatalf("a Card set to refuse refuses it: %+v", got)
	}
	if es, _ := rr.store.Exceptions(context.Background(), tenantID, ""); len(es) != 0 {
		t.Fatalf("a refusal held an exception: %+v", es)
	}
}

// A trial of a request that asks its owner says it would be held, as a
// real call is.
func TestATrialOfAnOwnerAskWouldBeHeld(t *testing.T) {
	req := refund(1000)
	req.OwnerAsk, req.OwnerAskWhy = "sealed_money", ownerAskWhy
	d, err := mandate.Trial([]mandate.Mandate{homeCard("card-a", 50000, 500000)}, req, used(0), base)
	if err != nil || d.Outcome != mandate.OutcomeOwnerRequired || len(d.Breaches) != 1 {
		t.Fatalf("%+v %v", d, err)
	}
	if d, _ := mandate.Trial([]mandate.Mandate{homeCard("card-a", 50000, 500000)}, refund(1000), used(0), base); !d.Allowed() {
		t.Fatalf("without the ask: %+v", d)
	}
}
