package meter

import "testing"

func pct(b Balance) int {
	if b.MeterPct == nil {
		return -1
	}
	return *b.MeterPct
}

func left(b Balance) int64 {
	if b.Left == nil {
		return -1
	}
	return *b.Left
}

func TestSoloBalance(t *testing.T) {
	inc := int64(SoloIncludedCredits)
	for _, c := range []struct {
		name       string
		in         BalanceIn
		state, why string
		pct        int
		left       int64
	}{
		{"nothing used", BalanceIn{Kind: BalanceSolo}, StateOK, "", 0, inc},
		{"below the warning", BalanceIn{Kind: BalanceSolo, Used: inc * 79 / 100}, StateOK, "", 79, inc - inc*79/100},
		{"at the warning", BalanceIn{Kind: BalanceSolo, Used: inc * 80 / 100}, StateWarning, "", 80, inc - inc*80/100},
		{"included used, no pack", BalanceIn{Kind: BalanceSolo, Used: inc}, StateWaiting, WhyUsedUp, 100, 0},
		// Packs count with the included credits: drawn ones and those left.
		{"into the packs", BalanceIn{Kind: BalanceSolo, Used: inc + 100, PacksDrawn: 100, PacksLeft: 300}, StateOK, "", int((inc + 100) * 100 / (inc + 400)), 300},
		{"a pack left, not yet drawn", BalanceIn{Kind: BalanceSolo, Used: inc + 100, PacksLeft: 400}, StateOK, "", int((inc + 100) * 100 / (inc + 400)), 300},
		{"packs used too", BalanceIn{Kind: BalanceSolo, Used: inc + 400, PacksDrawn: 400}, StateWaiting, WhyUsedUp, 100, 0},
		// A run going when the balance ran out finished: its credits past it
		// are free overshoot, and the period waits.
		{"past the balance", BalanceIn{Kind: BalanceSolo, Used: inc + 40, Over: 40}, StateWaiting, WhyUsedUp, 100, 0},
		// A pack bought after the overshoot lets the tafs go on, all of it:
		// the overshoot isn't taken from it.
		{"a pack after the overshoot", BalanceIn{Kind: BalanceSolo, Used: inc + 40, Over: 40, PacksLeft: 500}, StateOK, "", int(inc * 100 / (inc + 500)), 500},
	} {
		t.Run(c.name, func(t *testing.T) {
			b := ComputeBalance(c.in)
			if b.State != c.state || b.Why != c.why || pct(b) != c.pct || left(b) != c.left || b.Included != inc {
				t.Fatalf("got state %q why %q meter %d left %d included %d, want %q %q %d %d", b.State, b.Why, pct(b), left(b), b.Included, c.state, c.why, c.pct, c.left)
			}
		})
	}
}

func TestCompanyBalance(t *testing.T) {
	inc := int64(CompanyCreditsPerSeat * 4)
	step := int64(SpendLimitStepCents)
	beyond := func(cents int64) int64 { return inc + cents/CentsPerCredit }
	for _, c := range []struct {
		name       string
		in         BalanceIn
		state, why string
		pct        int
		cents      int64
	}{
		{"in the included credits", BalanceIn{Kind: BalanceCompany, Seats: 4, Used: inc / 2}, StateOK, "", 50, 0},
		// Using the included credits warns nobody: the bar fills, then moves to the limit.
		{"all the included credits", BalanceIn{Kind: BalanceCompany, Seats: 4, Used: inc}, StateOK, "", 100, 0},
		{"beyond, below the warning", BalanceIn{Kind: BalanceCompany, Seats: 4, Used: beyond(step * 79 / 100)}, StateOK, "", 79, step * 79 / 100},
		{"the limit's 80%", BalanceIn{Kind: BalanceCompany, Seats: 4, Used: beyond(step * 80 / 100)}, StateWarning, "", 80, step * 80 / 100},
		{"the limit", BalanceIn{Kind: BalanceCompany, Seats: 4, Used: beyond(step)}, StateWaiting, WhyLimit, 100, step},
		// Runs going when it was reached finish, and go on the invoice past it.
		{"past the limit", BalanceIn{Kind: BalanceCompany, Seats: 4, Used: beyond(step + 300)}, StateWaiting, WhyLimit, 100, step + 300},
		{"a raised limit", BalanceIn{Kind: BalanceCompany, Seats: 4, Used: beyond(step), LimitCents: 2 * step}, StateOK, "", 50, step},
	} {
		t.Run(c.name, func(t *testing.T) {
			b := ComputeBalance(c.in)
			if b.State != c.state || b.Why != c.why || pct(b) != c.pct || b.BeyondCents != c.cents || b.Included != inc {
				t.Fatalf("got state %q why %q meter %d beyond %d included %d, want %q %q %d %d", b.State, b.Why, pct(b), b.BeyondCents, b.Included, c.state, c.why, c.pct, c.cents)
			}
			if c.in.LimitCents == 0 && b.LimitCents != step {
				t.Fatalf("the default limit: got %d, want %d", b.LimitCents, step)
			}
		})
	}
}

