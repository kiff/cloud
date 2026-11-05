package mandate

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
	"time"
)

// A Card revision is routine only if every term is the same as or narrower
// than the Card in force; anything else widens it, and confirming it takes
// the owner's fingerprint every time (kiff/tafs#494). Widens says each way
// a revision widens, in words for the owner, so the app can say why it
// asks. It compares what the Card allows, never how it reads: a title, a
// location or how long a hold waits is not authority.

// Widening is one way a revision widens a Card.
type Widening struct {
	// Kind is the term that widens: action, entity_states, per_action,
	// resources, aggregate, valid_until, valid_from, holders, on_exceed,
	// scope.
	Kind string `json:"kind"`
	// Words say it for the owner, such as "Adds the action refund".
	Words string `json:"words"`
}

// Widens lists how next widens cur, both as issued (AsIssuedOver). A new
// Card, with no Card in force, has nothing to compare with: nil. It fails
// closed: the terms it knows are compared one by one, and any other
// difference between the two Cards, a term added later included, widens.
func Widens(cur *Mandate, next Mandate) []Widening {
	if cur == nil {
		return nil
	}
	var out []Widening
	add := func(kind, format string, a ...any) {
		out = append(out, Widening{Kind: kind, Words: fmt.Sprintf(format, a...)})
	}
	if next.Domain != cur.Domain || next.Environment != cur.Environment {
		add("scope", "Moves the Card to another domain or environment")
	}
	was, now := actionTerms(cur.Grants), actionTerms(next.Grants)
	for _, action := range sortedKeys(now) {
		n := now[action]
		w, ok := was[action]
		if !ok {
			add("action", "Adds the action %s", action)
			continue
		}
		if states := widerSet(w.states, n.states); states != "" {
			add("entity_states", "Lets %s act %s", action, states)
		}
		for _, q := range sortedKeys(w.perAction) {
			max, kept := n.perAction[q]
			switch {
			case !kept:
				add("per_action", "Removes the limit of %d on %s for each %s", w.perAction[q], q, action)
			case max > w.perAction[q]:
				add("per_action", "Raises the limit on %s for each %s from %d to %d", q, action, w.perAction[q], max)
			}
		}
		if res := widerResources(w.resources, n.resources); res != "" {
			add("resources", "Lets %s act on %s", action, res)
		}
	}
	for _, a := range cur.Aggregates {
		b, ok := matchingAggregate(next.Aggregates, a)
		if !ok {
			add("aggregate", "Removes the limit of %d on %s %s", a.Limit, a.Quantity, windowWords(a.Window))
			continue
		}
		if b.Limit > a.Limit {
			add("aggregate", "Raises the limit on %s %s from %d to %d", a.Quantity, windowWords(a.Window), a.Limit, b.Limit)
		}
		if windowLooseness(b.Window) > windowLooseness(a.Window) {
			add("aggregate", "Makes the limit on %s reset %s instead of %s", a.Quantity, windowWords(b.Window), windowWords(a.Window))
		}
	}
	if !cur.ValidUntil.IsZero() && (next.ValidUntil.IsZero() || next.ValidUntil.After(cur.ValidUntil)) {
		if next.ValidUntil.IsZero() {
			add("valid_until", "Removes the Card's end date")
		} else {
			add("valid_until", "Moves the Card's end from %s to %s", day(cur.ValidUntil), day(next.ValidUntil))
		}
	}
	if !next.ValidFrom.IsZero() && !cur.ValidFrom.IsZero() && next.ValidFrom.Before(cur.ValidFrom) {
		add("valid_from", "Starts the Card earlier, on %s", day(next.ValidFrom))
	}
	for _, h := range cardHolders(next) {
		if !slices.Contains(cardHolders(*cur), h) {
			add("holders", "Gives the Card to another holder")
			break
		}
	}
	if policyOf(*cur) == OnExceedRefuse && policyOf(next) == OnExceedOwner {
		add("on_exceed", "Holds work outside the Card for you instead of refusing it")
	}
	if !reflect.DeepEqual(otherTerms(*cur), otherTerms(next)) {
		add("other", "Changes another term of the Card")
	}
	return out
}

// otherTerms is the Card with every term Widens compares itself, and what
// is no authority (its name, ids, location, hold time, revocation and
// archive), set aside: what remains must be the same, or the revision
// widens. Next steps are compared as one set across the Card's grants.
// Extra room is left out: a draft never carries it, and adding it is
// critical through its own route.
func otherTerms(m Mandate) *Mandate {
	o := m
	o.ID, o.TenantID, o.Subject, o.Holders, o.Domain, o.DomainID, o.Environment = "", "", "", nil, "", "", ""
	o.ValidFrom, o.ValidUntil, o.RevokedAt, o.ArchivedAt = time.Time{}, time.Time{}, nil, nil
	o.Location, o.DefaultLocation, o.OnExceed, o.HoldExpiry, o.Title = "", "", "", 0, ""
	var steps []string
	for _, g := range m.Grants {
		for _, st := range g.NextSteps {
			if !slices.Contains(steps, st) {
				steps = append(steps, st)
			}
		}
	}
	slices.Sort(steps)
	o.Grants = nil
	if len(steps) > 0 {
		o.Grants = []Grant{{NextSteps: steps}}
	}
	o.Aggregates = nil
	return &o
}

