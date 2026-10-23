package mandate

import (
	"context"
	"errors"
	"math"
	"sort"
	"time"
)

// Forecast is an estimate, never an authorization or a promise of capacity.
// Rolling limits deliberately have no ResetsAt: individual draws age out.
type Forecast struct {
	State              string     `json:"state"`
	ObservedAt         time.Time  `json:"observed_at"`
	ObservationSeconds int64      `json:"observation_seconds"`
	Rate               float64    `json:"quantity_per_second,omitempty"`
	RunsOutAt          *time.Time `json:"runs_out_at,omitempty"`
	ResetsAt           *time.Time `json:"resets_at,omitempty"`
	NextCapacityAt     *time.Time `json:"next_capacity_at,omitempty"`
}

// WarningHistory is a bounded ledger snapshot. Too much/missing history is
// unavailable, never silently truncated into a reassuring estimate.
type WarningHistory struct {
	Limited    bool
	Balances   map[string]LimitBalance
	FirstDraws map[string]time.Time
	Draws      []Draw
	Releases   []EffectRelease
	Revisions  []Revision
	IssuedAt   time.Time
}

type WarningHistoryStore interface {
	WarningHistory(context.Context, string, string, time.Time, time.Time) (WarningHistory, error)
}

var ErrWarningHistory = errors.New("mandate: warning history unavailable")

const warningHistoryLimit = 10000

func Observation(w Window) time.Duration {
	if w == WindowRollingHour {
		return 5 * time.Minute
	}
	return time.Hour
}

func windowDuration(m Mandate, a Aggregate, now time.Time) time.Duration {
	t := chargedLimit(a, m, now)
	if a.Window == WindowCalendarDay {
		return t.WindowEnd.Sub(t.WindowStart)
	}
	if a.Window == WindowRollingHour {
		return time.Hour
	}
	if a.Window == WindowLifetime {
		// What is left of the Card's life, or a fixed horizon without an
		// end date: a lifetime total has no period of its own.
		if !m.ValidUntil.IsZero() && m.ValidUntil.After(now) {
			return m.ValidUntil.Sub(now)
		}
		return lifetimeHorizon
	}
	return 24 * time.Hour
}

func sameAccounting(a, b Aggregate) bool {
	return a.Quantity.Key() == b.Quantity.Key() && a.Window == b.Window && a.EffectiveMeasure() == b.EffectiveMeasure()
}

// stableSince ignores authority-neutral ceiling/room/holder changes. Location
// is additionally checked against each observed draw's original charged terms.
func stableSince(h WarningHistory, a Aggregate) time.Time {
	stable := h.IssuedAt
	wasPresent := true
	for _, r := range h.Revisions {
		found := false
		for _, old := range r.Aggregates {
			if sameAccounting(a, old) {
				found = true
				break
			}
		}
		if !found || !wasPresent {
			stable = r.At
		}
		wasPresent = found
	}
	return stable
}

func historyReleases(h WarningHistory) map[string]EffectRelease {
	r := make(map[string]EffectRelease, len(h.Releases))
	for _, v := range h.Releases {
		r[key(v.TenantID, v.MandateID, v.ProposalID, v.QuantityKey)] = v
	}
	return r
}

// withBalance returns h carrying b as the authoritative balance of a, for a
// total bounded history cannot rebuild (a lifetime one). The map is copied.
func withBalance(h WarningHistory, a Aggregate, b LimitBalance) WarningHistory {
	balances := make(map[string]LimitBalance, len(h.Balances)+1)
	for k, v := range h.Balances {
		balances[k] = v
	}
	balances[balanceKey(a)] = b
	h.Balances = balances
	return h
}

func balanceKey(a Aggregate) string {
	return a.Quantity.Key() + "|" + string(a.Window) + "|" + string(a.EffectiveMeasure())
}

