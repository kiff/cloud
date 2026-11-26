package meter

import "testing"

// A credit pays for $0.0027 of AI, the inverse of Credits' rate, so a plan's
// period read in micro-USD is the same credits KIFF counts runs in.
func TestACreditPaysForItsAI(t *testing.T) {
	if MicroUSDPerCredit != 2_700 {
		t.Fatalf("MicroUSDPerCredit = %d, want 2,700 ($0.0027)", MicroUSDPerCredit)
	}
	for _, n := range []int64{1, 500, 5_000, 56_500} {
		if got := Credits(n * MicroUSDPerCredit); got != n {
			t.Errorf("Credits(%d credits' AI) = %d", n, got)
		}
	}
}

// The plan the allowance answer says: on Solo and Company, everything the
// period can pay for plus Coach's fair use; nothing for an unbounded plan or
// no plan.
func TestThePlanTheAllowanceSays(t *testing.T) {
	period := func(a AI) int64 {
		t.Helper()
		if a.PeriodMicroUSD == nil {
			t.Fatalf("%s: no period", a.Kind)
		}
		return *a.PeriodMicroUSD
	}
	// Solo: the included 500, 1,000 drawn from packs this period and 5,000
	// left. The free overshoot isn't counted.
	solo := ComputeBalance(BalanceIn{Kind: BalanceSolo, Used: 1_800, Over: 300, PacksDrawn: 1_000, PacksLeft: 5_000})
	if a := AIOf(solo); a.Kind != BalanceSolo || period(a) != 6_500*MicroUSDPerCredit+SetupAllowanceMicroUSD {
		t.Fatalf("Solo with packs: %s %d", a.Kind, period(a))
	}
	if a := AIOf(ComputeBalance(BalanceIn{Kind: BalanceSolo})); period(a) != SoloIncludedCredits*MicroUSDPerCredit+SetupAllowanceMicroUSD {
		t.Fatalf("Solo alone: %d", period(a))
	}
	// Used up, it still says the period: the share is a bound, not the gate.
	if a := AIOf(ComputeBalance(BalanceIn{Kind: BalanceSolo, Used: SoloIncludedCredits})); period(a) != SoloIncludedCredits*MicroUSDPerCredit+SetupAllowanceMicroUSD {
		t.Fatalf("Solo used up: %d", period(a))
	}
	// Company: 3 seats' included credits and a limit raised to €550, at a
	// cent a credit; and the default limit.
	team := ComputeBalance(BalanceIn{Kind: BalanceCompany, Seats: 3, LimitCents: 55_000})
	if a := AIOf(team); a.Kind != BalanceCompany || period(a) != (1_500+55_000)*MicroUSDPerCredit+SetupAllowanceMicroUSD {
		t.Fatalf("Team, raised limit: %s %d", a.Kind, period(a))
	}
	if a := AIOf(ComputeBalance(BalanceIn{Kind: BalanceCompany, Seats: 1})); period(a) != (500+SpendLimitStepCents)*MicroUSDPerCredit+SetupAllowanceMicroUSD {
		t.Fatalf("Team, default limit: %d", period(a))
	}
	for _, k := range []BalanceKind{BalanceUnbounded, BalanceNone} {
		if a := AIOf(ComputeBalance(BalanceIn{Kind: k})); a.Kind != k || a.PeriodMicroUSD != nil {
			t.Fatalf("%s: %+v", k, a)
		}
	}
	if a := AIOf(Balance{Kind: "something new"}); a.Kind != BalanceNone || a.PeriodMicroUSD != nil {
		t.Fatalf("an unknown kind: %+v", a)
	}
}