// actionTerm is what every grant for one action allows together: a call
// matches any of them, so the widest of each term counts.
type actionTerm struct {
	states    []string // nil: any state
	perAction map[string]int64
	resources map[string][]string // nil: any resource
}

func actionTerms(gs []Grant) map[string]actionTerm {
	out := map[string]actionTerm{}
	for _, g := range gs {
		t, ok := out[g.Action]
		if !ok {
			t = actionTerm{perAction: map[string]int64{}}
			if len(g.EntityStates) > 0 {
				t.states = slices.Clone(g.EntityStates)
			}
			for _, p := range g.PerAction {
				t.perAction[p.Quantity.String()] = p.Max
			}
			if g.Resources != nil {
				t.resources = map[string][]string{g.Resources.Parameter: slices.Clone(g.Resources.Values)}
			}
			out[g.Action] = t
			continue
		}
		// Another grant for the same action: any state if either is, the
		// higher of each per-action limit and none if either has none, any
		// resource if either is.
		if t.states != nil && len(g.EntityStates) > 0 {
			for _, s := range g.EntityStates {
				if !slices.Contains(t.states, s) {
					t.states = append(t.states, s)
				}
			}
		} else {
			t.states = nil
		}
		limits := map[string]int64{}
		for _, p := range g.PerAction {
			limits[p.Quantity.String()] = p.Max
		}
		for q, max := range t.perAction {
			other, ok := limits[q]
			switch {
			case !ok:
				delete(t.perAction, q)
			case other > max:
				t.perAction[q] = other
			}
		}
		if t.resources != nil && g.Resources != nil {
			t.resources[g.Resources.Parameter] = append(t.resources[g.Resources.Parameter], g.Resources.Values...)
		} else {
			t.resources = nil
		}
		out[g.Action] = t
	}
	return out
}

// widerSet says, in words, what next allows that was doesn't (nil is any),
// or "" when next is the same or narrower.
func widerSet(was, next []string) string {
	if was == nil {
		return ""
	}
	if next == nil {
		return "in any state"
	}
	var extra []string
	for _, s := range next {
		if !slices.Contains(was, s) {
			extra = append(extra, s)
		}
	}
	if len(extra) == 0 {
		return ""
	}
	return "in " + strings.Join(extra, ", ") + " too"
}

func widerResources(was, next map[string][]string) string {
	if was == nil {
		return ""
	}
	if next == nil {
		return "any resource"
	}
	var extra []string
	for _, p := range sortedKeys(next) {
		for _, v := range next[p] {
			if !slices.Contains(was[p], v) {
				extra = append(extra, p+" "+v)
			}
		}
	}
	if len(extra) == 0 {
		return ""
	}
	return strings.Join(extra, ", ") + " too"
}

// matchingAggregate is next's limit on the same quantity and measure.
// Several on one quantity (a day's and a lifetime's): the one with the
// same window, else the loosest, since any of them can be the one met.
func matchingAggregate(next []Aggregate, a Aggregate) (Aggregate, bool) {
	var found []Aggregate
	for _, b := range next {
		if b.Quantity == a.Quantity && measureOf(b) == measureOf(a) {
			if b.Window == a.Window {
				return b, true
			}
			found = append(found, b)
		}
	}
	if len(found) == 0 {
		return Aggregate{}, false
	}
	loosest := found[0]
	for _, b := range found[1:] {
		if windowLooseness(b.Window) > windowLooseness(loosest.Window) {
			loosest = b
		}
	}
	return loosest, true
}

// windowLooseness orders windows by how much the same limit allows: one
// that resets every hour allows the most, a calendar day can allow twice
// across midnight, the last 24 hours never more than once, and a lifetime
// total never resets.
func windowLooseness(w Window) int {
	switch w {
	case WindowRollingHour:
		return 4
	case WindowCalendarDay:
		return 3
	case WindowRollingDay:
		return 2
	case WindowLifetime:
		return 1
	}
	return 5 // unknown: the loosest, so a change to it is never taken as narrower
}

func windowWords(w Window) string {
	switch w {
	case WindowRollingHour:
		return "each hour"
	case WindowCalendarDay:
		return "each day"
	case WindowRollingDay:
		return "in any 24 hours"
	case WindowLifetime:
		return "for the Card's whole life"
	}
	return string(w)
}

func measureOf(a Aggregate) Measure {
	if a.Measure != "" {
		return a.Measure
	}
	if a.Quantity.Kind == KindSum {
		return MeasureEffects
	}
	return MeasureAttempts
}

func cardHolders(m Mandate) []string {
	if len(m.Holders) > 0 {
		return m.Holders
	}
	return []string{m.Subject}
}

func day(t time.Time) string { return t.UTC().Format("2 Jan 2006") }

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
