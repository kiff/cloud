package meter_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/meter"
)

// A run's credits are its AI cost in euro cents at the fixed rate, times
// the multiplier, rounded up once: a run that cost anything uses one.
func TestCreditsAreARunsAICostRoundedUp(t *testing.T) {
	for _, c := range []struct {
		micro int64
		want  int64
	}{
		{0, 0}, {-5, 0},
		{1, 1},
		{2_700, 1},   // exactly one credit's worth: a quarter of a euro cent
		{2_701, 2},   // a millionth of a dollar over: rounded up
		{10_800, 4},  // a euro cent: four credits
		{30_000, 12}, // a scheduled Refunds run
		{120_000, 45},
		{320_000, 119},
		{10_800_000, 4_000}, // Setup's whole fair use: 10 euros
	} {
		if got := meter.Credits(c.micro); got != c.want {
			t.Errorf("Credits(%d) = %d, want %d", c.micro, got, c.want)
		}
	}
}

// A trial and a run that failed on the runtime's side are free, in that
// order; Setup's runs are free while its period stays within its fair
// use, this run included; everything else counts, a run with refused or
// declined calls too.
func TestHowARunCounts(t *testing.T) {
	base := meter.RunReport{Tenant: "t1", Run: "r1", Agent: "agt", RuntimeAgent: "taf-1", CostMicroUSD: 30_000, Status: "COMPLETED"}
	with := func(f func(*meter.RunReport)) meter.RunReport { r := base; f(&r); return r }
	for _, c := range []struct {
		name    string
		r       meter.RunReport
		spent   int64
		counted bool
		free    string
		credits int64
	}{
		{"a run", base, 0, true, "", 12},
		{"refused and declined calls", with(func(r *meter.RunReport) { r.Refused, r.Declined = 2, 1 }), 0, true, "", 12},
		{"a trial", with(func(r *meter.RunReport) { r.Trigger = "trial" }), 0, false, meter.FreeTrial, 0},
		{"a failed trial", with(func(r *meter.RunReport) { r.Trigger, r.Failed = "trial", true }), 0, false, meter.FreeTrial, 0},
		{"failed on the runtime's side", with(func(r *meter.RunReport) { r.Failed, r.Status = true, "ERROR" }), 0, false, meter.FreeFailed, 0},
		{"Setup within its fair use", with(func(r *meter.RunReport) { r.Setup = true }), 0, false, meter.FreeSetup, 0},
		{"Setup reaching its fair use exactly", with(func(r *meter.RunReport) { r.Setup = true }), meter.SetupAllowanceMicroUSD - 30_000, false, meter.FreeSetup, 0},
		{"Setup past its fair use", with(func(r *meter.RunReport) { r.Setup = true }), meter.SetupAllowanceMicroUSD - 29_999, true, "", 12},
		{"a failed run of Setup's", with(func(r *meter.RunReport) { r.Setup, r.Failed = true, true }), meter.SetupAllowanceMicroUSD, false, meter.FreeFailed, 0},
	} {
		e := meter.ClassifyRun(c.r, c.spent)
		if e.Counted != c.counted || e.Free != c.free || e.Credits != c.credits || e.Run != c.r.Run {
			t.Errorf("%s: counted %v, free %q, credits %d; want %v, %q, %d", c.name, e.Counted, e.Free, e.Credits, c.counted, c.free, c.credits)
		}
	}
}

