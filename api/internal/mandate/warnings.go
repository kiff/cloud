package mandate

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"
)

type WarningSettings struct {
	Runway            bool `json:"runway"`
	Spent             bool `json:"spent"`
	Unusual           bool `json:"unusual"`
	Burst             bool `json:"burst"`
	RunwayPercent     int  `json:"runway_percent"`
	UnusualMultiplier int  `json:"unusual_multiplier"`
	BurstPercent      int  `json:"burst_percent"`
}

func DefaultWarningSettings() WarningSettings {
	return WarningSettings{Runway: true, Spent: true, Unusual: true, Burst: true, RunwayPercent: 25, UnusualMultiplier: 3, BurstPercent: 50}
}

func (s WarningSettings) Validate() error {
	if s.RunwayPercent < 1 || s.RunwayPercent > 100 || s.UnusualMultiplier < 2 || s.UnusualMultiplier > 100 || s.BurstPercent < 1 || s.BurstPercent > 100 {
		return fmt.Errorf("warnings: runway and burst percentages must be 1–100; unusual multiplier must be 2–100")
	}
	return nil
}

type WarningConfig struct {
	Revision int64           `json:"revision"`
	At       time.Time       `json:"at,omitempty"`
	By       string          `json:"by,omitempty"`
	Settings WarningSettings `json:"settings"`
}

type CardWarning struct {
	ResolvedAt *time.Time `json:"resolved_at,omitempty"`
	ID         string     `json:"id"`
	TenantID   string     `json:"-"`
	CardID     string     `json:"card_id"`
	Kind       string     `json:"kind"`
	Quantity   string     `json:"quantity"`
	Measure    Measure    `json:"measure"`
	Window     Window     `json:"window"`
	At         time.Time  `json:"at"`
	Used       int64      `json:"used"`
	Ceiling    int64      `json:"ceiling"`
	Ratio      float64    `json:"ratio,omitempty"`
	Forecast   Forecast   `json:"forecast"`
	Reason     string     `json:"reason"`
	Episode    string     `json:"episode"`
}

type warningEpisode struct {
	StartedAt    time.Time       `json:"started_at,omitempty"`
	Number       int64           `json:"number"`
	Anchor       time.Time       `json:"anchor"`
	RecoveringAt *time.Time      `json:"recovering_at,omitempty"`
	Seen         map[string]bool `json:"seen"`
}

// equivalentInterval refuses ambiguous/missing local wall-clock endpoints.
// Rates use elapsed seconds so a DST transition cannot fabricate a spike.
func equivalentInterval(start, end time.Time, days int, loc *time.Location) (time.Time, time.Time, bool) {
	shift := func(t time.Time) (time.Time, bool) {
		x := t.In(loc)
		want := time.Date(x.Year(), x.Month(), x.Day()-days, x.Hour(), x.Minute(), x.Second(), x.Nanosecond(), loc)
		w := want.In(loc)
		if w.Hour() != x.Hour() || w.Minute() != x.Minute() {
			return time.Time{}, false
		}
		for _, delta := range []time.Duration{-time.Hour, time.Hour, -30 * time.Minute, 30 * time.Minute} {
			y := want.Add(delta).In(loc)
			if y.Year() == w.Year() && y.Month() == w.Month() && y.Day() == w.Day() && y.Hour() == w.Hour() && y.Minute() == w.Minute() {
				return time.Time{}, false
			}
		}
		return want, true
	}
	a, ok := shift(start)
	if !ok {
		return a, a, false
	}
	b, ok := shift(end)
	return a, b, ok && b.After(a)
}

func unusualRatio(h WarningHistory, m Mandate, a Aggregate, now time.Time) (float64, bool) {
	if h.Limited || h.IssuedAt.IsZero() || stableSince(h, a).After(now.AddDate(0, 0, -7).Add(-Observation(a.Window))) {
		return 0, false
	}
	obs := Observation(a.Window)
	current, _, _, ok := observedUsage(h, m, a, now.Add(-obs), now)
	if !ok {
		return 0, false
	}
	var baseline float64
	var samples int
	for day := 1; day <= 7; day++ {
		start, end, valid := equivalentInterval(now.Add(-obs), now, day, m.Loc())
		if !valid {
			return 0, false
		}
		used, n, _, valid := observedUsage(h, m, a, start, end)
		if !valid {
			return 0, false
		}
		baseline += used / end.Sub(start).Seconds()
		samples += n
	}
	if samples < 7 || baseline <= 0 {
		return 0, false
	}
	return (current / obs.Seconds()) / (baseline / 7), true
}

func warningIdentity(m Mandate, a Aggregate, c WarningConfig, now time.Time) string {
	// Only effective terms: unrelated Card edits never restart an episode.
	room, _ := json.Marshal(a.ExtraRoom)
	terms := string(room) + fmt.Sprintf("%s|%s|%s|%s|%d|%d|", a.Quantity.Key(), a.Window, a.EffectiveMeasure(), m.Loc(), a.Limit, c.Revision)
	if a.Window == WindowCalendarDay {
		start, _ := a.Window.Start(now, m.Loc())
		terms += start.UTC().Format(time.RFC3339Nano)
	}
	return hashWarning(terms)
}

