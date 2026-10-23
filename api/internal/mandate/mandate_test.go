package mandate_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/domain"
	"github.com/kiff/cloud/api/internal/mandate"
)

var base = time.Date(2026, 9, 11, 14, 30, 0, 0, time.UTC)

func spendMandate(limit int64) mandate.Mandate {
	return mandate.Mandate{
		ID: "mdt-1", TenantID: "t1", Subject: "agent://refund-agent", Domain: "billing", Environment: "prod",
		Grants:     []mandate.Grant{{Action: "REFUND_INVOICE", EntityStates: []string{"PAID"}}},
		Aggregates: []mandate.Aggregate{{Quantity: mandate.Quantity{Kind: mandate.KindSum, Parameter: "amount_cents"}, Limit: limit, Window: mandate.WindowCalendarDay}},
		ValidFrom:  base.Add(-24 * time.Hour), ValidUntil: base.Add(24 * time.Hour),
	}
}

func countMandate(limit int64, w mandate.Window) mandate.Mandate {
	return mandate.Mandate{
		ID: "mdt-ops", TenantID: "t1", Subject: "agent://platform", Domain: "infra", Environment: "prod",
		Grants:     []mandate.Grant{{Action: "DEPLOY_SERVICE"}},
		Aggregates: []mandate.Aggregate{{Quantity: mandate.Quantity{Kind: mandate.KindCount}, Limit: limit, Window: w}},
		ValidFrom:  base.Add(-24 * time.Hour), ValidUntil: base.Add(24 * time.Hour),
	}
}

func req(subject, action, state string, params map[string]int64) mandate.Request {
	domainName, env := "billing", "prod"
	if strings.Contains(subject, "platform") {
		domainName, env = "infra", "prod"
	}
	return mandate.Request{Subject: subject, Domain: domainName, Environment: env,
		Action: action, EntityState: state, Parameters: params}
}

func used(n int64) mandate.Usage {
	return mandate.UsageFunc(func(string, mandate.Quantity, time.Time) (int64, error) { return n, nil })
}

// Invariant: a subject cannot exceed its cumulative limit, however many
// individually-permitted actions it proposes. Per-action ceilings bound
// one mistake; this bounds the day.
// Category: authority
// Attack: propose a further €600 refund after €4,600 has already been refunded today, each one individually under the per-action ceiling
// Refused: the draw is added to the window's running total before the decision, so the 51st compliant action is refused when the 50 before it exhausted the limit
func TestCumulativeLimitRefusesTheActionThatCrossesIt(t *testing.T) {
	m := spendMandate(500000) // €5,000
	d, err := mandate.Evaluate([]mandate.Mandate{m},
		req("agent://refund-agent", "REFUND_INVOICE", "PAID", map[string]int64{"amount_cents": 60000}),
		used(460000), base)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if d.Allowed() {
		t.Fatalf("allowed a draw that crosses the limit: %+v", d)
	}
	if d.Outcome != mandate.OutcomeLimitReached {
		t.Errorf("outcome = %s, want limit_reached", d.Outcome)
	}
	if d.Used != 460000 || d.Draw != 60000 || d.Limit != 500000 {
		t.Errorf("used/draw/limit = %d/%d/%d, want 460000/60000/500000", d.Used, d.Draw, d.Limit)
	}
	if !strings.Contains(d.Reason, "limit 500000") {
		t.Errorf("reason should state the limit: %q", d.Reason)
	}
}

func TestExactlyAtTheLimitIsAllowed(t *testing.T) {
	d, _ := mandate.Evaluate([]mandate.Mandate{spendMandate(500000)},
		req("agent://refund-agent", "REFUND_INVOICE", "PAID", map[string]int64{"amount_cents": 40000}),
		used(460000), base)
	if !d.Allowed() {
		t.Fatalf("a draw landing exactly on the limit must be allowed: %+v", d)
	}
}

// The primitive is not about money: count bounds irreversible work that
// has no amount at all.
func TestCountBoundsNonMonetaryActions(t *testing.T) {
	m := countMandate(2, mandate.WindowCalendarDay)
	r := req("agent://platform", "DEPLOY_SERVICE", "HEALTHY", nil)

	if d, _ := mandate.Evaluate([]mandate.Mandate{m}, r, used(1), base); !d.Allowed() {
		t.Fatalf("second deploy of the day should be allowed: %+v", d)
	}
	d, _ := mandate.Evaluate([]mandate.Mandate{m}, r, used(2), base)
	if d.Allowed() {
		t.Fatalf("third deploy should be refused: %+v", d)
	}
	if d.Draw != 1 {
		t.Errorf("a count draw is 1, got %d", d.Draw)
	}
}

