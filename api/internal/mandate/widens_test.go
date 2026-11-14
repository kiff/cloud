package mandate

import (
	"strings"
	"testing"
	"time"
)

func widenBase() Mandate {
	sum := Quantity{Kind: KindSum, Parameter: "amount_cents"}
	return Mandate{ID: "card_1", Subject: "agent_1", Domain: "stripe", Environment: "test", OnExceed: OnExceedRefuse,
		ValidUntil: time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC),
		Grants: []Grant{{Action: "refund", EntityStates: []string{"paid"}, PerAction: []PerActionLimit{{Quantity: sum, Max: 50000}},
			Resources: &ResourceScope{Parameter: "order_id", Values: []string{"ord_1"}}}},
		Aggregates: []Aggregate{{Quantity: sum, Limit: 90000, Window: WindowCalendarDay}}}
}

func kinds(ws []Widening) string {
	var k []string
	for _, w := range ws {
		k = append(k, w.Kind)
	}
	return strings.Join(k, ",")
}

// Every way a revision widens a Card is named; the same Card, or a
// narrower one, widens nothing.
func TestWidens(t *testing.T) {
	cur := widenBase()
	for _, tc := range []struct {
		name   string
		change func(*Mandate)
		want   string
	}{
		{"the same", func(m *Mandate) {}, ""},
		{"a new title, location, hold time", func(m *Mandate) { m.Title, m.Location, m.HoldExpiry = "x", "Europe/Paris", time.Hour }, ""},
		{"issued by another draft", func(m *Mandate) { m.DraftID = "drf_2" }, ""},
		{"a lower limit", func(m *Mandate) { m.Aggregates[0].Limit = 1000 }, ""},
		{"a stricter window", func(m *Mandate) { m.Aggregates[0].Window = WindowLifetime }, ""},
		{"an extra limit", func(m *Mandate) {
			m.Aggregates = append(m.Aggregates, Aggregate{Quantity: Quantity{Kind: KindCount}, Limit: 3, Window: WindowCalendarDay})
		}, ""},
		{"an earlier end", func(m *Mandate) { m.ValidUntil = m.ValidUntil.Add(-24 * time.Hour) }, ""},
		{"owner to refuse is narrower", func(m *Mandate) { m.OnExceed = OnExceedRefuse }, ""},
		{"a removed action", func(m *Mandate) { m.Grants = nil }, ""},
		{"an action added", func(m *Mandate) { m.Grants = append(m.Grants, Grant{Action: "cancel"}) }, "action"},
		{"any state", func(m *Mandate) { m.Grants[0].EntityStates = nil }, "entity_states"},
		{"another state", func(m *Mandate) { m.Grants[0].EntityStates = []string{"paid", "disputed"} }, "entity_states"},
		{"a per-action limit raised", func(m *Mandate) { m.Grants[0].PerAction[0].Max = 60000 }, "per_action"},
		{"a per-action limit removed", func(m *Mandate) { m.Grants[0].PerAction = nil }, "per_action"},
		{"any resource", func(m *Mandate) { m.Grants[0].Resources = nil }, "resources"},
		{"a longer allowlist", func(m *Mandate) {
			m.Grants[0].Resources = &ResourceScope{Parameter: "order_id", Values: []string{"ord_1", "ord_2"}}
		}, "resources"},
		{"a raised total", func(m *Mandate) { m.Aggregates[0].Limit = 90001 }, "aggregate"},
		{"a removed total", func(m *Mandate) { m.Aggregates = nil }, "aggregate"},
		{"a looser window, no number raised", func(m *Mandate) { m.Aggregates[0].Window = WindowRollingHour }, "aggregate"},
		{"another quantity (another currency's field)", func(m *Mandate) {
			m.Aggregates[0].Quantity = Quantity{Kind: KindSum, Parameter: "amount_usd_cents"}
		}, "aggregate"},
		{"a later end", func(m *Mandate) { m.ValidUntil = m.ValidUntil.Add(24 * time.Hour) }, "valid_until"},
		{"no end", func(m *Mandate) { m.ValidUntil = time.Time{} }, "valid_until"},
		{"another holder", func(m *Mandate) { m.Holders = []string{"agent_1", "agent_2"} }, "holders"},
		{"refuse to owner", func(m *Mandate) { m.OnExceed = OnExceedOwner }, "on_exceed"},
		{"another domain", func(m *Mandate) { m.Domain = "adyen" }, "scope"},
		{"a term the rule doesn't name (next steps)", func(m *Mandate) { m.Grants[0].NextSteps = []string{"return_first"} }, "other"},
	} {
		next := widenBase()
		tc.change(&next)
		if got := kinds(Widens(&cur, next)); got != tc.want {
			t.Errorf("%s: widens %q, want %q (%v)", tc.name, got, tc.want, Widens(&cur, next))
		}
	}
	// Every widening says why in words.
	next := widenBase()
	next.Grants = append(next.Grants, Grant{Action: "cancel"})
	if w := Widens(&cur, next); len(w) != 1 || w[0].Words != "Adds the action cancel" {
		t.Errorf("words: %v", w)
	}
	// A new Card has nothing in force to widen.
	if w := Widens(nil, next); w != nil {
		t.Errorf("a new Card: %v", w)
	}
	// Two grants for one action: the widest of each term counts, so a
	// revision that keeps the widest is the same.
	two := widenBase()
	two.Grants = append(two.Grants, Grant{Action: "refund"})
	if w := Widens(&two, widenBase()); len(w) != 0 {
		t.Errorf("narrowing two grants into one: %v", w)
	}
	if w := kinds(Widens(&cur, two)); !strings.Contains(w, "entity_states") || !strings.Contains(w, "per_action") || !strings.Contains(w, "resources") {
		t.Errorf("a second, open grant for the action: %q", w)
	}
}
