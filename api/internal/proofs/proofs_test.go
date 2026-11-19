package proofs

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)

const testTenant = "tnt_test"

var testTenants = map[string]bool{testTenant: true}

// goodRun is a passing run that Validate accepts.
func goodRun() Run {
	return Run{
		Schema: 1, RunID: "01JPROOFRUN0000000000000001", App: "stripe", Job: "refunds-in-time",
		Tenant: testTenant, Trigger: TriggerSchedule, Thread: "thr_1",
		StartedAt: t0.Add(-3 * time.Minute), EndedAt: t0.Add(-time.Minute), Outcome: OutcomePass,
		Checks: []Check{
			{Name: CheckConnects, OK: true}, {Name: CheckCardLimitHeld, OK: true}, {Name: CheckVendorReceived, OK: true},
		},
		LimitsHeld: true, P50Ms: 300, P95Ms: 900, RunnerVersion: strings.Repeat("a", 40),
		EventsKey: "proofs/01JPROOFRUN0000000000000001/events.jsonl",
		Entry:     "stripe@7", Rules: json.RawMessage(`{"coach_words":"sha256:ab12","judge_limits":"sha256:cd34"}`),
	}
}

func TestAGoodRunIsAccepted(t *testing.T) {
	if err := Validate(goodRun(), testTenants, t0); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestARunIsRefused(t *testing.T) {
	cases := map[string]struct {
		edit func(*Run)
		code string
	}{
		"another schema":          {func(r *Run) { r.Schema = 2 }, "schema"},
		"a customer's tenant":     {func(r *Run) { r.Tenant = "tnt_customer" }, "tenant_not_test"},
		"no tenant":               {func(r *Run) { r.Tenant = "" }, "tenant_not_test"},
		"a bad app":               {func(r *Run) { r.App = "Stripe/../x" }, "app"},
		"a bad trigger":           {func(r *Run) { r.Trigger = "cron" }, "trigger"},
		"ended before it started": {func(r *Run) { r.EndedAt = r.StartedAt.Add(-time.Second) }, "times"},
		"ended in the future":     {func(r *Run) { r.EndedAt = t0.Add(time.Hour) }, "times_future"},
		"p50 over p95":            {func(r *Run) { r.P50Ms = 1000 }, "timings"},
		"a short runner version":  {func(r *Run) { r.RunnerVersion = "abc" }, "runner_version"},
		"another run's key":       {func(r *Run) { r.EventsKey = "proofs/01JPROOFRUN0000000000000002/events.jsonl" }, "events_key"},
		"the old dated layout":    {func(r *Run) { r.EventsKey = "proofs/stripe/2026/11/17/" + r.RunID + ".jsonl" }, "events_key"},
		"a key outside proofs/":   {func(r *Run) { r.EventsKey = "reports/" + r.RunID + "/events.jsonl" }, "events_key"},
		"a dot-dot in the key":    {func(r *Run) { r.EventsKey = "proofs/" + r.RunID + "/../other/x" }, "events_key"},
		"no entry":                {func(r *Run) { r.Entry = "" }, "entry"},
		"an entry with no rev":    {func(r *Run) { r.Entry = "stripe" }, "entry"},
		"an entry with a space":   {func(r *Run) { r.Entry = "stripe @7" }, "entry"},
		"no rules":                {func(r *Run) { r.Rules = nil }, "rules"},
		"rules not an object":     {func(r *Run) { r.Rules = json.RawMessage(`["sha256:ab"]`) }, "rules"},
		"rules null":              {func(r *Run) { r.Rules = json.RawMessage(`null`) }, "rules"},
		"rules too long": {func(r *Run) {
			r.Rules = json.RawMessage(`{"x":"` + strings.Repeat("a", 17<<10) + `"}`)
		}, "rules"},
		"a secret in the rules": {func(r *Run) { r.Rules = json.RawMessage(`{"k":"sk_` + `live_ABCDEFGHIJKLMNOPQRST"}`) }, "secret_in_text"},
		"an unknown check":      {func(r *Run) { r.Checks = append(r.Checks, Check{Name: "vibes", OK: true}) }, "check"},
		"a check twice":         {func(r *Run) { r.Checks = append(r.Checks, Check{Name: CheckConnects, OK: true}) }, "twice"},
		"no limits check":       {func(r *Run) { r.Checks = r.Checks[:1] }, "missing"},
		"a pass with a failed check": {func(r *Run) {
			r.Checks[2].OK = false
		}, "pass_with_failed_check"},
		"a fail with every check ok": {func(r *Run) { r.Outcome = OutcomeFail }, "fail_without_failed_check"},
		"limits_held disagreeing": {func(r *Run) {
			r.Outcome, r.Checks[1].OK = OutcomeFail, false // limits failed, but limits_held says true
		}, "limits_held_disagrees"},
		"an unknown outcome":           {func(r *Run) { r.Outcome = "maybe" }, "outcome"},
		"a token in a check's why":     {func(r *Run) { r.Checks[2].Why = "sent with sk_" + "live_ABCDEFGHIJKLMNOP" }, "secret_in_text"},
		"a bearer value in the job":    {func(r *Run) { r.Job = "Bearer abcdefghijklmnopqrstuvwx" }, "secret_in_text"},
		"a JWT in the thread":          {func(r *Run) { r.Thread = "eyJhbGciOiJSUzI1NiJ9" + ".eyJzdWIiOiJ4eHh4In0.c2lnbmF0dXJl" }, "secret_in_text"},
		"a control character in a why": {func(r *Run) { r.Checks[0].Why = "a\x00b" }, "why"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			r := goodRun()
			c.edit(&r)
			err := Validate(r, testTenants, t0)
			if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), c.code) {
				t.Fatalf("Validate = %v, want %q", err, c.code)
			}
		})
	}
}