func historicalBalance(h WarningHistory, m Mandate, a Aggregate, now time.Time) (LimitBalance, error) {
	if a.Window == WindowLifetime {
		// Never summed from bounded history, which would understate it.
		b, ok := h.Balances[balanceKey(a)]
		if !ok {
			return LimitBalance{}, ErrWarningHistory
		}
		return b, nil
	}
	if h.Limited {
		b, ok := h.Balances[a.Quantity.Key()+"|"+string(a.Window)+"|"+string(a.EffectiveMeasure())]
		if !ok {
			return LimitBalance{}, ErrWarningHistory
		}
		return b, nil
	}
	start, err := a.Window.Start(now, m.Loc())
	if err != nil {
		return LimitBalance{}, err
	}
	releases := historyReleases(h)
	b := LimitBalance{}
	for _, d := range h.Draws {
		if d.QuantityKey != a.Quantity.Key() || d.DrawnAt.Before(start) || d.DrawnAt.After(now) {
			continue
		}
		var release *EffectRelease
		if r, ok := releases[key(d.TenantID, d.MandateID, d.ProposalID, d.QuantityKey)]; ok {
			release = &r
		}
		if err := addBalance(&b, d, release, m, a, now, start); err != nil {
			return LimitBalance{}, err
		}
	}
	return b, nil
}

func observedUsage(h WarningHistory, m Mandate, a Aggregate, start, end time.Time) (float64, int, time.Duration, bool) {
	releases := historyReleases(h)
	var total float64
	var samples int
	var first, last time.Time
	for _, d := range h.Draws {
		if d.QuantityKey != a.Quantity.Key() || d.DrawnAt.Before(start) || d.DrawnAt.After(end) || d.Amount <= 0 {
			continue
		}
		matching := false
		for _, t := range d.ChargedLimits {
			if t.Measure == a.EffectiveMeasure() && t.Window == a.Window && t.Location == m.Loc().String() {
				matching = true
				break
			}
		}
		if !matching {
			return 0, 0, 0, false
		}
		samples++
		if first.IsZero() || d.DrawnAt.Before(first) {
			first = d.DrawnAt
		}
		if d.DrawnAt.After(last) {
			last = d.DrawnAt
		}
		if r, ok := releases[key(d.TenantID, d.MandateID, d.ProposalID, d.QuantityKey)]; ok {
			since, _ := a.Window.Start(end, m.Loc())
			if releaseCounts(r, d, m, a, end, since) {
				continue
			}
		}
		total += float64(d.Amount)
	}
	return total, samples, last.Sub(first), true
}

