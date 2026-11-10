package thune

import (
	"slices"
	"testing"
	"time"
)

var now0 = time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)

func charge100(refunds ...Refund) Facts {
	return Facts{Account: "acct_1", Charge: "ch_1", Currency: "eur", Captured: 10000, Refunds: refunds, ReadAt: now0}
}

var id1 = Identity{TenantID: "t1", Account: "acct_1", Object: "ch_1", Kind: KindRefund, Request: "ticket-7"}

func TestACaseAllowsTheRequestedAmountWhenTheRemainderCoversIt(t *testing.T) {
	c := Build(id1, Ask{Amount: 2000}, charge100(), now0)
	if c.Verdict != VerdictAllow || c.Authorized != 2000 || len(c.Reasons) != 0 {
		t.Fatalf("case = %+v", c)
	}
}

func TestACaseNeverChangesTheAmountQuietly(t *testing.T) {
	c := Build(id1, Ask{Amount: 9000}, charge100(Refund{ID: "re_1", Amount: 2000, Status: RefundSucceeded}), now0)
	if c.Verdict != VerdictRefuse || c.Authorized != 0 || !slices.Equal(c.Reasons, []string{ReasonOverRemainder}) {
		t.Fatalf("over the remainder: %+v", c)
	}
}

func TestTheRemainderBecomesTheFigureRead(t *testing.T) {
	c := Build(id1, Ask{Remainder: true}, charge100(
		Refund{ID: "re_1", Amount: 2000, Status: RefundSucceeded},
		Refund{ID: "re_2", Amount: 1000, Status: RefundPending},
		Refund{ID: "re_3", Amount: 5000, Status: RefundFailed}), now0)
	if c.Authorized != 7000 {
		t.Fatalf("authorized %d, want 7000 (failed refunds give the money back)", c.Authorized)
	}
	if c.Verdict != VerdictAsk || !slices.Contains(c.Reasons, ReasonSecondRefund) {
		t.Fatalf("a charge with a refund asks: %+v", c)
	}
}

func TestASecondRefundOnAChargeIsTheOwners(t *testing.T) {
	c := Build(id1, Ask{Amount: 1000}, charge100(Refund{ID: "re_1", Amount: 2000, Status: RefundPending}), now0)
	if c.Verdict != VerdictAsk || c.Authorized != 1000 || !slices.Equal(c.Reasons, []string{ReasonSecondRefund}) {
		t.Fatalf("case = %+v", c)
	}
	// A failed or cancelled refund is no first refund.
	c = Build(id1, Ask{Amount: 1000}, charge100(Refund{ID: "re_1", Amount: 2000, Status: RefundCancelled}), now0)
	if c.Verdict != VerdictAllow {
		t.Fatalf("after a cancelled refund: %+v", c)
	}
}

func TestACaseWithNoRequestIsHeld(t *testing.T) {
	id := id1
	id.Request = ""
	c := Build(id, Ask{Amount: 1000}, charge100(), now0)
	if c.Verdict != VerdictAsk || !slices.Equal(c.Reasons, []string{ReasonNoRequest}) {
		t.Fatalf("case = %+v", c)
	}
}

func TestACaseIsRefused(t *testing.T) {
	for name, tc := range map[string]struct {
		ask   Ask
		facts Facts
		want  string
	}{
		"dispute open":   {Ask{Amount: 100}, func() Facts { f := charge100(); f.Disputed = true; return f }(), ReasonDisputeOpen},
		"nothing left":   {Ask{Remainder: true}, charge100(Refund{ID: "re_1", Amount: 10000, Status: RefundSucceeded}), ReasonNothingLeft},
		"zero":           {Ask{Amount: 0}, charge100(), ReasonBadAmount},
		"negative":       {Ask{Amount: -5}, charge100(), ReasonBadAmount},
		"other currency": {Ask{Amount: 100, Currency: "usd"}, charge100(), ReasonCurrency},
		"stale facts":    {Ask{Amount: 100}, func() Facts { f := charge100(); f.ReadAt = now0.Add(-MaxFactsAge - time.Second); return f }(), ReasonFactsStale},
		"unread facts":   {Ask{Amount: 100}, func() Facts { f := charge100(); f.ReadAt = time.Time{}; return f }(), ReasonFactsStale},
	} {
		c := Build(id1, tc.ask, tc.facts, now0)
		if c.Verdict != VerdictRefuse || c.Authorized != 0 || !slices.Equal(c.Reasons, []string{tc.want}) {
			t.Errorf("%s: %+v", name, c)
		}
	}
}

func TestChangedFactsVoidAnApproval(t *testing.T) {
	was := charge100()
	same := charge100()
	same.ReadAt = now0.Add(3 * time.Hour)
	if Changed(was, same) {
		t.Fatal("the same facts read later are no change")
	}
	for name, f := range map[string]func(*Facts){
		"a dashboard refund": func(f *Facts) {
			f.Refunds = append(f.Refunds, Refund{ID: "re_x", Amount: 2000, Status: RefundSucceeded})
		},
		"a dispute opens":  func(f *Facts) { f.Disputed = true },
		"another charge":   func(f *Facts) { f.Charge = "ch_2" },
		"another currency": func(f *Facts) { f.Currency = "usd" },
	} {
		now := charge100()
		f(&now)
		if !Changed(was, now) {
			t.Errorf("%s: not seen as a change", name)
		}
	}
}
