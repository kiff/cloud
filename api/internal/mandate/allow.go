package mandate

import (
	"math"
	"slices"
	"strings"
	"time"

	"golang.org/x/text/currency"
)

// "Allow this from now on" on a held call (kiff/tafs/docs/allow-from-now-on.md):
// the narrowest lasting change to the hold's Cards that would have let the
// call go on its own. It is worked out from what the hold recorded, never
// from usage read now, so it says what the call needed when it was held,
// and tafs cloud's own check of it, from the same breaches, agrees.

// Kinds of an allow change.
const (
	AllowPerAction = "per_action" // a grant's per-call limit
	AllowTotal     = "total"      // a total's limit, in an amount
	AllowCount     = "count"      // a total's limit, as a count of calls
	AllowResource  = "resource"   // a value added to a scope
)

// Why a hold gets no allow change.
const (
	AllowNoneUnit     = "unit"      // the Card counts another unit: Coach's to fix
	AllowNoneNoChange = "no_change" // nothing narrow would let it go on its own
)

// AllowChange is one change of an allow: Before and After for a limit (in the
// Card's unit, minor units for an amount), Parameter and Value for a scope.
type AllowChange struct {
	MandateID string `json:"mandate_id"`
	Kind      string `json:"kind"`
	Before    *int64 `json:"before,omitempty"`
	After     *int64 `json:"after,omitempty"`
	Unit      string `json:"unit,omitempty"`
	Window    string `json:"window,omitempty"`
	Parameter string `json:"parameter,omitempty"`
	Value     string `json:"value,omitempty"`
	Rounded   bool   `json:"rounded"`
}

// Allow is the allow change for held call e over the subject's Cards ms: one
// change per breach, on that breach's Card, and the Cards as they'd stand
// after it, only those it changes. With round, each amount goes one rounding
// step up (RoundUp), and Rounded says where that moved it. roundAfter is
// what rounding would give the per-call figure (else a total's), when it
// would move it and round is off, or 0. why is AllowNone* when there's no
// change, and the other results are then empty.
//
// A breach's Card must be in force and held by e's subject alone: a draft
// revises only such a Card. A per-call limit on a count, a per-item total, a
// scope already full, or a breach whose limit isn't found as the hold saw it
// gives no change.
func Allow(ms []Mandate, e Exception, round bool, now time.Time) (cards []Mandate, changes []AllowChange, roundAfter int64, why string) {
	if e.KindOf() == KindCardUnit {
		return nil, nil, 0, AllowNoneUnit
	}
	if len(e.Breaches) == 0 {
		return nil, nil, 0, AllowNoneNoChange
	}
	byID := map[string]int{}
	var exact, rounded int64
	for _, b := range e.Breaches {
		i, seen := byID[b.MandateID]
		if !seen {
			j := slices.IndexFunc(ms, func(m Mandate) bool { return m.ID == b.MandateID })
			if j < 0 || !ms[j].Active(now) || !ms[j].HeldBy(e.Subject) || len(ms[j].HolderList()) != 1 {
				return nil, nil, 0, AllowNoneNoChange
			}
			cards = append(cards, cloneTerms(ms[j]))
			i = len(cards) - 1
			byID[b.MandateID] = i
		}
		c, ok := allowOne(&cards[i], e.Request, b, round)
		if !ok {
			return nil, nil, 0, AllowNoneNoChange
		}
		changes = append(changes, c)
		if c.Kind == AllowPerAction || (c.Kind == AllowTotal && exact == 0) {
			own := figureOf(b, c.Kind)
			exact, rounded = own, RoundUp(own, b.Unit)
		}
	}
	if !round && rounded != exact {
		roundAfter = rounded
	}
	return cards, changes, roundAfter, ""
}

