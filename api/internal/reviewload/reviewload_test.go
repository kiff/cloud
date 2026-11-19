package reviewload

import "testing"

// The engagement is sold on this number, which means a prospect will
// check it against their own records. Every rule below exists so that
// when they do, it holds.

// A refusal removes a person's attention too, so counting it would
// raise the headline figure. It stays out: a refused action is also
// work that did not happen, which is a different thing to sell and
// sometimes a cost to the customer.
func TestARefusalIsNotReviewRemoved(t *testing.T) {
	l := FromCounts(map[string]int{
		"allowed": 60, "approval_required": 20, "blocked": 20,
	}, true)

	if l.Total() != 100 {
		t.Fatalf("total = %d, want 100", l.Total())
	}
	if got := l.Percent(); got != 60 {
		t.Errorf("Percent = %d, want 60; a blocked action in the numerator would have made it 80", got)
	}
	if l.WouldBeRefused != 20 {
		t.Errorf("WouldBeRefused = %d, want 20", l.WouldBeRefused)
	}
}

// Our own billing limit turning someone away says nothing about their
// operation, and letting it into the denominator would drag the ratio
// down for a reason the customer cannot act on.
func TestOurBillingLimitIsNotTheirOperation(t *testing.T) {
	withMeter := FromCounts(map[string]int{
		"allowed": 60, "approval_required": 20, "blocked": 20, "limit_exceeded": 900,
	}, true)
	without := FromCounts(map[string]int{
		"allowed": 60, "approval_required": 20, "blocked": 20,
	}, true)

	if withMeter.Percent() != without.Percent() {
		t.Errorf("metering moved the ratio: %d vs %d", withMeter.Percent(), without.Percent())
	}
	if withMeter.Total() != 100 {
		t.Errorf("total = %d, want 100; metered decisions are excluded", withMeter.Total())
	}
	if withMeter.Metered != 900 {
		t.Errorf("Metered = %d, want 900 — reported so the totals reconcile", withMeter.Metered)
	}
}

// 100% off four decisions is the number that destroys the engagement
// the first time a customer checks it.
func TestAThinWindowRefusesToAnswer(t *testing.T) {
	thin := FromCounts(map[string]int{"allowed": 4}, true)

	if thin.Sufficient() {
		t.Errorf("a window of %d decisions reported as sufficient", thin.Total())
	}
	if got := thin.Percent(); got != 0 {
		t.Errorf("Percent = %d on an insufficient sample, want 0 with Sufficient false", got)
	}

	// One short, then exactly enough.
	just := FromCounts(map[string]int{"allowed": MinimumSample - 1}, true)
	if just.Sufficient() {
		t.Errorf("%d decisions reported as sufficient; the floor is %d", just.Total(), MinimumSample)
	}
	at := FromCounts(map[string]int{"allowed": MinimumSample}, true)
	if !at.Sufficient() {
		t.Errorf("%d decisions reported as insufficient at the floor", at.Total())
	}
}

// Zero from Percent means "not answerable", never "nothing could be
// automated". Anything rendering the number has to ask Sufficient
// first, and this pins the distinction the renderer depends on.
func TestZeroPercentAndUnanswerableAreDifferentFacts(t *testing.T) {
	unanswerable := FromCounts(map[string]int{"allowed": 3}, true)
	nothingAutomatable := FromCounts(map[string]int{"approval_required": 100}, true)

	if unanswerable.Percent() != 0 || nothingAutomatable.Percent() != 0 {
		t.Fatal("both cases should read 0 from Percent alone")
	}
	if unanswerable.Sufficient() {
		t.Error("the thin window must report insufficient")
	}
	if !nothingAutomatable.Sufficient() {
		t.Error("a full window of approvals is a real answer: everything needs a person")
	}
}

// A future outcome must not land in a bucket by default and quietly
// move the figure.
func TestAnUnknownOutcomeIsIgnoredRatherThanBucketed(t *testing.T) {
	l := FromCounts(map[string]int{
		"allowed": 50, "approval_required": 50, "quarantined_pending_review": 1000,
	}, true)

	if l.Total() != 100 {
		t.Errorf("total = %d, want 100; an unrecognised outcome was counted", l.Total())
	}
	if l.Percent() != 50 {
		t.Errorf("Percent = %d, want 50", l.Percent())
	}
}

// In observe mode nothing was withheld, so every figure is a
// counterfactual and the surface has to say "would have". The flag is
// carried rather than inferred because the tense of every sentence
// around the number depends on it.
func TestTenseIsCarriedNotInferred(t *testing.T) {
	if FromCounts(map[string]int{"allowed": 40}, false).Enforcing {
		t.Error("observe mode reported as enforcing")
	}
	if !FromCounts(map[string]int{"allowed": 40}, true).Enforcing {
		t.Error("enforce mode reported as observing")
	}
}

func TestPercentRounds(t *testing.T) {
	for _, tc := range []struct {
		allowed, person, want int
	}{
		{30, 0, 100},
		{0, 30, 0},
		{15, 15, 50},
		// 2/3 rounds to 67, not 66.
		{40, 20, 67},
		// 1/3 rounds to 33.
		{20, 40, 33},
	} {
		l := FromCounts(map[string]int{
			"allowed": tc.allowed, "approval_required": tc.person,
		}, true)
		if got := l.Percent(); got != tc.want {
			t.Errorf("allowed=%d person=%d: Percent = %d, want %d", tc.allowed, tc.person, got, tc.want)
		}
	}
}