func TestTheEventsKeyMayBeTheRunsPrefix(t *testing.T) {
	for _, key := range []string{"proofs/01JPROOFRUN0000000000000001", "proofs/01JPROOFRUN0000000000000001/",
		"proofs/01JPROOFRUN0000000000000001/events.jsonl", "proofs/01JPROOFRUN0000000000000001/judge/limits.json"} {
		r := goodRun()
		r.EventsKey = key
		if err := Validate(r, testTenants, t0); err != nil {
			t.Errorf("%s: %v", key, err)
		}
	}
}

func TestAnErrorRunNeedsNoChecks(t *testing.T) {
	r := goodRun()
	r.Outcome, r.Checks, r.LimitsHeld = OutcomeError, nil, false
	if err := Validate(r, testTenants, t0); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

// run makes a counted run of app ending at t0+min minutes.
func run(app string, min int, outcome string) Run {
	r := Run{App: app, Entry: app + "@1", RunID: app + "-" + time.Duration(min).String(), Outcome: outcome, Counted: true,
		EndedAt: t0.Add(time.Duration(min) * time.Minute), StartedAt: t0.Add(time.Duration(min-1) * time.Minute),
		Trigger: TriggerSchedule, Tenant: testTenant}
	switch outcome {
	case OutcomePass:
		r.Checks = []Check{{Name: CheckConnects, OK: true}, {Name: CheckCardLimitHeld, OK: true}}
	case OutcomeFail:
		r.Checks = []Check{{Name: CheckConnects, OK: false}, {Name: CheckCardLimitHeld, OK: true}}
	}
	return r
}

func limitsFail(app string, min int) Run {
	r := run(app, min, OutcomeFail)
	r.Checks = []Check{{Name: CheckConnects, OK: true}, {Name: CheckCardLimitHeld, OK: false}}
	return r
}

func outcomes(app string, list ...string) []Run {
	var out []Run
	for i, o := range list {
		out = append(out, run(app, i, o))
	}
	return out
}

func TestAnAppsStateFollowsItsRuns(t *testing.T) {
	P, F, E := OutcomePass, OutcomeFail, OutcomeError
	cases := []struct {
		name   string
		runs   []Run
		state  string
		reason string
	}{
		{"no runs", nil, StateUnknown, ""},
		{"one pass", outcomes("a", P), StateUnknown, ""},
		{"two passes", outcomes("a", P, P), StateGreen, ReasonTwoPassed},
		{"green then two fails stays green", outcomes("a", P, P, F, F), StateGreen, ReasonTwoPassed},
		{"three fails", outcomes("a", F, F, F), StateRed, ReasonThreeFailed},
		{"green then three fails", outcomes("a", P, P, F, F, F), StateRed, ReasonThreeFailed},
		{"red then one pass stays red", outcomes("a", F, F, F, P), StateRed, ReasonThreeFailed},
		{"red then two passes", outcomes("a", F, F, F, P, P), StateGreen, ReasonTwoPassed},
		{"error runs neither break nor count", outcomes("a", F, E, F, E, P, F), StateUnknown, ""},
		{"errors between fails don't reset them", outcomes("a", F, E, F, E, F), StateRed, ReasonThreeFailed},
		{"a pass between fails resets them", outcomes("a", F, F, P, F), StateUnknown, ""},
		{"limits fail once is red at once", append(outcomes("a", P, P), limitsFail("a", 9)), StateRed, ReasonLimitsFailed},
		{"limits fail then one pass stays red", append(append(outcomes("a", P, P), limitsFail("a", 9)), run("a", 10, P)), StateRed, ReasonLimitsFailed},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := Derive(c.runs)
			if d.State != c.state || d.Reason != c.reason {
				t.Fatalf("Derive = %+v, want %s/%s", d, c.state, c.reason)
			}
		})
	}
}