// Invariant: when current usage cannot be established, the proposal is
// refused. A mandate is the authority itself, not a meter, so allowing
// when uncertain means acting with authority nobody could confirm.
// Category: failure-modes
// Attack: make the draw ledger unreadable at the moment a proposal arrives, hoping the check degrades to allow
// Refused: Evaluate returns ErrUsageUnavailable and the caller must refuse — the opposite default to the billing limiter beside it, which allows when uncertain on purpose
func TestMandateFailsClosedUnlikeTheUsageLimiter(t *testing.T) {
	boom := mandate.UsageFunc(func(string, mandate.Quantity, time.Time) (int64, error) {
		return 0, errors.New("postgres is unreachable")
	})
	_, err := mandate.Evaluate([]mandate.Mandate{spendMandate(500000)},
		req("agent://refund-agent", "REFUND_INVOICE", "PAID", map[string]int64{"amount_cents": 100}),
		boom, base)
	if !errors.Is(err, mandate.ErrUsageUnavailable) {
		t.Fatalf("err = %v, want ErrUsageUnavailable so the caller refuses", err)
	}
}

// Invariant: an expired or revoked mandate never widens authority. It
// is reported as expired rather than falling through to "no mandate
// applies", which would turn an expiry into a grant.
// Category: authority
// Attack: let a mandate lapse, then propose the action it used to bound, hoping the check finds no active mandate and waves it through
// Refused: a lapsed mandate that covers the action is reported as expired, so the decide path refuses instead of treating the subject as unbounded
func TestExpiredMandateDoesNotFallThroughToUnbounded(t *testing.T) {
	m := spendMandate(500000)
	m.ValidUntil = base.Add(-time.Hour)
	d, err := mandate.Evaluate([]mandate.Mandate{m},
		req("agent://refund-agent", "REFUND_INVOICE", "PAID", map[string]int64{"amount_cents": 100}),
		used(0), base)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if d.Outcome != mandate.OutcomeExpired || d.Allowed() {
		t.Fatalf("outcome = %s allowed = %v, want expired and refused", d.Outcome, d.Allowed())
	}
}

func TestRevokedMandateBehavesTheSameWay(t *testing.T) {
	m := spendMandate(500000)
	revoked := base.Add(-time.Minute)
	m.RevokedAt = &revoked
	d, _ := mandate.Evaluate([]mandate.Mandate{m},
		req("agent://refund-agent", "REFUND_INVOICE", "PAID", map[string]int64{"amount_cents": 100}),
		used(0), base)
	if d.Allowed() {
		t.Fatalf("a revoked mandate must not authorize: %+v", d)
	}
}

// Issuing a first mandate must never break a running tenant, so an
// action no mandate covers is a no-op rather than a refusal.
func TestUncoveredActionIsANoOp(t *testing.T) {
	d, err := mandate.Evaluate([]mandate.Mandate{spendMandate(500000)},
		req("agent://refund-agent", "CANCEL_INVOICE", "PAID", nil), used(0), base)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if d.Outcome != mandate.OutcomeNotCovered || !d.Allowed() {
		t.Fatalf("outcome = %s, want not_covered and allowed", d.Outcome)
	}
}

// Invariant: where several mandates cover one action, the tightest
// binds. Issuing an additional mandate can only narrow authority.
// Category: authority
// Attack: hold a generous domain-wide mandate and issue a second, narrower one, expecting the generous one to still permit the action
// Refused: mandates are conjunctive, so every covering mandate must have room and the tighter limit decides
func TestTighterMandateBindsWhenSeveralCover(t *testing.T) {
	loose := spendMandate(500000)
	tight := spendMandate(10000)
	tight.ID = "mdt-tight"
	d, _ := mandate.Evaluate([]mandate.Mandate{loose, tight},
		req("agent://refund-agent", "REFUND_INVOICE", "PAID", map[string]int64{"amount_cents": 20000}),
		used(0), base)
	if d.Allowed() {
		t.Fatalf("the tighter mandate must bind: %+v", d)
	}
	if d.MandateID != "mdt-tight" {
		t.Errorf("MandateID = %q, want the tighter one", d.MandateID)
	}
}

func TestAnotherSubjectsMandateDoesNotApply(t *testing.T) {
	d, _ := mandate.Evaluate([]mandate.Mandate{spendMandate(1)},
		req("agent://someone-else", "REFUND_INVOICE", "PAID", map[string]int64{"amount_cents": 999999}),
		used(0), base)
	if d.Outcome != mandate.OutcomeNotCovered {
		t.Errorf("outcome = %s, want not_covered — a mandate binds its own subject", d.Outcome)
	}
}

