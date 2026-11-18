package billing_test

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/alert"
	"github.com/kiff/cloud/api/internal/billing"
	"github.com/kiff/cloud/api/internal/meter"
	"github.com/kiff/cloud/api/internal/tenant"
)

type totalsFake struct{ credits, over int64 }

func (f *totalsFake) RunTotals(context.Context, string, time.Time, time.Time) (meter.RunTotals, error) {
	return meter.RunTotals{Credits: f.credits, Over: f.over}, nil
}

type packsFake struct{ drawn, left int64 }

func (f *packsFake) Credits(context.Context, string, time.Time, time.Time) (int64, int64, error) {
	return f.drawn, f.left, nil
}

type alertsFake struct {
	mu   sync.Mutex
	seen []alert.Event
}

func (a *alertsFake) Notify(ev alert.Event) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.seen = append(a.seen, ev)
}

func (a *alertsFake) told() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []string
	for _, ev := range a.seen {
		if ev.Outcome == alert.CreditsWarning {
			out = append(out, slices.Concat([]string{ev.EntityID[:10]}, ev.Reasons)...)
		}
	}
	return out
}

// The warnings go out at 80% and at 100% of what the period has, once each
// a period: on Solo of the included credits and the packs, on Company of
// the spend limit beyond the included credits. A plan with no limit, or no
// plan, is never warned.
func TestCreditWarnings(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	store := tenant.NewMemoryStore()
	mk := func(meta map[string]any) tenant.Tenant {
		id := tenant.NewID()
		tt := tenant.Tenant{ID: id, Slug: id, Status: tenant.StatusActive, Metadata: meta}
		if err := store.Create(ctx, tt); err != nil {
			t.Fatal(err)
		}
		return tt
	}
	check := func(w *billing.CreditWarnings, tt tenant.Tenant, at time.Time) {
		t.Helper()
		got, err := store.Get(ctx, tt.ID)
		if err != nil {
			t.Fatal(err)
		}
		if err := w.Check(ctx, got, at); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("solo", func(t *testing.T) {
		solo := mk(map[string]any{tenant.MetadataKeyPlan: "solo"})
		totals, packs, alerts := &totalsFake{}, &packsFake{left: 100}, &alertsFake{}
		w := billing.NewCreditWarnings(store, totals, packs, alerts)
		has := int64(meter.SoloIncludedCredits + 100)
		totals.credits = has * 79 / 100
		check(w, solo, now)
		if got := alerts.told(); len(got) != 0 {
			t.Fatalf("below 80%%: %v", got)
		}
		totals.credits = has * 85 / 100
		check(w, solo, now)
		check(w, solo, now)
		if got := alerts.told(); !slices.Equal(got, []string{"2026-11-01", "80"}) {
			t.Fatalf("at 85%%, twice: %v", got)
		}
		totals.credits = has
		check(w, solo, now)
		check(w, solo, now)
		if got := alerts.told(); !slices.Equal(got, []string{"2026-11-01", "80", "2026-11-01", "100", meter.WhyUsedUp}) {
			t.Fatalf("all of it, twice: %v", got)
		}
		// A new period warns again.
		totals.credits = has * 90 / 100
		check(w, solo, now.AddDate(0, 1, 0))
		if got := alerts.told(); len(got) != 7 || got[5] != "2026-12-01" || got[6] != "80" {
			t.Fatalf("the next period: %v", got)
		}
	})
	t.Run("company", func(t *testing.T) {
		team := mk(map[string]any{tenant.MetadataKeyPlan: "team", tenant.MetadataKeySeats: float64(2)})
		included := int64(2 * meter.CompanyCreditsPerSeat)
		totals, alerts := &totalsFake{}, &alertsFake{}
		w := billing.NewCreditWarnings(store, totals, nil, alerts)
		// Using the included credits warns nobody.
		totals.credits = included
		check(w, team, now)
		totals.credits = included + meter.SpendLimitStepCents*80/100/meter.CentsPerCredit
		check(w, team, now)
		totals.credits = included + meter.SpendLimitStepCents/meter.CentsPerCredit
		check(w, team, now)
		if got := alerts.told(); !slices.Equal(got, []string{"2026-11-01", "80", "2026-11-01", "100", meter.WhyLimit}) {
			t.Fatalf("the spend limit: %v", got)
		}
	})
	t.Run("no limit, or no plan", func(t *testing.T) {
		totals, alerts := &totalsFake{credits: 1_000_000}, &alertsFake{}
		w := billing.NewCreditWarnings(store, totals, nil, alerts)
		for _, meta := range []map[string]any{{tenant.MetadataKeyPlan: "internal"}, {tenant.MetadataKeyPlan: "company"}, {}} {
			check(w, mk(meta), now)
		}
		if got := alerts.told(); len(got) != 0 {
			t.Fatalf("warned: %v", got)
		}
	})
}

// The reporter looks at the warnings of each account whose runs it settled.
func TestOverageWarnsAfterSettling(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	store := tenant.NewMemoryStore()
	id := tenant.NewID()
	if err := store.Create(ctx, tenant.Tenant{ID: id, Slug: "team", Status: tenant.StatusActive,
		Metadata: map[string]any{tenant.MetadataKeyPlan: "team", tenant.MetadataKeySeats: float64(2)}}); err != nil {
		t.Fatal(err)
	}
	runs := &runLedger{runs: []ledgerRun{{tenant: id, id: "r", ended: now.Add(-time.Hour), credits: 5}}}
	totals := &totalsFake{credits: 2*meter.CompanyCreditsPerSeat + meter.SpendLimitStepCents/meter.CentsPerCredit}
	alerts := &alertsFake{}
	r := billing.NewOverageReporter(&ledger{}, store, &sender{}, nil).WithRuns(runs, nil).
		WithWarnings(billing.NewCreditWarnings(store, totals, nil, alerts)).WithClock(func() time.Time { return now })
	if err := r.Once(ctx); err != nil {
		t.Fatal(err)
	}
	if got := alerts.told(); !slices.Equal(got, []string{"2026-11-01", "100", meter.WhyLimit}) {
		t.Fatalf("told %v", got)
	}
}
