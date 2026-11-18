package billing_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/billing"
	"github.com/kiff/cloud/api/internal/meter"
	"github.com/kiff/cloud/api/internal/packs"
	"github.com/kiff/cloud/api/internal/tenant"
)

// ledger is the meter's counted calls, in memory.
type ledger struct {
	calls []ledgerCall
}

type ledgerCall struct {
	tenant, key string
	unit        meter.Unit
	at          time.Time
	settled     bool
}

func (l *ledger) add(tenantID string, unit meter.Unit, n int, at time.Time) {
	for i := 0; i < n; i++ {
		l.calls = append(l.calls, ledgerCall{tenant: tenantID, key: fmt.Sprintf("%s-%s-%03d", tenantID[:4], unit, len(l.calls)), unit: unit, at: at.Add(time.Duration(i) * time.Second)})
	}
}

func (l *ledger) Pending(context.Context) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	for _, c := range l.calls {
		if !c.settled && !seen[c.tenant] {
			seen[c.tenant] = true
			out = append(out, c.tenant)
		}
	}
	return out, nil
}

func (l *ledger) Unsettled(_ context.Context, tenantID string, unit meter.Unit, from, to time.Time) ([]meter.Placed, error) {
	var in []ledgerCall
	for _, c := range l.calls {
		if c.tenant == tenantID && c.unit == unit && !c.at.Before(from) && c.at.Before(to) {
			in = append(in, c)
		}
	}
	sort.Slice(in, func(i, j int) bool { return in[i].at.Before(in[j].at) })
	var out []meter.Placed
	for i, c := range in {
		if !c.settled {
			out = append(out, meter.Placed{Key: c.key, At: c.at, Place: i + 1})
		}
	}
	return out, nil
}

func (l *ledger) Settle(_ context.Context, tenantID string, keys []string) error {
	for _, k := range keys {
		for i := range l.calls {
			if l.calls[i].tenant == tenantID && l.calls[i].key == k {
				l.calls[i].settled = true
			}
		}
	}
	return nil
}

func (l *ledger) SettleBefore(_ context.Context, tenantID string, t time.Time) error {
	for i := range l.calls {
		if l.calls[i].tenant == tenantID && l.calls[i].at.Before(t) {
			l.calls[i].settled = true
		}
	}
	return nil
}

// sender records the meter events Stripe was sent.
type sender struct {
	sent []string
	fail bool
}

func (s *sender) Send(_ context.Context, customer, identifier string, value int, _ time.Time) error {
	if s.fail {
		return errors.New("stripe down")
	}
	s.sent = append(s.sent, customer+"|"+identifier+"|"+fmt.Sprint(value))
	return nil
}

// runLedger is the run ledger's unsettled runs, in memory.
type runLedger struct {
	runs []ledgerRun
}

type ledgerRun struct {
	tenant, id string
	ended      time.Time
	credits    int64
	settled    bool
}

func (l *runLedger) PendingRunTenants(context.Context) ([]string, error) {
	var out []string
	for _, r := range l.runs {
		if !r.settled && !slices.Contains(out, r.tenant) {
			out = append(out, r.tenant)
		}
	}
	return out, nil
}

func (l *runLedger) UnsettledRuns(_ context.Context, tenantID string, from, to time.Time) ([]meter.UnsettledRun, error) {
	var out []meter.UnsettledRun
	for _, r := range l.runs {
		if r.tenant == tenantID && !r.settled && !r.ended.Before(from) && r.ended.Before(to) {
			out = append(out, meter.UnsettledRun{Run: r.id, Ended: r.ended, Credits: r.credits})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Ended.Before(out[j].Ended) })
	return out, nil
}

func (l *runLedger) SettleRunsBefore(_ context.Context, tenantID string, t time.Time) error {
	for i := range l.runs {
		if l.runs[i].tenant == tenantID && l.runs[i].ended.Before(t) {
			l.runs[i].settled = true
		}
	}
	return nil
}

func (l *runLedger) RunTotals(_ context.Context, tenantID string, from, to time.Time) (meter.RunTotals, error) {
	var t meter.RunTotals
	for _, r := range l.runs {
		if r.tenant == tenantID && !r.ended.Before(from) && r.ended.Before(to) {
			t.Credits += r.credits
			t.Runs++
		}
	}
	return t, nil
}