// The ledger keeps a run's first report, totals a period's runs per agent
// (Setup's apart) with the free ones by why, and lists them newest first,
// page by page, one agent's or a person's own and their managed agents'.
func TestTheRunLedger(t *testing.T) {
	ctx := context.Background()
	pool := fixture(t)
	p := meter.NewPostgres(pool)
	tn := fmt.Sprintf("runs-%d", time.Now().UnixNano())
	if _, err := pool.Exec(ctx, `INSERT INTO cloud_meta.tenants VALUES ($1)`, tn); err != nil {
		t.Fatal(err)
	}
	from := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	to := from.AddDate(0, 1, 0)
	put := func(run, agent, runtime string, ended time.Time, cost int64, f func(*meter.RunReport)) meter.RunEntry {
		t.Helper()
		r := meter.RunReport{Tenant: tn, Run: run, Agent: agent, RuntimeAgent: runtime, Trigger: "schedule", Started: ended.Add(-time.Minute),
			Ended: ended, CostMicroUSD: cost, Turns: 3, Status: "COMPLETED"}
		if f != nil {
			f(&r)
		}
		e, fresh, err := p.KeepRun(ctx, r, from, to)
		if err != nil || !fresh {
			t.Fatalf("keep %s: fresh %v, %v", run, fresh, err)
		}
		return e
	}
	day := func(d int) time.Time { return from.AddDate(0, 0, d).Add(9 * time.Hour) }
	kept := day(30)
	put("r1", "agt-refunds", "taf-refunds", day(1), 30_000, func(r *meter.RunReport) {
		r.RequesterKind, r.AskedBy, r.Refused, r.KeptUntil = "member", "ada", 1, &kept
	})
	put("r2", "agt-refunds", "taf-refunds", day(2), 320_000, nil)
	put("r3", "agt-chargebacks", "taf-chargebacks", day(3), 120_000, nil)
	put("r4", "agt-refunds", "taf-refunds", day(4), 50_000, func(r *meter.RunReport) { r.Trigger = "trial" })
	put("r5", "agt-chargebacks", "taf-chargebacks", day(5), 9_000, func(r *meter.RunReport) { r.Failed, r.Status = true, "ERROR" })
	put("r6", "agt-coach", "taf-coach", day(6), 10_000_000, func(r *meter.RunReport) { r.Setup = true })
	put("r7", "agt-coach", "taf-coach", day(7), 1_000_000, func(r *meter.RunReport) { r.Setup = true }) // past its fair use
	put("r0", "agt-refunds", "taf-refunds", from.Add(-time.Second), 999_000, nil)                       // the period before
	put("r9", "agt-refunds", "taf-refunds", to, 999_000, nil)                                           // the next one

	// A run sent again keeps its first report.
	again := meter.RunReport{Tenant: tn, Run: "r1", Agent: "agt-refunds", RuntimeAgent: "taf-refunds", Started: day(1),
		Ended: day(1), CostMicroUSD: 1, Status: "COMPLETED"}
	if e, fresh, err := p.KeepRun(ctx, again, from, to); err != nil || fresh || e.CostMicroUSD != 30_000 || e.Credits != 12 || e.AskedBy != "ada" ||
		e.Refused != 1 || e.KeptUntil == nil || !e.KeptUntil.Equal(kept) {
		t.Fatalf("sent again: %+v, fresh %v, %v", e, fresh, err)
	}

	tot, err := p.RunTotals(ctx, tn, from, to)
	if err != nil {
		t.Fatal(err)
	}
	coach := meter.Credits(1_000_000)
	if tot.Credits != 12+119+45+coach || tot.Runs != 4 || tot.Setup.Runs != 1 || tot.Setup.Credits != coach ||
		tot.Free[meter.FreeSetup] != 1 || tot.Free[meter.FreeTrial] != 1 || tot.Free[meter.FreeFailed] != 1 || len(tot.Free) != 3 {
		t.Fatalf("totals %+v", tot)
	}
	if len(tot.ByAgent) != 2 || tot.ByAgent[0].RuntimeAgent != "taf-refunds" || tot.ByAgent[0].Credits != 131 || tot.ByAgent[0].Runs != 2 ||
		tot.ByAgent[1].Agent != "agt-chargebacks" || tot.ByAgent[1].Credits != 45 {
		t.Fatalf("by agent %+v", tot.ByAgent)
	}
	// By day and agent, for the operators' console: one account's, or every
	// account's.
	days, err := p.RunDays(ctx, tn, from, to)
	if err != nil || len(days) != 7 {
		t.Fatalf("run days: %+v, %v", days, err)
	}
	if d := days[1]; d.Tenant != tn || !d.Day.Equal(from.AddDate(0, 0, 2)) || d.Agent != "agt-refunds" || d.Runs != 1 || d.Counted != 1 ||
		d.Credits != 119 || d.CostMicroUSD != 320_000 || !d.Last.Equal(day(2)) {
		t.Fatalf("day 2: %+v", d)
	}
	if d := days[3]; d.RuntimeAgent != "taf-refunds" || d.Runs != 1 || d.Counted != 0 || d.Credits != 0 || d.CostMicroUSD != 50_000 {
		t.Fatalf("the trial's day: %+v", d)
	}
	if d := days[5]; !d.Setup || d.Agent != "agt-coach" {
		t.Fatalf("Setup's day: %+v", d)
	}
	all, err := p.RunDays(ctx, "", from, to)
	if err != nil {
		t.Fatal(err)
	}
	mine := 0
	for _, d := range all {
		if d.Tenant == tn {
			mine++
		}
	}
	if mine != 7 {
		t.Fatalf("every account's days hold %d of this one's, want 7", mine)
	}
	if empty, err := p.RunTotals(ctx, tn, to.AddDate(0, 1, 0), to.AddDate(0, 2, 0)); err != nil || empty.Runs != 0 || len(empty.ByAgent) != 0 ||
		len(empty.Free) != 3 || empty.Free[meter.FreeSetup] != 0 {
		t.Fatalf("a period with nothing: %+v, %v", empty, err)
	}

	// Newest first, two a page.
	var got []string
	var page *meter.RunPage
	for i := 0; i < 5; i++ {
		list, next, err := p.Runs(ctx, tn, "", nil, from, to, page, 2)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range list {
			got = append(got, e.Run)
		}
		if next == nil {
			break
		}
		page = next
	}
	if fmt.Sprint(got) != "[r7 r6 r5 r4 r3 r2 r1]" {
		t.Fatalf("paged runs %v", got)
	}
	list, _, err := p.Runs(ctx, tn, "taf-refunds", nil, from, to, nil, 10)
	if err != nil || len(list) != 3 || list[0].Run != "r4" || list[0].Free != meter.FreeTrial || list[2].AskedBy != "ada" || !list[2].Ended.Equal(day(1)) {
		t.Fatalf("one agent's runs: %+v, %v", list, err)
	}
	// A person reads their own runs and those of the agents they manage.
	list, _, err = p.Runs(ctx, tn, "", &meter.TaskScope{Subject: "ada", Agents: []string{"agt-chargebacks"}}, from, to, nil, 10)
	var scoped []string
	for _, e := range list {
		scoped = append(scoped, e.Run)
	}
	if err != nil || fmt.Sprint(scoped) != "[r5 r3 r1]" {
		t.Fatalf("ada's reading: %v, %v", scoped, err)
	}
	if list, _, err := p.Runs(ctx, tn, "", &meter.TaskScope{Subject: "paul"}, from, to, nil, 10); err != nil || len(list) != 0 {
		t.Fatalf("someone with no runs of theirs reads %d, %v", len(list), err)
	}
	// A scope with no subject is one agent's runs, whoever asked for them:
	// Coach's credits read.
	list, _, err = p.Runs(ctx, tn, "", &meter.TaskScope{Agents: []string{"agt-refunds"}}, from, to, nil, 10)
	var agents []string
	for _, e := range list {
		agents = append(agents, e.Run)
	}
	if err != nil || fmt.Sprint(agents) != "[r4 r2 r1]" {
		t.Fatalf("one KIFF agent's runs: %v, %v", agents, err)
	}
}

