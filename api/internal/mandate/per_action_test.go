package mandate_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/kiff/cloud/api/internal/domain"
	"github.com/kiff/cloud/api/internal/mandate"
)

// Per-action limits: up to €500 each, €5,000 a day.

func perActionMandate(perAction, total int64) mandate.Mandate {
	m := spendMandate(total)
	m.Grants[0].PerAction = []mandate.PerActionLimit{{
		Quantity: mandate.Quantity{Kind: mandate.KindSum, Parameter: "amount_cents"}, Max: perAction,
	}}
	return m
}

func refund(amount int64) mandate.Request {
	return req("agent://refund-agent", "REFUND_INVOICE", "PAID", map[string]int64{"amount_cents": amount})
}

func evaluate(t *testing.T, ms []mandate.Mandate, r mandate.Request, usedSoFar int64) mandate.Decision {
	t.Helper()
	d, err := mandate.Evaluate(ms, r, used(usedSoFar), base)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	return d
}

// Invariant: one action cannot carry more than the per-action limit,
// however much the Card's total has left.
// Category: authority
// Attack: propose a single €1,500 refund against a Card allowing €500 each and €5,000 a day, with nothing spent yet
// Refused: the per-action limit is checked before the totals and refuses with its own outcome
func TestPerActionLimit(t *testing.T) {
	m := perActionMandate(50000, 500000)
	for _, tc := range []struct {
		name   string
		amount int64
		used   int64
		want   mandate.Outcome
	}{
		{"exactly the per-action limit", 50000, 0, mandate.OutcomeWithinLimit},
		{"one cent over", 50001, 0, mandate.OutcomePerActionExceeded},
		{"per-action only: well over, total untouched", 150000, 0, mandate.OutcomePerActionExceeded},
		{"total only: small action, total nearly spent", 40000, 480000, mandate.OutcomeLimitReached},
		{"both: per-action is reported first", 150000, 480000, mandate.OutcomePerActionExceeded},
		{"negative amount", -100, 0, mandate.OutcomePerActionExceeded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := evaluate(t, []mandate.Mandate{m}, refund(tc.amount), tc.used)
			if d.Outcome != tc.want {
				t.Fatalf("outcome = %s, want %s (%s)", d.Outcome, tc.want, d.Reason)
			}
			if tc.want == mandate.OutcomePerActionExceeded && (d.Limit != 50000 || d.MandateID != "mdt-1") {
				t.Fatalf("decision should name the limit and the Card: %+v", d)
			}
		})
	}

	d := evaluate(t, []mandate.Mandate{m}, refund(150000), 0)
	if !strings.Contains(d.Reason, "sum(amount_cents) of 150000 is above the per-action limit of 50000") {
		t.Errorf("reason = %q", d.Reason)
	}
}

// A per-action limit is refused, not waved through, when the amount
// cannot be read reliably.
func TestPerActionLimitRefusesUnreadableAmounts(t *testing.T) {
	m := perActionMandate(50000, 500000)

	ambiguous := req("agent://refund-agent", "REFUND_INVOICE", "PAID",
		map[string]int64{"amount_cents": 100, "AMOUNT_CENTS": 100})
	if d := evaluate(t, []mandate.Mandate{m}, ambiguous, 0); d.Outcome != mandate.OutcomePerActionExceeded {
		t.Errorf("ambiguous case variants: %s", d.Outcome)
	}

	uncountable := refund(0)
	uncountable.Parameters = nil
	uncountable.Uncountable = map[string]bool{"amount_cents": true}
	if d := evaluate(t, []mandate.Mandate{m}, uncountable, 0); d.Outcome != mandate.OutcomeUncountableDraw {
		t.Errorf("uncountable amount: %s", d.Outcome)
	}

	// A missing parameter is the action contract's business, as for
	// totals: the Card does not become a second way to require it.
	missing := refund(0)
	missing.Parameters = nil
	if d := evaluate(t, []mandate.Mandate{m}, missing, 0); d.Outcome != mandate.OutcomeWithinLimit {
		t.Errorf("missing parameter: %s", d.Outcome)
	}
}

