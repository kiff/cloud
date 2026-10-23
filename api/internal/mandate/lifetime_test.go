package mandate_test

import (
	"context"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/mandate"
)

// lifetimeCard is spendMandate counted over the Card's whole life:
// "EUR 200 for this job until Friday".
func lifetimeCard(limit int64) mandate.Mandate {
	m := spendMandate(limit)
	m.Aggregates[0].Window = mandate.WindowLifetime
	m.ValidUntil = base.Add(5 * 24 * time.Hour)
	return m
}

// Invariant: a lifetime total never resets. Calls across days draw on one
// total, so a daily-looking budget cannot be spent once per day.
// Category: authority
// Attack: spend the Card's total, wait for midnight (or 24 hours), spend it again
// Refused: the next day's call is refused while the lifetime total is spent
func TestLifetimeTotalNeverResets(t *testing.T) {
	r := newRig(t, lifetimeCard(20000))
	if d := r.decide("p1", 8000); !d.Allowed() {
		t.Fatalf("first call: %+v", d)
	}
	r.clock = base.Add(25 * time.Hour)
	if d := r.decide("p2", 8000); !d.Allowed() {
		t.Fatalf("second day: %+v", d)
	}
	r.clock = base.Add(49 * time.Hour)
	d := r.decide("p3", 8000)
	if d.Outcome != mandate.OutcomeLimitReached || d.Used != 16000 || d.Limit != 20000 {
		t.Fatalf("third day must see both earlier draws: %+v", d)
	}
	if d := r.decide("p4", 4000); !d.Allowed() {
		t.Fatalf("what is left of the total stays usable: %+v", d)
	}
	draws, err := r.store.Draws(context.Background(), tenantID, "mdt-1", time.Time{})
	if err != nil || len(draws) != 3 {
		t.Fatalf("draws %v %v", draws, err)
	}
	for _, d := range draws {
		if len(d.ChargedLimits) != 1 || d.ChargedLimits[0].Window != mandate.WindowLifetime || d.ChargedLimits[0].WindowEnd.Year() != 9999 {
			t.Fatalf("a lifetime charge never ends: %+v", d.ChargedLimits)
		}
	}
}

// Invariant: reissuing a Card cannot reset its lifetime total, not by
// moving ValidFrom later and not by switching a daily total to a lifetime
// one: every draw the Card has made counts.
// Category: authority
// Attack: spend most of the room, then reissue the Card with a later ValidFrom (or a new window) to get it back
// Refused: the reissued Card still counts the earlier draws
func TestLifetimeTotalSurvivesReissue(t *testing.T) {
	daily := spendMandate(20000)
	r := newRig(t, daily)
	if d := r.decide("p1", 15000); !d.Allowed() {
		t.Fatalf("first call: %+v", d)
	}
	r.clock = base.Add(26 * time.Hour)
	again := lifetimeCard(20000)
	again.ValidFrom = r.clock
	if err := r.store.Issue(context.Background(), again, "owner@x"); err != nil {
		t.Fatal(err)
	}
	r.clock = base.Add(27 * time.Hour)
	if d := r.decide("p2", 8000); d.Outcome != mandate.OutcomeLimitReached || d.Used != 15000 {
		t.Fatalf("the reissued lifetime total must count the earlier draw: %+v", d)
	}
}

// A lifetime total's forecast uses the authoritative balance: bounded
// history (the last 25 hours) cannot rebuild it, and must not make an old,
// spent total read as unused or unavailable.
func TestLifetimeForecastReadsTheWholeLedger(t *testing.T) {
	r := newRig(t, lifetimeCard(20000))
	if d := r.decide("p1", 20000); !d.Allowed() {
		t.Fatalf("first call: %+v", d)
	}
	r.clock = base.Add(72 * time.Hour)
	ctx := context.Background()
	ms, err := r.store.List(ctx, tenantID)
	if err != nil || len(ms) != 1 {
		t.Fatalf("cards %v %v", ms, err)
	}
	m := ms[0]
	a := m.Aggregates[0]
	b, err := mandate.ReadLimitBalance(ctx, r.store, tenantID, m, a, r.clock, "")
	if err != nil || b.Net != 20000 {
		t.Fatalf("balance %+v %v", b, err)
	}
	h, err := r.store.WarningHistory(ctx, tenantID, m.ID, r.clock.Add(-25*time.Hour), r.clock)
	if err != nil {
		t.Fatal(err)
	}
	if f := mandate.Project(h, m, a, r.clock, b); f.State != "spent" || f.ResetsAt != nil {
		t.Fatalf("forecast %+v", f)
	}
}