func TestUncountedRunsNeverMoveTheState(t *testing.T) {
	runs := outcomes("a", OutcomeFail, OutcomeFail, OutcomeFail)
	for i := range runs {
		runs[i].Counted = false
	}
	if d := Derive(runs); d.State != StateUnknown {
		t.Fatalf("uncounted fails made it %s", d.State)
	}
	// And three uncounted passes can't clear a red app.
	red := outcomes("a", OutcomeFail, OutcomeFail, OutcomeFail)
	for i := 0; i < 3; i++ {
		p := run("a", 10+i, OutcomePass)
		p.Counted = false
		red = append(red, p)
	}
	if d := Derive(red); d.State != StateRed {
		t.Fatalf("uncounted passes cleared a red app: %s", d.State)
	}
}

func TestDeriveIgnoresTheOrderItIsGiven(t *testing.T) {
	runs := outcomes("a", OutcomeFail, OutcomeFail, OutcomeFail, OutcomePass, OutcomePass)
	rev := make([]Run, len(runs))
	for i := range runs {
		rev[len(runs)-1-i] = runs[i]
	}
	if d := Derive(rev); d.State != StateGreen {
		t.Fatalf("reversed order gave %s", d.State)
	}
}

func TestOverrides(t *testing.T) {
	red := Derived{State: StateRed, Reason: ReasonThreeFailed}
	green := Derived{State: StateGreen}
	until := t0.Add(time.Hour)
	past := t0.Add(-time.Minute)
	hold := Override{App: "a", Action: OverrideHold, CreatedAt: t0.Add(-time.Minute), Until: &until}
	release := Override{App: "a", Action: OverrideRelease, CreatedAt: t0.Add(-time.Minute), Until: &until, OwnerApproval: "yes"}

	if s := Effective("a", red, nil, t0); !s.Held {
		t.Error("a red app isn't held")
	}
	if s := Effective("a", green, nil, t0); s.Held {
		t.Error("a green app is held")
	}
	if s := Effective("a", green, []Override{hold}, t0); !s.Held || s.Override != OverrideHold {
		t.Errorf("a hold didn't hold a green app: %+v", s)
	}
	if s := Effective("a", red, []Override{release}, t0); s.Held {
		t.Error("a release didn't release a red app")
	}
	expired := release
	expired.Until = &past
	if s := Effective("a", red, []Override{expired}, t0); !s.Held || s.Override != "" {
		t.Errorf("an expired release still stands: %+v", s)
	}
	clear := Override{App: "a", Action: OverrideClear, CreatedAt: t0}
	if s := Effective("a", red, []Override{release, clear}, t0); !s.Held {
		t.Error("a clear didn't end the release")
	}
	other := hold
	other.App = "b"
	if s := Effective("a", green, []Override{other}, t0); s.Held {
		t.Error("another app's hold held this one")
	}
}

type flakyStore struct {
	*MemoryStore
	fail bool
}

func (f *flakyStore) RunsSince(ctx context.Context, since time.Time) ([]Run, error) {
	if f.fail {
		return nil, errors.New("down")
	}
	return f.MemoryStore.RunsSince(ctx, since)
}