// allowOne applies breach b's change to m, and says what it was.
func allowOne(m *Mandate, req Request, b Breach, round bool) (AllowChange, bool) {
	c := AllowChange{MandateID: b.MandateID, Unit: b.Unit}
	switch b.Outcome {
	case OutcomePerActionExceeded:
		if b.Quantity.Kind == KindCount {
			return c, false
		}
		c.Kind = AllowPerAction
	case OutcomeLimitReached:
		if b.Quantity.Per != "" {
			return c, false
		}
		c.Kind, c.Window = AllowTotal, string(b.Window)
		if b.Quantity.Kind == KindCount {
			c.Kind, c.Unit = AllowCount, ""
		}
	case OutcomeResourceOutside:
		c.Kind, c.Parameter, c.Value = AllowResource, b.Resource, b.Value
		return c, b.Value != "" && addResource(m, req, b.Resource, b.Value)
	default:
		return c, false
	}
	own := figureOf(b, c.Kind)
	after := own
	if round && c.Kind != AllowCount {
		after = RoundUp(own, b.Unit)
		c.Rounded = after != own
	}
	before := b.Limit
	if after <= before {
		return c, false
	}
	c.Before, c.After = &before, &after
	return c, raiseLimit(m, req, b, c.Kind, after)
}

// figureOf is the call's own figure for a change: its amount for a per-call
// limit, what the window had used plus it for a total, as the hold recorded.
func figureOf(b Breach, kind string) int64 {
	if kind == AllowPerAction {
		return b.Draw
	}
	if b.Used > math.MaxInt64-b.Draw {
		return math.MaxInt64
	}
	return b.Used + b.Draw
}

// raiseLimit sets the limit b names on m to after: the per-call limits of the
// grants for the call's action in b's quantity whose maximum is the one the
// hold saw, or the total in b's quantity and window. It reports whether one
// was found. A total's extra room isn't kept: the revision's limit is the
// whole figure, for good.
func raiseLimit(m *Mandate, req Request, b Breach, kind string, after int64) bool {
	found := false
	if kind == AllowPerAction {
		for i, g := range m.Grants {
			if !grantMatches(g, req.Action, req.EntityState) {
				continue
			}
			for j, l := range g.PerAction {
				if l.Quantity.Key() == b.Quantity.Key() && l.Max == b.Limit {
					m.Grants[i].PerAction[j].Max, found = after, true
				}
			}
		}
		return found
	}
	for i, a := range m.Aggregates {
		if a.Quantity.Key() == b.Quantity.Key() && a.Window == b.Window && a.Quantity.Per == "" {
			m.Aggregates[i].Limit, m.Aggregates[i].ExtraRoom, found = after, nil, true
		}
	}
	return found
}

// addResource adds value to the scope on parameter of each grant for the
// call's action that leaves it out, as withHeldResource does, and reports
// whether one took it.
func addResource(m *Mandate, req Request, parameter, value string) bool {
	added := false
	for i, g := range m.Grants {
		r := g.Resources
		if r == nil || !grantMatches(g, req.Action, req.EntityState) || !strings.EqualFold(r.Parameter, parameter) {
			continue
		}
		if slices.Contains(r.Values, value) || len(r.Values) >= MaxResourceValues {
			continue
		}
		m.Grants[i].Resources = &ResourceScope{Parameter: r.Parameter, Values: append(slices.Clone(r.Values), value)}
		added = true
	}
	return added
}

// cloneTerms is m with its grants and totals copied, so changing them leaves
// m as it was.
func cloneTerms(m Mandate) Mandate {
	grants := make([]Grant, len(m.Grants))
	for i, g := range m.Grants {
		g.PerAction = slices.Clone(g.PerAction)
		grants[i] = g
	}
	m.Grants, m.Aggregates = grants, slices.Clone(m.Aggregates)
	return m
}

// RoundUp is an amount in minor units rounded up to a figure an owner would
// pick: the next 50 below 1,000, the next 100 below 10,000, then the next
// 1,000, in major units (€320 → €350, €1,240 → €1,300). Never below it, and
// never more than one step above it. The minor digits are the currency's
// (ISO 4217, as CLDR gives them), 2 for a unit that isn't one: tafs cloud
// rounds the same way, so its check agrees.
func RoundUp(minor int64, unit string) int64 {
	scale := int64(math.Pow10(minorDigits(unit)))
	major := float64(minor) / float64(scale)
	step := int64(1000)
	switch {
	case major < 1000:
		step = 50
	case major < 10000:
		step = 100
	}
	unitStep := step * scale
	if minor <= 0 || minor > math.MaxInt64-unitStep {
		return minor
	}
	return (minor + unitStep - 1) / unitStep * unitStep
}

func minorDigits(unit string) int {
	c, err := currency.ParseISO(unit)
	if err != nil {
		return 2
	}
	scale, _ := currency.Standard.Rounding(c)
	return scale
}
