package mandate

import (
	"fmt"
	"testing"
	"time"
)

func warningFixture(w Window, limit int64) (WarningHistory, Mandate, Aggregate, time.Time) {
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	a := Aggregate{Quantity: Quantity{Kind: KindSum, Parameter: "amount"}, Window: w, Limit: limit}
	m := Mandate{ID: "card", TenantID: "tenant", Aggregates: []Aggregate{a}}
	h := WarningHistory{IssuedAt: now.Add(-9 * 24 * time.Hour)}
	for i, ago := range []time.Duration{55 * time.Minute, 35 * time.Minute, 5 * time.Minute} {
		if w == WindowRollingHour {
			ago = []time.Duration{4 * time.Minute, 2 * time.Minute, 0}[i]
		}
		at := now.Add(-ago)
		h.Draws = append(h.Draws, Draw{TenantID: m.TenantID, MandateID: m.ID, Subject: fmt.Sprint(i), ProposalID: fmt.Sprint(i), QuantityKey: a.Quantity.Key(), Amount: 10, DrawnAt: at, ChargedLimits: []ChargedLimit{chargedLimit(a, m, at)}})
	}
	return h, m, a, now
}

func TestForecastCalendarAndInsufficientHistory(t *testing.T) {
	h, m, a, now := warningFixture(WindowCalendarDay, 100)
	b, _ := historicalBalance(h, m, a, now)
	f := Project(h, m, a, now, b)
	if f.State != "estimated" || f.RunsOutAt == nil || f.RunsOutAt.Sub(now) != 140*time.Minute || f.ResetsAt == nil {
		t.Fatalf("%+v", f)
	}
	h.Draws = h.Draws[:1]
	b, _ = historicalBalance(h, m, a, now)
	f = Project(h, m, a, now, b)
	if f.State != "insufficient_history" || f.RunsOutAt != nil {
		t.Fatalf("invented history: %+v", f)
	}
	// A balance from another concurrent snapshot cannot produce a false forecast.
	f = Project(h, m, a, now, LimitBalance{Gross: 999, Net: 999})
	if f.State != "unavailable" {
		t.Fatal(f)
	}
}

func TestForecastRollingAgingAndRoomExpiry(t *testing.T) {
	h, m, a, now := warningFixture(WindowRollingDay, 130)
	at := now.Add(-24*time.Hour + 10*time.Minute)
	h.Draws = append(h.Draws, Draw{TenantID: m.TenantID, MandateID: m.ID, ProposalID: "old", QuantityKey: a.Quantity.Key(), Amount: 70, DrawnAt: at, ChargedLimits: []ChargedLimit{chargedLimit(a, m, at)}})
	b, _ := historicalBalance(h, m, a, now)
	f := Project(h, m, a, now, b)
	if f.ResetsAt != nil || f.NextCapacityAt == nil || f.RunsOutAt == nil || f.RunsOutAt.Sub(now) < 3*time.Hour {
		t.Fatalf("ignored rolling replenishment: %+v", f)
	}
	a.Limit = 60
	a.ExtraRoom = []RoomGrant{{ID: "room", Amount: 100, At: now.Add(-time.Hour), ExpiresAt: now.Add(10 * time.Minute), Measure: a.EffectiveMeasure(), Window: a.Window, Location: m.Loc().String()}}
	f = Project(h, m, a, now, b)
	if f.RunsOutAt == nil || !f.RunsOutAt.Equal(now.Add(10*time.Minute)) {
		t.Fatalf("ignored fixed room expiry: %+v", f)
	}
}

func TestForecastEligibleReleaseAndUnknownOutcome(t *testing.T) {
	h, m, a, now := warningFixture(WindowCalendarDay, 100)
	d := h.Draws[0]
	h.Releases = []EffectRelease{{TenantID: m.TenantID, MandateID: m.ID, ProposalID: d.ProposalID, QuantityKey: d.QuantityKey, Amount: d.Amount, At: now.Add(-time.Minute), ChargedLimits: d.ChargedLimits}}
	b, _ := historicalBalance(h, m, a, now)
	if b.Net != 20 {
		t.Fatal(b)
	}
	f := Project(h, m, a, now, b)
	if f.Rate != 20.0/3600 {
		t.Fatal(f)
	}
	h.Releases[0].ChargedLimits = []ChargedLimit{{Measure: MeasureAttempts, Window: a.Window, Location: m.Loc().String(), WindowEnd: now.Add(time.Hour)}}
	b, _ = historicalBalance(h, m, a, now)
	if b.Net != 30 {
		t.Fatalf("released original-ineligible/unknown draw: %+v", b)
	}
}