func hashWarning(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:16]) }

// evaluateWarnings mutates only notification episode state. No authority,
// hold, draw, approval or Card terms can be changed by this function.
func evaluateWarnings(h WarningHistory, m Mandate, a Aggregate, c WarningConfig, e warningEpisode, now time.Time) (warningEpisode, *CardWarning, error) {
	b, err := historicalBalance(h, m, a, now)
	if err != nil {
		return e, nil, err
	}
	if e.Seen == nil {
		e.Seen = map[string]bool{}
	}
	if e.Anchor.IsZero() {
		if first, ok := h.FirstDraws[a.Quantity.Key()+"|"+string(a.Window)]; ok && !first.Before(e.StartedAt) {
			e.Anchor = first
		}
		for _, d := range h.Draws {
			if d.QuantityKey == a.Quantity.Key() && d.Amount > 0 && !d.DrawnAt.Before(e.StartedAt) && !d.DrawnAt.Before(now.Add(-windowDuration(m, a, now))) && !d.DrawnAt.After(now) && (e.Anchor.IsZero() || d.DrawnAt.Before(e.Anchor)) {
				e.Anchor = d.DrawnAt
			}
		}

	}
	f := Project(h, m, a, now, b)
	s := c.Settings
	ratio, hasRatio := unusualRatio(h, m, a, now)
	duration := windowDuration(m, a, now)
	anchor := e.Anchor
	if a.Window == WindowCalendarDay {
		anchor, _ = a.Window.Start(now, m.Loc())
	}
	conditions := map[string]bool{
		"spent":   s.Spent && b.Net >= a.Ceiling(m, now),
		"unusual": s.Unusual && hasRatio && ratio >= float64(s.UnusualMultiplier),
		"burst":   s.Burst && !anchor.IsZero() && !h.IssuedAt.IsZero() && now.Before(h.IssuedAt.AddDate(0, 0, 7)) && now.Sub(anchor) >= 0 && now.Sub(anchor) <= duration/10 && float64(b.Net) > float64(a.Ceiling(m, now))*float64(s.BurstPercent)/100,
		"runway":  s.Runway && a.Window != WindowRollingHour && f.State == "estimated" && f.RunsOutAt != nil && f.RunsOutAt.Sub(now) <= duration*time.Duration(s.RunwayPercent)/100,
	}
	active := false
	for _, v := range conditions {
		active = active || v
	}
	knownRecovery := (!e.Seen["runway"] || (f.State != "unavailable" && f.State != "insufficient_history")) && (!e.Seen["unusual"] || hasRatio)
	if a.Window != WindowCalendarDay && knownRecovery && !active && float64(b.Net) < float64(a.Ceiling(m, now))*0.8 && (!hasRatio || ratio < 2) {
		if e.RecoveringAt == nil {
			v := now
			e.RecoveringAt = &v
		}
		if now.Sub(*e.RecoveringAt) >= duration/10 {
			e.Number++
			e.StartedAt = now
			e.Anchor = time.Time{}
			e.Seen = map[string]bool{}
			e.RecoveringAt = nil
		}
	} else {
		e.RecoveringAt = nil
	}
	// A single jump sends the most urgent notice. Lower simultaneous crossings
	// are remembered too, so a later read/job cannot emit their stale notices.
	chosen := ""
	for _, kind := range []string{"runway", "burst", "unusual", "spent"} {
		if conditions[kind] && !e.Seen[kind] {
			chosen = kind
		}
	}
	for kind, v := range conditions {
		if v {
			e.Seen[kind] = true
		}
	}
	if chosen == "" {
		return e, nil, nil
	}
	episode := fmt.Sprintf("%s:%d", warningIdentity(m, a, c, now), e.Number)
	w := CardWarning{ID: "cw_" + hashWarning(m.TenantID+":"+m.ID+":"+episode+":"+chosen), TenantID: m.TenantID, CardID: m.ID, Kind: chosen, Quantity: a.Quantity.Key(), Measure: a.EffectiveMeasure(), Window: a.Window, At: now.UTC(), Used: b.Net, Ceiling: a.Ceiling(m, now), Ratio: ratio, Forecast: f, Episode: episode}
	switch chosen {
	case "spent":
		w.Reason = "This Card total is spent."
	case "unusual":
		w.Reason = fmt.Sprintf("Recent usage is %.1fx its equivalent seven-day average.", ratio)
	case "burst":
		w.Reason = "A new Card used more than its burst threshold at the start of this window."
	case "runway":
		w.Reason = "At the recent rate, this Card total is approaching exhaustion."
	}
	period := "today"
	if a.Window == WindowRollingHour {
		period = "over the last hour"
	} else if a.Window == WindowRollingDay {
		period = "over the last 24 hours"
	} else if a.Window == WindowLifetime {
		period = "so far"
	}
	if w.Ceiling > 0 {
		w.Reason += fmt.Sprintf(" %.0f%% of this total is used %s.", 100*float64(w.Used)/float64(w.Ceiling), period)
	}
	if a.Window == WindowLifetime {
		w.Reason += " This total never resets. Add room, change the Card, or pause its agent."
	} else {
		w.Reason += " Review this window’s room, change the Card, or pause its agent."
	}
	return e, &w, nil
}