func TestGrantStateNarrowsCoverage(t *testing.T) {
	d, _ := mandate.Evaluate([]mandate.Mandate{spendMandate(1)},
		req("agent://refund-agent", "REFUND_INVOICE", "REFUNDED", map[string]int64{"amount_cents": 999999}),
		used(0), base)
	if d.Outcome != mandate.OutcomeNotCovered {
		t.Errorf("outcome = %s: a grant scoped to PAID must not cover REFUNDED", d.Outcome)
	}
}

func TestParseQuantity(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want mandate.Quantity
		ok   bool
	}{
		{"count", mandate.Quantity{Kind: mandate.KindCount}, true},
		{"sum(amount_cents)", mandate.Quantity{Kind: mandate.KindSum, Parameter: "amount_cents"}, true},
		{"  sum( row_count )  ", mandate.Quantity{Kind: mandate.KindSum, Parameter: "row_count"}, true},
		{"sum()", mandate.Quantity{}, false},
		{"sum(a(b))", mandate.Quantity{}, false},
		{"total(x)", mandate.Quantity{}, false},
		{"", mandate.Quantity{}, false},
	} {
		got, err := mandate.ParseQuantity(tc.in)
		if tc.ok != (err == nil) {
			t.Errorf("ParseQuantity(%q) err = %v, wanted ok = %v", tc.in, err, tc.ok)
			continue
		}
		if tc.ok && got != tc.want {
			t.Errorf("ParseQuantity(%q) = %+v, want %+v", tc.in, got, tc.want)
		}
	}
}

func TestCalendarDayResetsInTheMandatesTimezone(t *testing.T) {
	// 00:30 UTC on the 11th is still the 10th in New York, so a
	// calendar-day window anchored there has not reset yet.
	at := time.Date(2026, 9, 11, 0, 30, 0, 0, time.UTC)
	utcStart, err := mandate.WindowCalendarDay.Start(at, time.UTC)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip("tzdata unavailable")
	}
	nyStart, err := mandate.WindowCalendarDay.Start(at, ny)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if !nyStart.Before(utcStart) {
		t.Errorf("New York window start %v should precede UTC start %v", nyStart, utcStart)
	}
}

func TestRollingWindowsLookBackFromNow(t *testing.T) {
	got, _ := mandate.WindowRollingHour.Start(base, time.UTC)
	if !got.Equal(base.Add(-time.Hour)) {
		t.Errorf("rolling_1h start = %v, want %v", got, base.Add(-time.Hour))
	}
	if _, err := mandate.Window("fortnight").Start(base, time.UTC); !errors.Is(err, mandate.ErrUnknownWindow) {
		t.Errorf("unknown window err = %v, want ErrUnknownWindow", err)
	}
}

// Invariant: a mandate that sums a parameter an action does not declare
// as numeric is refused when it is issued, not discovered at runtime.
// Category: authority
// Attack: issue a mandate capping sum(reason), where reason is a string, so the limit silently sums nothing and never binds
// Refused: issue-time validation reads the action's declared parameter types and rejects the mandate
func TestMandateSummingANonNumericParameterIsRefusedAtIssue(t *testing.T) {
	actions := []domain.ActionSpec{{
		Name:          "REFUND_INVOICE",
		AllowedStates: []string{"PAID"},
		Parameters: []domain.ParameterSpec{
			{Name: "amount_cents", Type: "integer"},
			{Name: "reason", Type: "string"},
		},
	}}

	ok := spendMandate(500000)
	if err := mandate.Validate(ok, actions); err != nil {
		t.Fatalf("a mandate summing a declared integer must validate: %v", err)
	}

	bad := spendMandate(500000)
	bad.Aggregates[0].Quantity = mandate.Quantity{Kind: mandate.KindSum, Parameter: "reason"}
	err := mandate.Validate(bad, actions)
	if !errors.Is(err, mandate.ErrInvalidMandate) || !strings.Contains(err.Error(), "string") {
		t.Fatalf("err = %v, want refusal naming the declared type", err)
	}

	missing := spendMandate(500000)
	missing.Aggregates[0].Quantity = mandate.Quantity{Kind: mandate.KindSum, Parameter: "nope"}
	if err := mandate.Validate(missing, actions); !errors.Is(err, mandate.ErrInvalidMandate) {
		t.Errorf("summing an undeclared parameter must be refused, got %v", err)
	}
}