func TestTheReaderKeepsTheLastStateFor24Hours(t *testing.T) {
	ctx := context.Background()
	store := &flakyStore{MemoryStore: NewMemoryStore()}
	for _, r := range outcomes("a", OutcomeFail, OutcomeFail, OutcomeFail) {
		if err := store.Append(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	now := t0.Add(time.Hour)
	var stale []time.Time
	reader := NewReader(store, func(last time.Time) { stale = append(stale, last) }).WithClock(func() time.Time { return now })

	if s := reader.StateFor(ctx, "a"); s.State != StateRed || !s.Held || s.Stale {
		t.Fatalf("first read: %+v", s)
	}
	store.fail = true
	now = now.Add(23 * time.Hour)
	if s := reader.StateFor(ctx, "a"); s.State != StateRed || !s.Held || !s.Stale {
		t.Fatalf("under 24 h unreadable, the red app must stay held: %+v", s)
	}
	if len(stale) != 0 {
		t.Fatalf("stale hook ran too early")
	}
	now = now.Add(2 * time.Hour) // 25 h since the last good read
	if s := reader.StateFor(ctx, "a"); s.State != StateUnknown || s.Held || s.Reason != ReasonStale {
		t.Fatalf("past 24 h it must be unknown and not held: %+v", s)
	}
	if len(stale) != 1 {
		t.Fatalf("stale hook ran %d times, want 1", len(stale))
	}
	now = now.Add(10 * time.Minute)
	reader.StateFor(ctx, "a")
	if len(stale) != 1 {
		t.Fatalf("stale hook ran again within the hour")
	}
	store.fail = false
	now = now.Add(time.Minute)
	if s := reader.StateFor(ctx, "a"); s.State != StateRed || s.Stale {
		t.Fatalf("after recovery: %+v", s)
	}
}

func TestTheReaderCachesForAMinute(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	now := t0
	reader := NewReader(store, nil).WithClock(func() time.Time { return now })
	if s := reader.StateFor(ctx, "a"); s.State != StateUnknown {
		t.Fatal(s)
	}
	for _, r := range outcomes("a", OutcomeFail, OutcomeFail, OutcomeFail) {
		_ = store.Append(ctx, r)
	}
	if s := reader.StateFor(ctx, "a"); s.State != StateUnknown {
		t.Fatalf("read again inside the cache window: %+v", s)
	}
	now = now.Add(CacheFor)
	if s := reader.StateFor(ctx, "a"); s.State != StateRed {
		t.Fatalf("not read again after the window: %+v", s)
	}
}

func TestRunsAreAppendOnly(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	r := goodRun()
	if err := store.Append(ctx, r); err != nil {
		t.Fatal(err)
	}
	r.Outcome = OutcomeFail
	if err := store.Append(ctx, r); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("second Append = %v, want ErrDuplicate", err)
	}
	got, _ := store.Run(ctx, r.RunID)
	if got.Outcome != OutcomePass {
		t.Fatal("the first run was replaced")
	}
	if _, err := store.Run(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
}

func TestTheSweepTakesOnlyOldRows(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	old, fresh := run("a", 0, OutcomePass), run("a", 1, OutcomePass)
	old.ReceivedAt, fresh.ReceivedAt = t0.AddDate(0, 0, -61), t0.AddDate(0, 0, -59)
	_ = store.Append(ctx, old)
	_ = store.Append(ctx, fresh)
	_, _ = store.AppendOverride(ctx, Override{App: "a", Action: OverrideHold, CreatedAt: t0.AddDate(0, 0, -90)})
	n, err := store.Sweep(ctx, t0.Add(-Retention))
	if err != nil || n != 2 {
		t.Fatalf("Sweep = %d, %v; want 2", n, err)
	}
	runs, _ := store.RunsSince(ctx, time.Time{})
	if len(runs) != 1 || runs[0].RunID != fresh.RunID {
		t.Fatalf("left %+v", runs)
	}
}

func TestRows(t *testing.T) {
	now := t0.Add(time.Hour)
	runs := outcomes("stripe", OutcomePass, OutcomeFail, OutcomePass, OutcomePass, OutcomeError)
	uncounted := run("stripe", 20, OutcomePass)
	uncounted.Counted = false
	runs = append(runs, uncounted, run("gmail", 1, OutcomeFail), run("gmail", 2, OutcomeFail), run("gmail", 3, OutcomeFail))
	rows := Rows(runs, nil, now)
	if len(rows) != 2 || rows[0].App != "gmail" || !rows[0].State.Held {
		t.Fatalf("held app first: %+v", rows)
	}
	s := rows[1]
	if s.Passed != 3 || s.Counted != 4 || s.Errors != 1 || s.Uncounted != 1 || s.LastRunID != uncounted.RunID {
		t.Fatalf("stripe row = %+v", s)
	}
	if len(s.Strip) != StripDays {
		t.Fatalf("strip has %d days", len(s.Strip))
	}
	today := s.Strip[len(s.Strip)-1]
	if today.Date != "2026-11-17" || today.Status != DayAmber {
		t.Fatalf("today = %+v", today)
	}
	if rows[0].Strip[len(rows[0].Strip)-1].Status != DayRed || s.Strip[0].Status != DayGrey {
		t.Fatalf("strip colours wrong")
	}
}

func TestErrorAlerts(t *testing.T) {
	three := outcomes("stripe", OutcomeError, OutcomeError, OutcomeError)
	if a := ErrorAlerts(three[2], three); len(a) != 1 || a[0].Kind != AlertAppErrors || a[0].App != "stripe" {
		t.Fatalf("three errors: %+v", a)
	}
	two := outcomes("stripe", OutcomePass, OutcomeError, OutcomeError)
	if a := ErrorAlerts(two[2], two); len(a) != 0 {
		t.Fatalf("two errors alerted: %+v", a)
	}
	pass := run("stripe", 5, OutcomePass)
	if a := ErrorAlerts(pass, append(three, pass)); len(a) != 0 {
		t.Fatalf("a pass alerted: %+v", a)
	}
	// One scheduled pass, every app broke.
	all := []Run{run("stripe", 0, OutcomeError), run("gmail", 1, OutcomeError), run("zendesk", 2, OutcomeError)}
	got := ErrorAlerts(all[2], all)
	if len(got) != 1 || got[0].Kind != AlertAllErrors {
		t.Fatalf("every app broke: %+v", got)
	}
	mixed := []Run{run("stripe", 0, OutcomeError), run("gmail", 1, OutcomePass), run("zendesk", 2, OutcomeError)}
	if a := ErrorAlerts(mixed[2], mixed); len(a) != 0 {
		t.Fatalf("one app passed, but it alerted: %+v", a)
	}
	alone := []Run{run("stripe", 0, OutcomeError)}
	if a := ErrorAlerts(alone[0], alone); len(a) != 0 {
		t.Fatalf("one app alone alerted for all: %+v", a)
	}
}

func TestRedactEvent(t *testing.T) {
	line := json.RawMessage(`{"seq":3,"kind":"tool_call","args":{"amount":700,"api_key":"abc","note":"Bearer abcdefghijklmnopqrstuvwxyz"},"list":["ok","gh` + `p_abcdefghijklmnopqrstuvwxyz0123"]}`)
	out, changed := RedactEvent(line)
	if !changed {
		t.Fatal("nothing redacted")
	}
	s := string(out)
	for _, leak := range []string{"abc\"", "Bearer", "ghp_"} {
		if strings.Contains(s, leak) {
			t.Fatalf("leaked %q in %s", leak, s)
		}
	}
	if !strings.Contains(s, `"amount":700`) {
		t.Fatalf("lost the plain fields: %s", s)
	}
	if _, changed := RedactEvent(json.RawMessage(`{"kind":"coach_said","text":"Your refund is held."}`)); changed {
		t.Fatal("redacted a plain line")
	}
}

func TestGitHubMain(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		switch {
		case strings.HasSuffix(r.URL.Path, strings.Repeat("a", 40)):
			_, _ = w.Write([]byte(`{"status":"behind"}`))
		case strings.HasSuffix(r.URL.Path, strings.Repeat("b", 40)):
			_, _ = w.Write([]byte(`{"status":"ahead"}`)) // on a branch, not on main
		case strings.HasSuffix(r.URL.Path, strings.Repeat("c", 40)):
			w.WriteHeader(http.StatusNotFound)
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer srv.Close()
	g := &GitHubMain{Repo: "kiff/agents", Branch: "main", API: srv.URL}
	ctx := context.Background()
	for sha, want := range map[string]bool{strings.Repeat("a", 40): true, strings.Repeat("b", 40): false, strings.Repeat("c", 40): false} {
		if on, err := g.OnMain(ctx, sha); err != nil || on != want {
			t.Errorf("%s: %v, %v; want %v", sha[:1], on, err, want)
		}
	}
	if _, err := g.OnMain(ctx, strings.Repeat("d", 40)); err == nil {
		t.Error("a 500 wasn't an error")
	}
	before := calls
	_, _ = g.OnMain(ctx, strings.Repeat("a", 40))
	if calls != before {
		t.Error("a confirmed commit was asked again")
	}
	if on, _ := g.OnMain(ctx, "not-a-sha"); on {
		t.Error("a malformed sha was confirmed")
	}
}
