package thune_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/kiff/cloud/gateway/internal/thune"
)

// Invariant: a refund whose answer was lost still takes from its charge's remainder until the processor lists it.
// Category: accounting
// Attack: a second request asks for the rest of a charge while the first refund's answer is lost and the processor doesn't list it yet
// Refused: the lost refund counts as pending, so the second is refused over what's left or held for the owner as a second refund
//
// A €20 refund is sent and its answer is lost, and the processor doesn't
// list it yet. Another request then asks €90 on the same €100 charge: the
// case counts the lost €20 as pending, so €90 is over what's left, and
// nothing is sent.
func TestAnUnknownRefundCountsInTheNextCaseOnItsCharge(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.proc.delay = 10 * time.Minute
	if _, err := e.gate.Before(ctx, req, thune.Ask{Amount: 2000}, "tc-lost"); err != nil {
		t.Fatal(err)
	}
	e.sendRefund(t, "tc-lost", 2000, true)
	e.advance(5 * time.Second)
	other := req
	other.Request = "ticket-8"
	b, err := e.gate.Before(ctx, other, thune.Ask{Amount: 9000}, "tc-new")
	if err != nil {
		t.Fatal(err)
	}
	if b.Case.Facts.Remainder() != 8000 {
		t.Errorf("remainder = %d, want 8000: the lost €20 wasn't counted", b.Case.Facts.Remainder())
	}
	if b.Case.Verdict != thune.VerdictRefuse || !slices.Contains(b.Case.Reasons, thune.ReasonOverRemainder) {
		t.Errorf("€90 on €80 left: verdict %s %v, want refused over the remainder", b.Case.Verdict, b.Case.Reasons)
	}
	if won, _ := e.gate.Sending(ctx, "tc-new"); won {
		t.Error("a refused case moved to sending")
	}
	if err := e.gate.After(ctx, other, "tc-new", thune.ExecRefused); err != nil {
		t.Fatal(err)
	}
	// A smaller one fits, and is held for the owner: it's a second refund.
	third := req
	third.Request = "ticket-9"
	b, err = e.gate.Before(ctx, third, thune.Ask{Amount: 1000}, "tc-small")
	if err != nil {
		t.Fatal(err)
	}
	if b.Case.Verdict != thune.VerdictAsk || !slices.Contains(b.Case.Reasons, thune.ReasonSecondRefund) {
		t.Errorf("€10 beside a lost €20: verdict %s %v, want held as a second refund", b.Case.Verdict, b.Case.Reasons)
	}
	_ = e.gate.After(ctx, third, "tc-small", thune.ExecRefused)
	// Once the processor lists it, it counts once, not twice.
	e.advance(10 * time.Minute)
	fourth := req
	fourth.Request = "ticket-10"
	b, err = e.gate.Before(ctx, fourth, thune.Ask{Amount: 1000}, "tc-later")
	if err != nil {
		t.Fatal(err)
	}
	if b.Case.Facts.Remainder() != 8000 {
		t.Errorf("remainder once listed = %d, want 8000", b.Case.Facts.Remainder())
	}
}

// racing is a reader that runs a hook before it answers: another retry of
// the same call sending while this one reads the charge.
type racing struct {
	p    *processor
	hook func()
}

func (r *racing) ReadCharge(ctx context.Context, tenant, account, charge string) (thune.Facts, error) {
	if r.hook != nil {
		h := r.hook
		r.hook = nil
		h()
	}
	return r.p.ReadCharge(ctx, tenant, account, charge)
}

// Invariant: a retry that loses the race to send never frees the claim of the retry that sent.
// Category: failure-modes
// Attack: two retries of one approved refund race, and the slower one reads the charge after the faster one's refund landed
// Refused: the slower one can't void a call already sending, so the claim stays and no other taf takes the request
//
// Two retries of one approved held call race. The first sends, and its
// refund lands while the second reads the charge; the second sees a
// changed charge, but the call is no longer its to void, so the claim
// stays and no other taf takes the request.
func TestARacingRetryKeepsTheClaimOfASentRefund(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if _, err := e.gate.Before(ctx, req, thune.Ask{Amount: 2000}, "tc-held"); err != nil {
		t.Fatal(err)
	}
	if err := e.gate.Held(ctx, req, "tc-held"); err != nil {
		t.Fatal(err)
	}
	e.advance(time.Hour)
	if b, err := e.gate.Before(ctx, req, thune.Ask{Amount: 2000}, "tc-held"); err != nil || b.Void {
		t.Fatalf("retry 1: %+v %v", b, err)
	}
	rr := &racing{p: e.proc, hook: func() {
		won, err := e.gate.Sending(ctx, "tc-held")
		if err != nil || !won {
			t.Fatalf("retry 1 sending: %v %v", won, err)
		}
		e.proc.refund("ch_1", 2000, "tc-held")
	}}
	g2 := &thune.Gate{Store: e.store, Reader: rr, Calls: e.calls}
	b2, err := g2.Before(ctx, req, thune.Ask{Amount: 2000}, "tc-held")
	if err != nil {
		t.Fatal(err)
	}
	if b2.Void {
		t.Error("retry 2 voided a call retry 1 had already sent")
	}
	a, _ := e.store.Get(ctx, "tc-held")
	if a.Execution != thune.ExecSending && a.Execution != thune.ExecSent {
		t.Errorf("after the race the call is %s, want sending or sent", a.Execution)
	}
	r, err := e.store.Claim(ctx, req, "tc-other-taf")
	if err != nil {
		t.Fatal(err)
	}
	if r.Won {
		t.Errorf("another taf took the request while its refund is at the processor (call %s)", a.Execution)
	}
	if e.proc.count() != 1 {
		t.Errorf("refunds at the processor = %d, want 1", e.proc.count())
	}
}

// A call that ended not sent on someone's word (the owner's "No" to "Did
// this happen?") whose refund the processor then lists by its marker is
// recorded as sent: the marker is the processor's word.
func TestARefundFoundByItsMarkerIsSentWhateverItsCallSaid(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if _, err := e.gate.Before(ctx, req, thune.Ask{Amount: 2000}, "tc-no"); err != nil {
		t.Fatal(err)
	}
	if won, err := e.gate.Sending(ctx, "tc-no"); err != nil || !won {
		t.Fatal(won, err)
	}
	e.proc.refund("ch_1", 2000, "tc-no")
	if err := e.gate.After(ctx, req, "tc-no", thune.ExecNotSent); err != nil {
		t.Fatal(err)
	}
	f, err := e.proc.ReadCharge(ctx, "t1", "acct_1", "ch_1")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.watch.Settle(ctx, "t1", f); err != nil {
		t.Fatal(err)
	}
	if a, _ := e.store.Get(ctx, "tc-no"); a.Execution != thune.ExecSent || a.Financial != thune.FinPending {
		t.Errorf("found by its marker: execution=%s financial=%s, want sent and pending", a.Execution, a.Financial)
	}
}