// A mandate narrows; it must never widen. Naming a state the contract
// does not allow would read as granting it.
func TestMandateCannotWidenTheContract(t *testing.T) {
	actions := []domain.ActionSpec{{
		Name: "REFUND_INVOICE", AllowedStates: []string{"PAID"},
		Parameters: []domain.ParameterSpec{{Name: "amount_cents", Type: "integer"}},
	}}
	m := spendMandate(500000)
	m.Grants[0].EntityStates = []string{"PAID", "REFUNDED"}
	err := mandate.Validate(m, actions)
	if !errors.Is(err, mandate.ErrInvalidMandate) || !strings.Contains(err.Error(), "widen") {
		t.Fatalf("err = %v, want refusal for widening the contract", err)
	}
}

func TestMandateWithNoAggregateIsRefused(t *testing.T) {
	actions := []domain.ActionSpec{{Name: "REFUND_INVOICE", AllowedStates: []string{"PAID"},
		Parameters: []domain.ParameterSpec{{Name: "amount_cents", Type: "integer"}}}}
	m := spendMandate(1)
	m.Aggregates = nil
	if err := mandate.Validate(m, actions); !errors.Is(err, mandate.ErrInvalidMandate) {
		t.Errorf("a mandate that bounds nothing must be refused, got %v", err)
	}
}

func TestMandateForAnUndeclaredActionIsRefused(t *testing.T) {
	m := spendMandate(1)
	if err := mandate.Validate(m, []domain.ActionSpec{{Name: "SOMETHING_ELSE"}}); !errors.Is(err, mandate.ErrInvalidMandate) {
		t.Errorf("err = %v, want refusal for an action the domain does not declare", err)
	}
}

func TestCoveringSumCardRefusesUncountableDraw(t *testing.T) {
	r := req("agent://refund-agent", "REFUND_INVOICE", "PAID", nil)
	r.Uncountable = map[string]bool{"amount_cents": true}
	d, err := mandate.Evaluate([]mandate.Mandate{spendMandate(500000)}, r,
		mandate.UsageFunc(func(string, mandate.Quantity, time.Time) (int64, error) {
			t.Fatal("an uncountable draw must stop before reading usage")
			return 0, nil
		}), base)
	if err != nil || d.Outcome != mandate.OutcomeUncountableDraw || d.Allowed() {
		t.Fatalf("decision = %+v, err = %v", d, err)
	}
}

func TestUncountableOnlyMattersToCoveringSum(t *testing.T) {
	r := req("agent://refund-agent", "REFUND_INVOICE", "PAID",
		map[string]int64{"amount_cents": 100})
	r.Uncountable = map[string]bool{"reason": true}
	if d, err := mandate.Evaluate([]mandate.Mandate{spendMandate(500000)}, r, used(0), base); err != nil || !d.Allowed() {
		t.Fatalf("unrelated string refused: %+v, %v", d, err)
	}
	r.Uncountable = map[string]bool{"amount_cents": true}
	if d, err := mandate.Evaluate(nil, r, used(0), base); err != nil || d.Outcome != mandate.OutcomeNotCovered {
		t.Fatalf("uncovered action refused: %+v, %v", d, err)
	}
}

func TestSumParameterMatchesCaseInsensitiveValidation(t *testing.T) {
	m := spendMandate(5000)
	m.Aggregates[0].Quantity.Parameter = "Amount_Cents"
	r := req("agent://refund-agent", "REFUND_INVOICE", "PAID",
		map[string]int64{"amount_cents": 2})
	d, err := mandate.Evaluate([]mandate.Mandate{m}, r, used(4999), base)
	if err != nil || d.Outcome != mandate.OutcomeLimitReached || d.Draw != 2 {
		t.Fatalf("case-variant sum = %+v, err = %v", d, err)
	}
	r.Uncountable = map[string]bool{"AMOUNT_CENTS": true}
	d, err = mandate.Evaluate([]mandate.Mandate{m}, r, used(0), base)
	if err != nil || d.Outcome != mandate.OutcomeUncountableDraw {
		t.Fatalf("case-variant uncountable = %+v, err = %v", d, err)
	}
}

func TestSumRefusesAmbiguousCaseVariantValues(t *testing.T) {
	r := req("agent://refund-agent", "REFUND_INVOICE", "PAID",
		map[string]int64{"amount_cents": 100, "Amount_Cents": 1})
	d, err := mandate.Evaluate([]mandate.Mandate{spendMandate(5000)}, r, used(0), base)
	if err != nil || d.Outcome != mandate.OutcomeLimitReached || d.Draw != -1 {
		t.Fatalf("ambiguous draw = %+v, err = %v", d, err)
	}
}