// Project uses the same original-release eligibility as authorization, models
// each rolling draw's aging and each fixed room/release expiry, and stops at
// local midnight for calendar limits. It has no writes or notification hooks.
func Project(h WarningHistory, m Mandate, a Aggregate, now time.Time, balance LimitBalance) Forecast {
	obs := Observation(a.Window)
	f := Forecast{State: "insufficient_history", ObservedAt: now.UTC(), ObservationSeconds: int64(obs / time.Second)}
	terms := chargedLimit(a, m, now)
	end := now.Add(windowDuration(m, a, now))
	if a.Window == WindowCalendarDay {
		reset := terms.WindowEnd.UTC()
		f.ResetsAt = &reset
		end = reset
	}
	if !m.Active(now) {
		f.State = "inactive"
		return f
	}
	if !m.ValidUntil.IsZero() && m.ValidUntil.Before(end) {
		end = m.ValidUntil
	}
	if a.Window == WindowLifetime {
		// A lifetime total cannot be rebuilt from bounded history. The
		// caller's balance is the authoritative SQL sum over the whole
		// ledger, read for this same instant.
		h = withBalance(h, a, balance)
	}
	known, err := historicalBalance(h, m, a, now)
	if err != nil || known != balance {
		f.State = "unavailable"
		return f
	}
	if balance.Net >= a.Ceiling(m, now) {
		f.State = "spent"
		at := now.UTC()
		f.RunsOutAt = &at
		return f
	}
	if h.Limited {
		f.State = "unavailable"
		return f
	}
	if h.IssuedAt.IsZero() || stableSince(h, a).After(now.Add(-obs)) {
		return f
	}
	usage, samples, span, valid := observedUsage(h, m, a, now.Add(-obs), now)
	if !valid || samples < 3 || span < obs/2 {
		return f
	}
	f.Rate = usage / obs.Seconds()
	f.State = "no_projected_exhaustion"
	changes := map[int64]float64{end.UnixNano(): 0}
	releases := historyReleases(h)
	contribution := func(d Draw, r *EffectRelease, at time.Time) float64 {
		start, _ := a.Window.Start(at, m.Loc())
		if d.DrawnAt.Before(start) {
			return 0
		}
		if r != nil && releaseCounts(*r, d, m, a, at, start) {
			return 0
		}
		return float64(d.Amount)
	}
	start, _ := a.Window.Start(now, m.Loc())
	for _, d := range h.Draws {
		if d.QuantityKey != a.Quantity.Key() || d.DrawnAt.Before(start) || d.DrawnAt.After(now) {
			continue
		}
		var release *EffectRelease
		if r, ok := releases[key(d.TenantID, d.MandateID, d.ProposalID, d.QuantityKey)]; ok {
			release = &r
		}
		var drawEvents []time.Time
		if a.Window == WindowRollingHour || a.Window == WindowRollingDay {
			at := d.DrawnAt.Add(windowDuration(m, a, now)).Add(time.Nanosecond)
			if at.After(now) && !at.After(end) {
				drawEvents = append(drawEvents, at)
				if d.Amount > 0 && (f.NextCapacityAt == nil || at.Before(*f.NextCapacityAt)) {
					v := at.UTC()
					f.NextCapacityAt = &v
				}
			}
		}
		if release != nil {
			for _, t := range release.ChargedLimits {
				if t.WindowEnd.After(now) && t.WindowEnd.Before(end) {
					drawEvents = append(drawEvents, t.WindowEnd)
				}
			}
		}
		sort.Slice(drawEvents, func(i, j int) bool { return drawEvents[i].Before(drawEvents[j]) })
		previous := contribution(d, release, now)
		for i, at := range drawEvents {
			if i > 0 && at.Equal(drawEvents[i-1]) {
				continue
			}
			current := contribution(d, release, at)
			changes[at.UnixNano()] += current - previous
			previous = current
		}
	}
	for _, r := range a.ExtraRoom {
		if r.ExpiresAt.After(now) && r.ExpiresAt.Before(end) {
			changes[r.ExpiresAt.UnixNano()] += 0
		}
	}
	events := make([]int64, 0, len(changes))
	for at := range changes {
		events = append(events, at)
	}
	sort.Slice(events, func(i, j int) bool { return events[i] < events[j] })
	at := now
	knownUsage := float64(balance.Net)
	for _, instant := range events {
		next := time.Unix(0, instant)
		used := knownUsage + f.Rate*at.Sub(now).Seconds()
		room := float64(a.Ceiling(m, at)) - used
		if room <= 0 {
			v := at.UTC()
			f.RunsOutAt = &v
			f.State = "estimated"
			return f
		}
		if f.Rate > 0 {
			seconds := room / f.Rate
			if seconds < next.Sub(at).Seconds() && seconds <= float64(math.MaxInt64)/float64(time.Second) {
				v := at.Add(time.Duration(math.Ceil(seconds * float64(time.Second)))).UTC()
				f.RunsOutAt = &v
				f.State = "estimated"
				return f
			}
		}
		at = next
		if (a.Window == WindowCalendarDay || a.Window == WindowLifetime) && !at.Before(end) {
			break
		}
		knownUsage += changes[instant]
		if knownUsage+f.Rate*at.Sub(now).Seconds() >= float64(a.Ceiling(m, at)) {
			v := at.UTC()
			f.RunsOutAt = &v
			f.State = "estimated"
			return f
		}
	}
	return f
}
