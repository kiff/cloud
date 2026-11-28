package mandate_test

import (
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/mandate"
)

// K4's universal classes: a Card signed before the account's switch keeps
// opening a tool that "unknown is closed" alone raised from write to
// irreversible, and nothing else.
// Category: authority
// Attack: a Card signed before the switch is used to open a money, delete or otherwise-raised tool, or keeps the carry-over after it's revised
// Refused: category_closed, unless the call is irreversible by "unknown" alone, the Card's terms were signed before the switch, and it grants the action
func TestK4CarriesOverOnlyUnknownToolsOnCardsSignedBeforeTheSwitch(t *testing.T) {
	switchAt := base.Add(-time.Hour)
	m := spendMandate(500000)
	m.RevisedAt = switchAt.Add(-time.Hour)
	unknown := func(class, by string, since time.Time) mandate.Request {
		r := k4Req(class, nil)
		r.ClassBy, r.UniversalSince = by, since
		return r
	}
	if d, _ := mandate.Evaluate([]mandate.Mandate{m}, unknown("irreversible", mandate.ClassByUnknown, switchAt), used(0), base); !d.Allowed() {
		t.Errorf("an unknown tool on a Card signed before the switch: %+v", d)
	}
	for name, tc := range map[string]struct {
		req mandate.Request
		m   mandate.Mandate
	}{
		"raised by another signal":           {unknown("irreversible", "", switchAt), m},
		"money":                              {unknown("money", mandate.ClassByUnknown, switchAt), m},
		"delete":                             {unknown("delete", mandate.ClassByUnknown, switchAt), m},
		"an account with no switch time":     {unknown("irreversible", mandate.ClassByUnknown, time.Time{}), m},
		"a Card revised after the switch":    {unknown("irreversible", mandate.ClassByUnknown, switchAt), func() mandate.Mandate { c := m; c.RevisedAt = switchAt.Add(time.Minute); return c }()},
		"a Card revised at the switch":       {unknown("irreversible", mandate.ClassByUnknown, switchAt), func() mandate.Mandate { c := m; c.RevisedAt = switchAt; return c }()},
		"a Card with no revision time known": {unknown("irreversible", mandate.ClassByUnknown, switchAt), func() mandate.Mandate { c := m; c.RevisedAt = time.Time{}; return c }()},
	} {
		if d, _ := mandate.Evaluate([]mandate.Mandate{tc.m}, tc.req, used(0), base); d.Outcome != mandate.OutcomeCategoryClosed {
			t.Errorf("%s: %+v", name, d)
		}
	}
	// Every covering Card must open it: one revised after the switch closes it.
	later := spendMandate(500000)
	later.ID, later.RevisedAt = "card_later", switchAt.Add(time.Minute)
	if d, _ := mandate.Evaluate([]mandate.Mandate{m, later}, unknown("irreversible", mandate.ClassByUnknown, switchAt), used(0), base); d.Outcome != mandate.OutcomeCategoryClosed || d.MandateID != later.ID {
		t.Errorf("two Cards, one revised after: %+v", d)
	}
	// A call the gateway sent as irreversible for another reason is never carried over by the action alone.
	if mandate.CarriedOver(m, k4Req("irreversible", nil)) {
		t.Error("carried over with no ClassBy")
	}
}

// The memory store stamps a Card's revision time on each issue, as the
// Postgres store reads it from the latest revision.
func TestARevisionStampsTheCardsRevisionTime(t *testing.T) {
	s := mandate.NewMemoryStore()
	m := spendMandate(500000)
	if err := s.Issue(t.Context(), m, "owner"); err != nil {
		t.Fatal(err)
	}
	got, err := s.List(t.Context(), m.TenantID)
	if err != nil || len(got) != 1 || got[0].RevisedAt.IsZero() {
		t.Fatalf("%+v %v", got, err)
	}
}