func (l *runLedger) SettleRuns(_ context.Context, tenantID string, runs []string) error {
	for i := range l.runs {
		if l.runs[i].tenant == tenantID && slices.Contains(runs, l.runs[i].id) {
			l.runs[i].settled = true
		}
	}
	return nil
}

// settler records the runs settled against Solo's balance, in order.
type settler struct {
	l       *runLedger
	settled []string
	fail    bool
}

func (s *settler) SettleRun(_ context.Context, tenantID, run string, from, to time.Time, included int64) (packs.Settled, error) {
	if s.fail {
		return packs.Settled{}, errors.New("database down")
	}
	for i := range s.l.runs {
		if s.l.runs[i].tenant == tenantID && s.l.runs[i].id == run {
			s.l.runs[i].settled = true
		}
	}
	s.settled = append(s.settled, fmt.Sprintf("%s %s..%s %d", run, from.Format("01-02"), to.Format("01-02"), included))
	return packs.Settled{Included: included}, nil
}

// Only actions past the period's included ones reach Stripe, once each;
// tasks are counted no more, and are settled with nothing drawn or sent.
// Plans that aren't billed send nothing, and neither does Solo, which has
// no KIFF actions.
func TestOverageReportsOnlyWhatsPastIncluded(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	store := tenant.NewMemoryStore()
	mk := func(slug string, meta map[string]any) string {
		id := tenant.NewID()
		if err := store.Create(ctx, tenant.Tenant{ID: id, Slug: slug, Status: tenant.StatusActive, Metadata: meta}); err != nil {
			t.Fatal(err)
		}
		return id
	}
	company := mk("company", map[string]any{tenant.MetadataKeyPlan: "team", tenant.MetadataKeySeats: float64(1), billing.MetaStripeCustomerID: "cus_company"})
	solo := mk("solo", map[string]any{tenant.MetadataKeyPlan: "solo", billing.MetaStripeCustomerID: "cus_solo"})
	trial := mk("trial", map[string]any{tenant.MetadataKeyPlan: "trial"})
	internal := mk("internal", map[string]any{tenant.MetadataKeyPlan: "internal"})
	l := &ledger{}
	at := now.Add(-time.Hour)
	l.add(company, meter.UnitTask, 23, at)    // settled, nothing drawn
	l.add(company, meter.UnitAction, 501, at) // 500 a seat included, 1 over
	l.add(solo, meter.UnitAction, 600, at)    // Solo has no actions: never billed
	l.add(trial, meter.UnitTask, 26, at)      // settled, nothing drawn
	l.add(trial, meter.UnitAction, 600, at)   // never billed
	l.add(internal, meter.UnitTask, 99, at)   // no limit
	actions := &sender{}
	r := billing.NewOverageReporter(l, store, actions, nil).WithClock(func() time.Time { return now })

	// Stripe is down: no action past included is settled, so it's sent later.
	actions.fail = true
	if err := r.Once(ctx); err == nil {
		t.Fatal("a failed send was not reported")
	}
	actions.fail = false
	if err := r.Once(ctx); err != nil {
		t.Fatal(err)
	}
	if len(actions.sent) != 1 || actions.sent[0] != "cus_company|"+company[:4]+"-action-523|1" {
		t.Fatalf("sent %v, want the 501st action only", actions.sent)
	}
	// Everything is settled; a second run sends nothing more.
	if pending, _ := l.Pending(ctx); len(pending) != 0 {
		t.Fatalf("still pending: %v", pending)
	}
	if err := r.Once(ctx); err != nil || len(actions.sent) != 1 {
		t.Fatalf("a second run did more: %v %v", err, actions.sent)
	}
}