// A grant's per-action limit applies only where that grant applies, and
// where two Cards cover one action the tighter per-action limit binds.
func TestPerActionLimitScopeAndStacking(t *testing.T) {
	scoped := spendMandate(500000)
	scoped.Grants = []mandate.Grant{
		{Action: "REFUND_INVOICE", EntityStates: []string{"PAID"},
			PerAction: []mandate.PerActionLimit{{Quantity: mandate.Quantity{Kind: mandate.KindSum, Parameter: "amount_cents"}, Max: 10000}}},
		{Action: "REFUND_INVOICE", EntityStates: []string{"DISPUTED"}},
	}
	if d := evaluate(t, []mandate.Mandate{scoped}, refund(20000), 0); d.Outcome != mandate.OutcomePerActionExceeded {
		t.Errorf("PAID grant's limit should apply: %s", d.Outcome)
	}
	disputed := req("agent://refund-agent", "REFUND_INVOICE", "DISPUTED", map[string]int64{"amount_cents": 20000})
	if d := evaluate(t, []mandate.Mandate{scoped}, disputed, 0); d.Outcome != mandate.OutcomeWithinLimit {
		t.Errorf("DISPUTED grant has no per-action limit: %s", d.Outcome)
	}

	loose := perActionMandate(100000, 500000)
	tight := perActionMandate(30000, 500000)
	tight.ID = "mdt-tight"
	for _, order := range [][]mandate.Mandate{{loose, tight}, {tight, loose}} {
		d := evaluate(t, order, refund(50000), 0)
		if d.Outcome != mandate.OutcomePerActionExceeded || d.MandateID != "mdt-tight" {
			t.Errorf("the tighter Card must bind in either order: %+v", d)
		}
	}
}

func TestPerActionLimitValidation(t *testing.T) {
	actions := []domain.ActionSpec{{
		Name: "REFUND_INVOICE", AllowedStates: []string{"PAID"},
		Parameters: []domain.ParameterSpec{{Name: "amount_cents", Type: "integer"}, {Name: "reason", Type: "string"}},
	}}
	if err := mandate.Validate(perActionMandate(50000, 500000), actions); err != nil {
		t.Fatalf("a valid per-action limit must validate: %v", err)
	}
	for name, mutate := range map[string]func(*mandate.PerActionLimit){
		"count per action":      func(l *mandate.PerActionLimit) { l.Quantity = mandate.Quantity{Kind: mandate.KindCount} },
		"zero":                  func(l *mandate.PerActionLimit) { l.Max = 0 },
		"negative":              func(l *mandate.PerActionLimit) { l.Max = -5 },
		"undeclared parameter":  func(l *mandate.PerActionLimit) { l.Quantity.Parameter = "nope" },
		"non-numeric parameter": func(l *mandate.PerActionLimit) { l.Quantity.Parameter = "reason" },
	} {
		m := perActionMandate(50000, 500000)
		mutate(&m.Grants[0].PerAction[0])
		if err := mandate.Validate(m, actions); !errors.Is(err, mandate.ErrInvalidMandate) {
			t.Errorf("%s: err = %v, want ErrInvalidMandate", name, err)
		}
	}
}

// The same action granted twice with different state
// scopes keeps each scope's own per-action limit, in either grant order.
// A 20000 PAID refund stays refused under PAID ≤ 10000 even though
// DISPUTED allows 50000.
func TestRepeatedGrantKeepsEachScopesLimit(t *testing.T) {
	amount := mandate.Quantity{Kind: mandate.KindSum, Parameter: "amount_cents"}
	paid := mandate.Grant{Action: "REFUND_INVOICE", EntityStates: []string{"PAID"},
		PerAction: []mandate.PerActionLimit{{Quantity: amount, Max: 10000}}}
	disputed := mandate.Grant{Action: "REFUND_INVOICE", EntityStates: []string{"DISPUTED"},
		PerAction: []mandate.PerActionLimit{{Quantity: amount, Max: 50000}}}
	for _, grants := range [][]mandate.Grant{{paid, disputed}, {disputed, paid}} {
		m := spendMandate(500000)
		m.Grants = grants
		if d := evaluate(t, []mandate.Mandate{m}, refund(20000), 0); d.Outcome != mandate.OutcomePerActionExceeded || d.Limit != 10000 {
			t.Errorf("PAID 20000 must stay refused under PAID ≤ 10000: %+v", d)
		}
		inDispute := req("agent://refund-agent", "REFUND_INVOICE", "DISPUTED", map[string]int64{"amount_cents": 20000})
		if d := evaluate(t, []mandate.Mandate{m}, inDispute, 0); d.Outcome != mandate.OutcomeWithinLimit {
			t.Errorf("DISPUTED 20000 is within DISPUTED ≤ 50000: %+v", d)
		}
	}
}
