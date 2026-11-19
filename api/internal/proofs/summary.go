package proofs

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"time"
)

// EventSource reads a run's events (one JSON object per line) from where
// the runner wrote them, by the run's events_key. Production reads the
// private reports bucket; nil means the replay shows the summary only.
type EventSource interface {
	Events(ctx context.Context, key string) ([]json.RawMessage, error)
}

// ErrNoEvents: there's no event source, or it has nothing for the key.
var ErrNoEvents = errors.New("proofs: events not available")

// Day cells of the history strip.
const (
	DayGreen = "green" // every counted run passed
	DayAmber = "amber" // some failed
	DayRed   = "red"   // all failed
	DayGrey  = "grey"  // no counted run
)

// Window sizes of a row.
const (
	RecentRuns = 30
	StripDays  = 45
)

// Day is one cell of an app's strip.
type Day struct {
	Date   string `json:"date"` // YYYY-MM-DD, UTC
	Status string `json:"status"`
}

// Row is one app on the monitoring page.
type Row struct {
	App         string    `json:"app"`
	Passed      int       `json:"passed"`  // of the last RecentRuns counted runs
	Counted     int       `json:"counted"` // how many there were (≤ RecentRuns)
	Errors      int       `json:"errors"`  // error runs among the last RecentRuns runs
	Uncounted   int       `json:"uncounted"`
	LastRunID   string    `json:"last_run_id,omitempty"`
	LastAt      time.Time `json:"last_at,omitempty"`
	LastOutcome string    `json:"last_outcome,omitempty"`
	P50Ms       int       `json:"p50_ms"` // median of the last counted runs' p50
	Strip       []Day     `json:"strip"`
	State       AppState  `json:"state"`
}

// Rows builds the monitoring page from every run since the strip began and
// every override, one row per app, sorted held first then by name.
func Rows(runs []Run, overrides []Override, now time.Time) []Row {
	byApp := map[string][]Run{}
	for _, r := range runs {
		byApp[r.App] = append(byApp[r.App], r)
	}
	for _, o := range overrides {
		if _, ok := byApp[o.App]; !ok {
			byApp[o.App] = nil
		}
	}
	rows := make([]Row, 0, len(byApp))
	for app, appRuns := range byApp {
		rows = append(rows, row(app, appRuns, overrides, now))
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].State.Held != rows[j].State.Held {
			return rows[i].State.Held
		}
		return rows[i].App < rows[j].App
	})
	return rows
}

func row(app string, runs []Run, overrides []Override, now time.Time) Row {
	sort.Slice(runs, func(i, j int) bool { return runs[i].EndedAt.After(runs[j].EndedAt) }) // newest first
	r := Row{App: app, State: StateOfApp(app, runs, overrides, now)}
	if len(runs) > 0 {
		r.LastRunID, r.LastAt, r.LastOutcome = runs[0].RunID, runs[0].EndedAt, runs[0].Outcome
	}
	var p50s []int
	for i, run := range runs {
		if i < RecentRuns && run.Outcome == OutcomeError {
			r.Errors++
		}
		if !run.Counted {
			if i < RecentRuns {
				r.Uncounted++
			}
			continue
		}
		if run.Outcome == OutcomeError || r.Counted >= RecentRuns {
			continue
		}
		r.Counted++
		if run.Outcome == OutcomePass {
			r.Passed++
		}
		p50s = append(p50s, run.P50Ms)
	}
	r.P50Ms = median(p50s)
	r.Strip = strip(runs, now)
	return r
}

func median(xs []int) int {
	if len(xs) == 0 {
		return 0
	}
	s := append([]int(nil), xs...)
	sort.Ints(s)
	m := len(s) / 2
	if len(s)%2 == 1 {
		return s[m]
	}
	return (s[m-1] + s[m]) / 2
}

// strip is the last StripDays days, oldest first, from counted pass and
// fail runs.
func strip(runs []Run, now time.Time) []Day {
	type tally struct{ pass, fail int }
	byDay := map[string]*tally{}
	for _, r := range runs {
		if !r.Counted || (r.Outcome != OutcomePass && r.Outcome != OutcomeFail) {
			continue
		}
		d := r.EndedAt.UTC().Format("2006-01-02")
		t := byDay[d]
		if t == nil {
			t = &tally{}
			byDay[d] = t
		}
		if r.Outcome == OutcomePass {
			t.pass++
		} else {
			t.fail++
		}
	}
	today := now.UTC().Truncate(24 * time.Hour)
	out := make([]Day, 0, StripDays)
	for i := StripDays - 1; i >= 0; i-- {
		d := today.AddDate(0, 0, -i).Format("2006-01-02")
		status := DayGrey
		if t := byDay[d]; t != nil {
			switch {
			case t.fail == 0:
				status = DayGreen
			case t.pass == 0:
				status = DayRed
			default:
				status = DayAmber
			}
		}
		out = append(out, Day{Date: d, Status: status})
	}
	return out
}

// Alert kinds for the error-run rules.
const (
	AlertAppErrors = "proof_errors"     // three error runs in a row for one app
	AlertAllErrors = "proof_errors_all" // an error for every app in one scheduled pass
)

// ErrorAlert is one error-run condition to tell an operator about.
type ErrorAlert struct {
	Kind   string
	App    string // empty for AlertAllErrors
	Tenant string // a test tenant the runs used, for the operator queue
	Detail string
}

// SchedulePass is how close scheduled runs must start to count as one pass.
const SchedulePass = 90 * time.Minute

// ErrorAlerts checks the error-run rules after run was kept, against every
// run since the strip began (run included):
//   - the app's last three runs are all error runs;
//   - run is a scheduled error, and in the scheduled pass it belongs to
//     (runs that started within SchedulePass of it), every app that ran
//     has an error as its run in that pass, with at least two apps.
//
// An error run is the runner breaking, not the app, so it never moves a
// state; without these alerts a broken runner would hide a broken app.
func ErrorAlerts(run Run, runs []Run) []ErrorAlert {
	if run.Outcome != OutcomeError {
		return nil
	}
	var out []ErrorAlert
	var mine []Run
	for _, r := range runs {
		if r.App == run.App {
			mine = append(mine, r)
		}
	}
	sort.Slice(mine, func(i, j int) bool { return mine[i].EndedAt.After(mine[j].EndedAt) })
	if len(mine) >= 3 && mine[0].Outcome == OutcomeError && mine[1].Outcome == OutcomeError && mine[2].Outcome == OutcomeError {
		out = append(out, ErrorAlert{Kind: AlertAppErrors, App: run.App, Tenant: run.Tenant,
			Detail: "three proof runs in a row broke before judging " + run.App + "; the runner, not the app, needs a look"})
	}
	if run.Trigger == TriggerSchedule {
		latest := map[string]Run{}
		for _, r := range runs {
			if r.Trigger != TriggerSchedule {
				continue
			}
			if d := r.StartedAt.Sub(run.StartedAt); d > SchedulePass || d < -SchedulePass {
				continue
			}
			if cur, ok := latest[r.App]; !ok || r.EndedAt.After(cur.EndedAt) {
				latest[r.App] = r
			}
		}
		all := len(latest) >= 2
		for _, r := range latest {
			all = all && r.Outcome == OutcomeError
		}
		if all {
			out = append(out, ErrorAlert{Kind: AlertAllErrors, Tenant: run.Tenant,
				Detail: "every app's scheduled proof run broke in one pass; the runner or its environment needs a look"})
		}
	}
	return out
}
