package proofs

import (
	"context"
	"sort"
	"sync"
	"time"
)

// States of an app's proof.
const (
	StateGreen   = "green"
	StateRed     = "red"
	StateUnknown = "unknown"
)

// Why an app is red.
const (
	ReasonLimitsFailed = "limits_failed" // the latest counted run's Card limits didn't hold
	ReasonThreeFailed  = "three_failed"  // three counted runs in a row failed
	ReasonTwoPassed    = "two_passed"    // green: two counted runs in a row passed
	ReasonStale        = "stale"         // the state couldn't be read for 24 h
	// ReasonExpired: the app's runs made it green, but its latest counted
	// pass is over PassLapse old, so it reads unknown: not proved.
	ReasonExpired = "expired"
	// ReasonPreviousEntryRed: the app's current entry revision isn't green
	// yet and the one before it was red, so its writes stay held.
	ReasonPreviousEntryRed = "previous_entry_red"
)

// Rules for an app's state (the PM, 17 Nov).
const (
	RedAfterFails  = 3
	GreenAfterPass = 2
)

// PassLapse is how long a green app's latest counted pass stands with no
// newer one: past it, the app reads unknown (ReasonExpired), not proved,
// wherever its state is read. A proof that stops running never keeps an
// app proved.
const PassLapse = 48 * time.Hour

// Derived is an app's state as its runs give it, before any override.
type Derived struct {
	State  string    `json:"state"`
	Since  time.Time `json:"since,omitempty"`
	Reason string    `json:"reason,omitempty"`
}

// Derive works out an app's state from its runs, in any order. Only
// counted pass and fail runs move it:
//   - a failed card_limit_held check makes it red at once;
//   - three fails in a row make it red;
//   - two passes in a row make it green;
//   - anything else keeps the state it had, and with none it's unknown.
//
// So a red app stays red after one pass, and a green app stays green
// after one or two fails.
func Derive(runs []Run) Derived {
	ordered := make([]Run, 0, len(runs))
	for _, r := range runs {
		if r.Counted && (r.Outcome == OutcomePass || r.Outcome == OutcomeFail) {
			ordered = append(ordered, r)
		}
	}
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].EndedAt.Before(ordered[j].EndedAt) })
	d := Derived{State: StateUnknown}
	fails, passes := 0, 0
	set := func(state, reason string, at time.Time) {
		if d.State != state {
			d = Derived{State: state, Since: at, Reason: reason}
		} else if d.Reason != reason && state == StateRed {
			// Still red, now for a sharper reason (limits): keep since.
			d.Reason = reason
		}
	}
	for _, r := range ordered {
		switch {
		case r.LimitsFailed():
			fails, passes = fails+1, 0
			set(StateRed, ReasonLimitsFailed, r.EndedAt)
		case r.Outcome == OutcomeFail:
			fails, passes = fails+1, 0
			if fails >= RedAfterFails {
				set(StateRed, ReasonThreeFailed, r.EndedAt)
			}
		default:
			passes, fails = passes+1, 0
			if passes >= GreenAfterPass {
				set(StateGreen, ReasonTwoPassed, r.EndedAt)
			}
		}
	}
	return d
}

// Override actions.
const (
	OverrideHold    = "hold"    // hold the app's writes whatever its state
	OverrideRelease = "release" // don't hold a red app's writes; needs the owner's yes
	OverrideClear   = "clear"   // end the override in force
)