// Solo's runs are settled against its balance one at a time, oldest
// first, in the current period and the one before, each with the plan's
// included credits; older ones, and those of a plan with no balance, are
// settled taking nothing. A settlement that fails keeps the account for the
// next run. (Company's are TestOverageBillsCompanysCreditsPastIncluded.)
func TestOverageSettlesRuns(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	store := tenant.NewMemoryStore()
	mk := func(slug string, meta map[string]any) string {
		id := tenant.NewID()
		if err := store.Create(ctx, tenant.Tenant{ID: id, Slug: slug, Status: tenant.StatusActive, Metadata: meta}); err != nil {
			t.Fatal(err)
		}
		return id
	}
	solo := mk("solo", map[string]any{tenant.MetadataKeyPlan: "solo"})
	none := mk("none", map[string]any{})
	runs := &runLedger{runs: []ledgerRun{
		{tenant: solo, id: "nov-b", ended: now.Add(-time.Hour), credits: 5},
		{tenant: solo, id: "nov-a", ended: now.AddDate(0, 0, -20), credits: 7},
		{tenant: solo, id: "oct", ended: now.AddDate(0, -1, 0), credits: 3},
		{tenant: solo, id: "sep", ended: now.AddDate(0, -2, 0), credits: 9},
		{tenant: none, id: "n", ended: now.Add(-time.Hour), credits: 1},
	}}
	settle := &settler{l: runs, fail: true}
	r := billing.NewOverageReporter(&ledger{}, store, &sender{}, nil).WithRuns(runs, settle).WithClock(func() time.Time { return now })
	if err := r.Once(ctx); err == nil {
		t.Fatal("a failed settlement was not reported")
	}
	if pending, _ := runs.PendingRunTenants(ctx); !slices.Equal(pending, []string{solo}) {
		t.Fatalf("pending %v: want Solo kept, the others settled", pending)
	}
	settle.fail = false
	if err := r.Once(ctx); err != nil {
		t.Fatal(err)
	}
	want := []string{"oct 10-01..11-01 500", "nov-a 11-01..12-01 500", "nov-b 11-01..12-01 500"}
	if !slices.Equal(settle.settled, want) {
		t.Fatalf("settled %v, want %v", settle.settled, want)
	}
	if pending, _ := runs.PendingRunTenants(ctx); len(pending) != 0 {
		t.Fatalf("still pending: %v", pending)
	}
}

// A paid plan with no Stripe customer is a data problem: its action
// overage isn't dropped, it stays pending and is sent once the customer is
// linked. Its tasks are settled meanwhile: they need no Stripe.
func TestOverageKeepsAPaidPlanWithoutACustomerPending(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	store := tenant.NewMemoryStore()
	id := tenant.NewID()
	if err := store.Create(ctx, tenant.Tenant{ID: id, Slug: "nocus", Status: tenant.StatusActive,
		Metadata: map[string]any{tenant.MetadataKeyPlan: "team"}}); err != nil {
		t.Fatal(err)
	}
	l := &ledger{}
	l.add(id, meter.UnitAction, 501, now.Add(-time.Hour)) // 1 over
	l.add(id, meter.UnitTask, 21, now.Add(-time.Hour))    // settled, nothing drawn
	actions := &sender{}
	r := billing.NewOverageReporter(l, store, actions, nil).WithClock(func() time.Time { return now })

	if err := r.Once(ctx); err != nil {
		t.Fatal(err)
	}
	if pending, _ := l.Pending(ctx); len(pending) != 1 || len(actions.sent) != 0 {
		t.Fatalf("pending %v, sent %v: want actions kept, nothing sent", pending, actions.sent)
	}
	if err := store.SetMetadata(ctx, id, billing.MetaStripeCustomerID, "cus_late"); err != nil {
		t.Fatal(err)
	}
	if err := r.Once(ctx); err != nil || len(actions.sent) != 1 || actions.sent[0] != "cus_late|"+id[:4]+"-action-500|1" {
		t.Fatalf("once linked: %v %v", err, actions.sent)
	}
}