// Coach's reports that arrive at once are measured one after another, so
// its fair use is exact: of two runs that together pass it, one counts.
func TestCoachsFairUseHoldsUnderReportsAtOnce(t *testing.T) {
	ctx := context.Background()
	pool := fixture(t)
	p := meter.NewPostgres(pool)
	tn := fmt.Sprintf("runs-%d", time.Now().UnixNano())
	if _, err := pool.Exec(ctx, `INSERT INTO cloud_meta.tenants VALUES ($1)`, tn); err != nil {
		t.Fatal(err)
	}
	from := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	to := from.AddDate(0, 1, 0)
	const n = 16
	errs, start := make(chan error, n), make(chan struct{})
	for i := 0; i < n; i++ {
		go func(i int) {
			<-start // all at once
			r := meter.RunReport{Tenant: tn, Run: fmt.Sprintf("c%d", i), Agent: "agt-coach", Setup: true, Started: from.Add(time.Hour),
				Ended: from.Add(time.Hour + time.Duration(i)*time.Second), CostMicroUSD: meter.SetupAllowanceMicroUSD / 2, Status: "COMPLETED"}
			_, _, err := p.KeepRun(ctx, r, from, to)
			errs <- err
		}(i)
	}
	close(start)
	for i := 0; i < n; i++ {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	tot, err := p.RunTotals(ctx, tn, from, to)
	if err != nil {
		t.Fatal(err)
	}
	if tot.Free[meter.FreeSetup] != 2 || tot.Setup.Runs != n-2 {
		t.Fatalf("%d free and %d counted, want 2 and %d", tot.Free[meter.FreeSetup], tot.Setup.Runs, n-2)
	}
}

// A report of Setup's waits for the account's lock, so no two of them read
// Setup's spend at once; a taf's report doesn't wait for it.
func TestCoachsReportsTakeTheAccountsLock(t *testing.T) {
	ctx := context.Background()
	pool := fixture(t)
	p := meter.NewPostgres(pool)
	tn := fmt.Sprintf("runs-%d", time.Now().UnixNano())
	if _, err := pool.Exec(ctx, `INSERT INTO cloud_meta.tenants VALUES ($1)`, tn); err != nil {
		t.Fatal(err)
	}
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock(hashtext('billable_runs:setup:' || $1))`, tn); err != nil {
		t.Fatal(err)
	}
	from := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	to := from.AddDate(0, 1, 0)
	report := func(run string, setup bool) meter.RunReport {
		return meter.RunReport{Tenant: tn, Run: run, Agent: "agt", Setup: setup, Started: from.Add(time.Hour), Ended: from.Add(2 * time.Hour),
			CostMicroUSD: 1_000, Status: "COMPLETED"}
	}
	if _, _, err := p.KeepRun(ctx, report("taf-run", false), from, to); err != nil {
		t.Fatalf("a taf's report while the lock is held: %v", err)
	}
	done := make(chan error, 1)
	go func() { _, _, err := p.KeepRun(ctx, report("coach-run", true), from, to); done <- err }()
	select {
	case err := <-done:
		t.Fatalf("Coach's report didn't wait for the lock: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_unlock(hashtext('billable_runs:setup:' || $1))`, tn); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Coach's report never went on after the lock was let go")
	}
}