// Override is one operator action on an app's hold.
type Override struct {
	ID            string     `json:"id"`
	App           string     `json:"app"`
	Action        string     `json:"action"`
	Operator      string     `json:"operator"`
	Reason        string     `json:"reason"`
	OwnerApproval string     `json:"owner_approval,omitempty"`
	Until         *time.Time `json:"until,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
}

// InForce returns the override in force for an app at now: the newest
// one, unless it's a clear or has expired.
func InForce(overrides []Override, app string, now time.Time) (Override, bool) {
	var newest *Override
	for i := range overrides {
		o := &overrides[i]
		if o.App != app {
			continue
		}
		if newest == nil || o.CreatedAt.After(newest.CreatedAt) {
			newest = o
		}
	}
	if newest == nil || newest.Action == OverrideClear {
		return Override{}, false
	}
	if newest.Until != nil && !now.Before(*newest.Until) {
		return Override{}, false
	}
	return *newest, true
}

// AppState is what the gateway will read for an app: its derived state,
// any override in force, and whether its writes are held.
type AppState struct {
	App string `json:"app"`
	// Entry is the app's current catalog entry revision: the entry of its
	// latest counted run. The state is that entry's alone.
	Entry    string    `json:"entry,omitempty"`
	State    string    `json:"state"`
	Since    time.Time `json:"since,omitempty"`
	Reason   string    `json:"reason,omitempty"`
	Override string    `json:"override,omitempty"`
	Held     bool      `json:"held"`
	// Stale is set when the state is the last one read, because a fresh
	// read failed (it's kept up to 24 h).
	Stale bool `json:"stale,omitempty"`
}

// Effective applies the override in force to a derived state. A hold
// holds whatever the state; a release lets a red app's writes through.
func Effective(app string, d Derived, overrides []Override, now time.Time) AppState {
	s := AppState{App: app, State: d.State, Since: d.Since, Reason: d.Reason, Held: d.State == StateRed}
	if o, ok := InForce(overrides, app, now); ok {
		s.Override = o.Action
		switch o.Action {
		case OverrideHold:
			s.Held = true
		case OverrideRelease:
			s.Held = false
		}
	}
	return s
}

// MaxStale is how long the last state read stands when the store can't be
// read; after it, every app is unknown and nothing is held.
const MaxStale = 24 * time.Hour

// CacheFor is how long a read of every app's state is reused.
const CacheFor = 60 * time.Second

// Reader gives each app's state to the gateway, and each entry's to
// planque, cached. When the store can't be read it keeps the last read for
// up to MaxStale, so a red app stays held through a blip; after that
// every app and entry is unknown, nothing is held, and the stale hook
// runs, at most once an hour, so an operator hears.
type Reader struct {
	store   Store
	now     func() time.Time
	onStale func(lastGood time.Time)

	mu        sync.Mutex
	snap      *snapshot
	readAt    time.Time // when snap was last read from the store
	triedAt   time.Time // when a read was last tried
	staleSaid time.Time
}

// snapshot is one read of the store.
type snapshot struct {
	runs      []Run
	overrides []Override
	apps      map[string]AppState
}

// NewReader reads states from store. onStale may be nil.
func NewReader(store Store, onStale func(lastGood time.Time)) *Reader {
	return &Reader{store: store, now: time.Now, onStale: onStale}
}

// WithClock sets the clock, for tests.
func (r *Reader) WithClock(now func() time.Time) *Reader { r.now = now; return r }

// current returns the snapshot to answer from, reading the store when the
// cache is due, or nil past MaxStale (calling the stale hook). r.mu held.
func (r *Reader) current(ctx context.Context, now time.Time) *snapshot {
	if r.snap == nil || now.Sub(r.triedAt) >= CacheFor {
		r.triedAt = now
		if snap, err := readSnapshot(ctx, r.store, now); err == nil {
			r.snap, r.readAt = snap, now
		}
	}
	if r.snap == nil || now.Sub(r.readAt) > MaxStale {
		if r.onStale != nil && now.Sub(r.staleSaid) >= time.Hour {
			r.staleSaid = now
			r.onStale(r.readAt)
		}
		return nil
	}
	return r.snap
}

// StateFor returns app's state now. It never fails: with nothing known,
// the app is unknown and not held.
func (r *Reader) StateFor(ctx context.Context, app string) AppState {
	r.mu.Lock()
	defer r.mu.Unlock()
	snap := r.current(ctx, r.now())
	if snap == nil {
		return AppState{App: app, State: StateUnknown, Reason: ReasonStale}
	}
	s, ok := snap.apps[app]
	if !ok {
		s = AppState{App: app, State: StateUnknown}
	}
	s.Stale = r.readAt.Before(r.triedAt)
	return s
}

// EntryFor returns entry's answer for planque now: green only when the
// entry is freshly green (EntryState), unknown otherwise, and unknown with
// reason stale past MaxStale.
func (r *Reader) EntryFor(ctx context.Context, entry string) EntryAnswer {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	snap := r.current(ctx, now)
	if snap == nil {
		return EntryAnswer{Entry: entry, State: StateUnknown, Reason: ReasonStale}
	}
	return EntryState(snap.runs, snap.overrides, entry, now)
}

func readSnapshot(ctx context.Context, store Store, now time.Time) (*snapshot, error) {
	runs, err := store.RunsSince(ctx, now.Add(-Retention))
	if err != nil {
		return nil, err
	}
	overrides, err := store.Overrides(ctx)
	if err != nil {
		return nil, err
	}
	return &snapshot{runs: runs, overrides: overrides, apps: AppStates(runs, overrides, now)}, nil
}

// ReadStates works out every app's state from the store at now.
func ReadStates(ctx context.Context, store Store, now time.Time) (map[string]AppState, error) {
	snap, err := readSnapshot(ctx, store, now)
	if err != nil {
		return nil, err
	}
	return snap.apps, nil
}

// AppStates works out every app's state from its runs and the overrides.
func AppStates(runs []Run, overrides []Override, now time.Time) map[string]AppState {
	byApp := map[string][]Run{}
	for _, run := range runs {
		byApp[run.App] = append(byApp[run.App], run)
	}
	for _, o := range overrides {
		if _, ok := byApp[o.App]; !ok {
			byApp[o.App] = nil
		}
	}
	out := make(map[string]AppState, len(byApp))
	for app, appRuns := range byApp {
		out[app] = StateOfApp(app, appRuns, overrides, now)
	}
	return out
}

// StateOfApp is one app's state from its runs: the state of its current
// entry revision (the entry of its latest counted run; uncounted runs
// can't change it). A new revision starts unknown and never inherits the
// app's green. While it isn't green, a red previous revision keeps the
// writes held (ReasonPreviousEntryRed), so a catalog change alone never
// lets a broken integration's writes through. An override in force wins.
func StateOfApp(app string, runs []Run, overrides []Override, now time.Time) AppState {
	counted := make([]Run, 0, len(runs))
	for _, r := range runs {
		if r.App == app && r.Counted {
			counted = append(counted, r)
		}
	}
	if len(counted) == 0 {
		return Effective(app, Derived{State: StateUnknown}, overrides, now)
	}
	sort.SliceStable(counted, func(i, j int) bool { return counted[i].EndedAt.Before(counted[j].EndedAt) })
	entry := counted[len(counted)-1].Entry
	d := Derive(ofEntry(counted, entry))
	if d.State == StateGreen {
		if last := lastPass(ofEntry(counted, entry)); now.Sub(last) > PassLapse {
			d = Derived{State: StateUnknown, Since: last.Add(PassLapse), Reason: ReasonExpired}
		}
	}
	s := Effective(app, d, overrides, now)
	s.Entry = entry
	if d.State == StateGreen || s.Override != "" || s.Held {
		return s
	}
	for i := len(counted) - 1; i >= 0; i-- {
		if prev := counted[i].Entry; prev != entry {
			if Derive(ofEntry(counted, prev)).State == StateRed {
				s.Held, s.Reason = true, ReasonPreviousEntryRed
			}
			break
		}
	}
	return s
}

// lastPass is when the latest counted pass among runs ended (zero with none).
func lastPass(runs []Run) time.Time {
	var at time.Time
	for _, r := range runs {
		if r.Counted && r.Outcome == OutcomePass && r.EndedAt.After(at) {
			at = r.EndedAt
		}
	}
	return at
}

func ofEntry(runs []Run, entry string) []Run {
	out := make([]Run, 0, len(runs))
	for _, r := range runs {
		if r.Entry == entry {
			out = append(out, r)
		}
	}
	return out
}

// Reasons an entry isn't answered green for planque.
const (
	ReasonNoRuns       = "no_runs"       // no counted run of this entry revision
	ReasonNotGreen     = "not_green"     // its runs don't make it green (yet)
	ReasonOldRun       = "old_run"       // green, but its latest counted run is over MaxStale old
	ReasonOperatorHold = "operator_hold" // an operator holds the app
)

// EntryAnswer is what planque reads for a catalog entry revision.
type EntryAnswer struct {
	Entry  string    `json:"entry"`
	App    string    `json:"app"`
	State  string    `json:"state"` // green | unknown
	Since  time.Time `json:"since,omitempty"`
	Reason string    `json:"reason,omitempty"`
}

// EntryState answers planque for one entry revision at now: green only
// when that exact revision's counted runs make it green, its latest
// counted pass or fail ended within MaxStale, its latest counted pass
// within PassLapse (ReasonExpired), and no operator holds the
// app; unknown for anything else, red included (Reason says why). A
// release never makes an entry green.
func EntryState(runs []Run, overrides []Override, entry string, now time.Time) EntryAnswer {
	a := EntryAnswer{Entry: entry, State: StateUnknown}
	var mine []Run
	var last Run
	for _, r := range runs {
		if r.Entry != entry || !r.Counted {
			continue
		}
		mine = append(mine, r)
		if r.Outcome != OutcomeError && r.EndedAt.After(last.EndedAt) {
			last = r
		}
	}
	if last.RunID == "" {
		a.Reason = ReasonNoRuns
		return a
	}
	a.App = last.App
	d := Derive(mine)
	switch {
	case d.State != StateGreen:
		a.Reason = ReasonNotGreen
		if d.State == StateRed {
			a.Reason = d.Reason
		}
	case now.Sub(last.EndedAt) > MaxStale:
		a.Reason = ReasonOldRun
	case now.Sub(lastPass(mine)) > PassLapse:
		// Runs go on, but none has passed for PassLapse: not proved, as the app's state reads it.
		a.Reason = ReasonExpired
	default:
		if o, ok := InForce(overrides, last.App, now); ok && o.Action == OverrideHold {
			a.Reason = ReasonOperatorHold
			return a
		}
		a.State, a.Since, a.Reason = StateGreen, d.Since, d.Reason
	}
	return a
}