// Company's runs use the period's included credits (seats × 500, pooled)
// oldest first, after the runs already settled; what's past them is one
// meter event per run, keyed by the run, sent to the account's Stripe
// customer, and the run is settled only after Stripe took it. A run of the
// period before is billed against that period's credits; older ones are
// settled taking nothing. A send that fails keeps the run for the next run,
// which sends it once. Solo's runs never reach the credit meter, and a
// Company with no Stripe customer, or with no sender, keeps its runs.
func TestOverageBillsCompanysCreditsPastIncluded(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	store := tenant.NewMemoryStore()
	mk := func(slug string, meta map[string]any) string {
		id := tenant.NewID()
		if err := store.Create(ctx, tenant.Tenant{ID: id, Slug: slug, Status: tenant.StatusActive, Metadata: meta}); err != nil {
			t.Fatal(err)
		}
		return id
	}
	company := mk("company", map[string]any{tenant.MetadataKeyPlan: "team", tenant.MetadataKeySeats: float64(2), billing.MetaStripeCustomerID: "cus_co"})
	nocus := mk("nocus", map[string]any{tenant.MetadataKeyPlan: "team", tenant.MetadataKeySeats: float64(1)})
	solo := mk("solo", map[string]any{tenant.MetadataKeyPlan: "solo", billing.MetaStripeCustomerID: "cus_solo"})
	day := func(month time.Month, d int) time.Time { return time.Date(2026, month, d, 9, 0, 0, 0, time.UTC) }
	runs := &runLedger{runs: []ledgerRun{
		// September: too old to bill, settled taking nothing.
		{tenant: company, id: "sep", ended: day(9, 20), credits: 5000},
		// October, 1,000 included: 900, then 300 of which 200 are past.
		{tenant: company, id: "oct-1", ended: day(10, 3), credits: 900},
		{tenant: company, id: "oct-2", ended: day(10, 9), credits: 300},
		// November: 800 already settled, then 150 (within), 100 (50 past)
		// and 30 (all past).
		{tenant: company, id: "nov-0", ended: day(11, 2), credits: 800, settled: true},
		{tenant: company, id: "nov-1", ended: day(11, 5), credits: 150},
		{tenant: company, id: "nov-2", ended: day(11, 8), credits: 100},
		{tenant: company, id: "nov-3", ended: day(11, 9), credits: 30},
		{tenant: nocus, id: "nc", ended: day(11, 9), credits: 900},
		{tenant: solo, id: "solo", ended: day(11, 9), credits: 900},
	}}
	settle := &settler{l: runs}
	credits := &sender{fail: true}
	r := billing.NewOverageReporter(&ledger{}, store, &sender{}, nil).WithRuns(runs, settle).WithCredits(credits).
		WithClock(func() time.Time { return now })

	// Stripe down: nothing past the included is settled unsent.
	if err := r.Once(ctx); err == nil {
		t.Fatal("a failed send was not reported")
	}
	unsettled := func() []string {
		var out []string
		for _, u := range runs.runs {
			if !u.settled {
				out = append(out, u.id)
			}
		}
		return out
	}
	if got, want := unsettled(), []string{"oct-2", "nov-1", "nov-2", "nov-3", "nc"}; !slices.Equal(got, want) {
		t.Fatalf("unsettled with Stripe down: %v, want %v", got, want)
	}
	credits.fail = false
	if err := r.Once(ctx); err != nil {
		t.Fatal(err)
	}
	want := []string{"cus_co|credit-oct-2|200", "cus_co|credit-nov-2|50", "cus_co|credit-nov-3|30"}
	if !slices.Equal(credits.sent, want) {
		t.Fatalf("sent %v, want %v", credits.sent, want)
	}
	// Sent once: a second run sends nothing more.
	if err := r.Once(ctx); err != nil || len(credits.sent) != 3 {
		t.Fatalf("a second run: %v, sent %v", err, credits.sent)
	}
	// The Company with no customer keeps its run; Solo's went to its packs.
	if got := unsettled(); !slices.Equal(got, []string{"nc"}) {
		t.Fatalf("unsettled: %v, want only the Company with no customer", got)
	}
	if !slices.Equal(settle.settled, []string{"solo 11-01..12-01 500"}) {
		t.Fatalf("Solo settled %v", settle.settled)
	}

	// No sender (an API without Stripe): Company's runs wait.
	waiting := mk("waiting", map[string]any{tenant.MetadataKeyPlan: "team", billing.MetaStripeCustomerID: "cus_w"})
	runs.runs = append(runs.runs, ledgerRun{tenant: waiting, id: "w", ended: day(11, 10), credits: 900})
	if err := billing.NewOverageReporter(&ledger{}, store, nil, nil).WithRuns(runs, settle).
		WithClock(func() time.Time { return now }).Once(ctx); err != nil {
		t.Fatal(err)
	}
	if got := unsettled(); !slices.Contains(got, "w") {
		t.Fatalf("with no sender, Company's run was settled: %v", got)
	}
}