func TestUnboundedAndNoPlan(t *testing.T) {
	b := ComputeBalance(BalanceIn{Kind: BalanceUnbounded, Used: 1_000_000})
	if b.State != StateOK || b.MeterPct != nil || b.Left != nil {
		t.Fatalf("unbounded: %+v", b)
	}
	for _, k := range []BalanceKind{BalanceNone, "", "trial"} {
		b := ComputeBalance(BalanceIn{Kind: k})
		if b.Kind != BalanceNone || b.State != StateWaiting || b.Why != WhyNoPlan || b.MeterPct != nil {
			t.Fatalf("%q: %+v", k, b)
		}
	}
}

func TestAllow(t *testing.T) {
	ok := ComputeBalance(BalanceIn{Kind: BalanceSolo})
	usedUp := ComputeBalance(BalanceIn{Kind: BalanceSolo, Used: SoloIncludedCredits})
	limit := ComputeBalance(BalanceIn{Kind: BalanceCompany, Seats: 2, Used: 2*CompanyCreditsPerSeat + SpendLimitStepCents/CentsPerCredit})
	none := ComputeBalance(BalanceIn{Kind: BalanceNone})
	unbounded := ComputeBalance(BalanceIn{Kind: BalanceUnbounded})
	for _, c := range []struct {
		name                    string
		b                       Balance
		setup                   bool
		setupSpent, noPlanSpent int64
		allowed                 bool
		why                     string
	}{
		{"a taf, balance left", ok, false, 0, 0, true, ""},
		{"a taf, used up", usedUp, false, 0, 0, false, WhyUsedUp},
		{"a taf, at the limit", limit, false, 0, 0, false, WhyLimit},
		{"a taf, no plan", none, false, 0, 0, false, WhyNoPlan},
		{"a taf, unbounded", unbounded, false, 0, 0, true, ""},
		// Coach within its fair use doesn't wait while a plan runs.
		{"Coach in its fair use, used up", usedUp, true, SetupAllowanceMicroUSD - 1, 0, true, ""},
		{"Coach past its fair use, used up", usedUp, true, SetupAllowanceMicroUSD, 0, false, WhyUsedUp},
		{"Coach past its fair use, balance left", ok, true, SetupAllowanceMicroUSD, 0, true, ""},
		// With no plan, Coach has its own small allowance, once.
		{"Coach, no plan, in its allowance", none, true, 0, NoPlanSetupMicroUSD - 1, true, ""},
		{"Coach, no plan, past it", none, true, 0, NoPlanSetupMicroUSD, false, WhyNoPlan},
	} {
		t.Run(c.name, func(t *testing.T) {
			allowed, why := Allow(c.b, c.setup, c.setupSpent, c.noPlanSpent)
			if allowed != c.allowed || why != c.why {
				t.Fatalf("got %v %q, want %v %q", allowed, why, c.allowed, c.why)
			}
		})
	}
}