func TestForecastCalendarDSTAndResetBeforeExhaustion(t *testing.T) {
	loc, _ := time.LoadLocation("Europe/Paris")
	for _, test := range []struct {
		month time.Month
		day   int
		hours int
	}{{time.March, 29, 23}, {time.October, 25, 25}} {
		h, m, a, _ := warningFixture(WindowCalendarDay, 100000)
		m.Location = loc.String()
		now := time.Date(2026, test.month, test.day, 23, 45, 0, 0, loc)
		h.IssuedAt = now.Add(-9 * 24 * time.Hour)
		for i := range h.Draws {
			h.Draws[i].DrawnAt = now.Add(-time.Duration(55-i*20) * time.Minute)
			h.Draws[i].ChargedLimits = []ChargedLimit{chargedLimit(a, m, h.Draws[i].DrawnAt)}
		}
		b, _ := historicalBalance(h, m, a, now)
		f := Project(h, m, a, now, b)
		start, _ := a.Window.Start(now, loc)
		if f.State != "no_projected_exhaustion" || f.RunsOutAt != nil || f.ResetsAt == nil || f.ResetsAt.Sub(start) != time.Duration(test.hours)*time.Hour {
			t.Fatalf("DST/reset: %+v", f)
		}
	}
}

func TestWarningsHourlyRunwayBurstEscalationAndDedup(t *testing.T) {
	h, m, a, now := warningFixture(WindowRollingHour, 100)
	c := WarningConfig{Settings: DefaultWarningSettings()}
	e, w, err := evaluateWarnings(h, m, a, c, warningEpisode{}, now)
	if err != nil || w != nil {
		t.Fatalf("hourly runway reached people: %+v %v", w, err)
	}
	h.IssuedAt = now.Add(-time.Hour)
	a.Limit = 40
	e, w, err = evaluateWarnings(h, m, a, c, e, now)
	if err != nil || w == nil || w.Kind != "burst" {
		t.Fatalf("cold start: %+v %v", w, err)
	}
	e, w, err = evaluateWarnings(h, m, a, c, e, now)
	if err != nil || w != nil {
		t.Fatalf("duplicate: %+v %v", w, err)
	}
	a.Limit = 30
	e, w, err = evaluateWarnings(h, m, a, c, e, now)
	if err != nil || w == nil || w.Kind != "spent" {
		t.Fatalf("spent escalation: %+v %v", w, err)
	}
	_, w, err = evaluateWarnings(h, m, a, c, warningEpisode{}, now)
	if err != nil || w == nil || w.Kind != "spent" {
		t.Fatal("jump did not select spent")
	}
}

func TestWarningsEquivalentSevenDayBaseline(t *testing.T) {
	h, m, a, now := warningFixture(WindowCalendarDay, 1000)
	for day := 1; day <= 7; day++ {
		at := now.AddDate(0, 0, -day).Add(-30 * time.Minute)
		h.Draws = append(h.Draws, Draw{TenantID: m.TenantID, MandateID: m.ID, ProposalID: fmt.Sprintf("baseline-%d", day), QuantityKey: a.Quantity.Key(), Amount: 10, DrawnAt: at, ChargedLimits: []ChargedLimit{chargedLimit(a, m, at)}})
	}
	ratio, ok := unusualRatio(h, m, a, now)
	if !ok || ratio != 3 {
		t.Fatalf("ratio %v known %v", ratio, ok)
	}
	_, w, err := evaluateWarnings(h, m, a, WarningConfig{Settings: DefaultWarningSettings()}, warningEpisode{}, now)
	if err != nil || w == nil || w.Kind != "unusual" {
		t.Fatalf("%+v %v", w, err)
	}
	h.IssuedAt = now.Add(-6 * 24 * time.Hour)
	_, ok = unusualRatio(h, m, a, now)
	if ok {
		t.Fatal("invented baseline")
	}
}

func TestWarningRecoveryStartsANewRollingBurstObservation(t *testing.T) {
	h, m, a, now := warningFixture(WindowRollingHour, 30)
	h.IssuedAt = now.Add(-time.Hour)
	c := WarningConfig{Settings: DefaultWarningSettings()}
	e, w, err := evaluateWarnings(h, m, a, c, warningEpisode{}, now)
	if err != nil || w == nil || w.Kind != "spent" {
		t.Fatalf("initial: %+v %v", w, err)
	}
	at := now.Add(time.Hour + time.Minute)
	e, _, err = evaluateWarnings(h, m, a, c, e, at)
	if err != nil {
		t.Fatal(err)
	}
	at = at.Add(6 * time.Minute)
	e, w, err = evaluateWarnings(h, m, a, c, e, at)
	if err != nil || w != nil || e.Number != 1 || !e.Anchor.IsZero() {
		t.Fatalf("recovery %+v %+v %v", e, w, err)
	}
	at = at.Add(time.Minute)
	h.Draws = append(h.Draws, Draw{TenantID: m.TenantID, MandateID: m.ID, ProposalID: "new-episode", QuantityKey: a.Quantity.Key(), Amount: 20, DrawnAt: at, ChargedLimits: []ChargedLimit{chargedLimit(a, m, at)}})
	_, w, err = evaluateWarnings(h, m, a, c, e, at)
	if err != nil || w == nil || w.Kind != "burst" {
		t.Fatalf("new burst: %+v %v", w, err)
	}
}
