package mandate

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/kiff/cloud/api/internal/domain"
)

// ErrInvalidMandate is returned when a mandate cannot be issued.
var ErrInvalidMandate = errors.New("mandate: invalid")

// Validate checks a mandate against the action contracts it claims to
// bound, at issue time.
//
// This is what makes `sum(p)` well-founded rather than stringly-typed.
// The domain grammar already declares parameter types
// (domain.ParameterSpec carries Name and Type), so a mandate naming a
// parameter that an action does not declare, or declares as a
// non-numeric, is refused when it is issued — not discovered at
// runtime, weeks later, when a limit silently never binds because it
// has been summing a field that is always absent.
//
// actions is the contract set for the mandate's domain, keyed however
// the caller has them; only Name and Parameters are read.
func Validate(m Mandate, actions []domain.ActionSpec) error {
	if strings.TrimSpace(m.Subject) == "" {
		return fmt.Errorf("%w: subject is required — a mandate binds a named actor", ErrInvalidMandate)
	}
	if err := validateHolders(m); err != nil {
		return err
	}
	if len(m.Grants) == 0 {
		return fmt.Errorf("%w: at least one grant is required", ErrInvalidMandate)
	}
	if len(m.Aggregates) == 0 {
		return fmt.Errorf("%w: a mandate with no aggregate bounds nothing", ErrInvalidMandate)
	}
	if !m.ValidFrom.IsZero() && !m.ValidUntil.IsZero() && !m.ValidUntil.After(m.ValidFrom) {
		return fmt.Errorf("%w: valid_until must be after valid_from", ErrInvalidMandate)
	}
	if h := m.HoldExpiry; h != 0 && h != HoldExpiryKeep && (h < MinHoldExpiry || h > MaxHoldExpiry) {
		return fmt.Errorf("%w: hold_expiry must be between %s and %s", ErrInvalidMandate, MinHoldExpiry, MaxHoldExpiry)
	}

	byName := make(map[string]domain.ActionSpec, len(actions))
	for _, a := range actions {
		byName[strings.ToUpper(strings.TrimSpace(a.Name))] = a
	}

	granted := make([]domain.ActionSpec, 0, len(m.Grants))
	for _, g := range m.Grants {
		name := strings.ToUpper(strings.TrimSpace(g.Action))
		spec, ok := byName[name]
		if !ok {
			return fmt.Errorf("%w: action %q is not declared in this domain", ErrInvalidMandate, g.Action)
		}
		// A mandate narrows; it must never widen. Naming a state the
		// action does not allow would read as granting it.
		for _, st := range g.EntityStates {
			if !containsFold(spec.AllowedStates, st) {
				return fmt.Errorf("%w: action %s does not allow state %q, and a mandate cannot widen a contract",
					ErrInvalidMandate, spec.Name, st)
			}
		}
		// A per-action limit caps one numeric parameter of this grant's
		// action. A count per action is always one, a limit of zero
		// would forbid what the grant allows, and a parameter the action
		// does not declare as a number would never bind.
		for _, lim := range g.PerAction {
			if lim.Quantity.Kind != KindSum {
				return fmt.Errorf("%w: a per-action limit on %s must be sum(<parameter>); a count per action is always one",
					ErrInvalidMandate, spec.Name)
			}
			if lim.Max <= 0 {
				return fmt.Errorf("%w: the per-action limit for %s on %s must be above zero",
					ErrInvalidMandate, lim.Quantity, spec.Name)
			}
			p, ok := parameterOf(spec, lim.Quantity.Parameter)
			if !ok {
				return fmt.Errorf("%w: per-action limit %s names %q, which action %s does not declare",
					ErrInvalidMandate, lim.Quantity, lim.Quantity.Parameter, spec.Name)
			}
			if !isNumeric(p.Type) {
				return fmt.Errorf("%w: per-action limit %s names %q, which action %s declares as %s",
					ErrInvalidMandate, lim.Quantity, lim.Quantity.Parameter, spec.Name, p.Type)
			}
		}
		if err := validateResources(spec, g.Resources); err != nil {
			return err
		}
		if err := validateNextSteps(spec.Name, g.NextSteps); err != nil {
			return err
		}
		granted = append(granted, spec)
	}

	for _, agg := range m.Aggregates {
		if err := agg.validateMeasure(); err != nil {
			return err
		}
		if agg.Limit < 0 {
			return fmt.Errorf("%w: limit for %s must not be negative", ErrInvalidMandate, agg.Quantity)
		}
		if _, err := agg.Window.Start(time.Now(), m.Loc()); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidMandate, err)
		}
		if agg.Quantity.Kind != KindSum {
			continue
		}
		for _, spec := range granted {
			p, ok := parameterOf(spec, agg.Quantity.Parameter)
			if !ok {
				return fmt.Errorf("%w: %s sums %q, which action %s does not declare",
					ErrInvalidMandate, agg.Quantity, agg.Quantity.Parameter, spec.Name)
			}
			if !isNumeric(p.Type) {
				return fmt.Errorf("%w: %s sums %q, which action %s declares as %s",
					ErrInvalidMandate, agg.Quantity, agg.Quantity.Parameter, spec.Name, p.Type)
			}
		}
	}
	return nil
}

// validateResources checks a grant's resource scope: a string or enum
// parameter the action declares, and one to MaxResourceValues exact values. A scope
// with no values would forbid everything the grant allows; a value with
// surrounding spaces would never match what a tool sends.
func validateResources(spec domain.ActionSpec, r *ResourceScope) error {
	if r == nil {
		return nil
	}
	p, ok := parameterOf(spec, r.Parameter)
	if !ok {
		return fmt.Errorf("%w: the resources for %s name %q, which the action does not declare",
			ErrInvalidMandate, spec.Name, r.Parameter)
	}
	if t := strings.ToLower(strings.TrimSpace(p.Type)); t != "string" && t != "enum" {
		return fmt.Errorf("%w: the resources for %s name %q, which the action declares as %s; a resource is a string parameter",
			ErrInvalidMandate, spec.Name, r.Parameter, p.Type)
	}
	if len(r.Values) == 0 || len(r.Values) > MaxResourceValues {
		return fmt.Errorf("%w: the resources for %s must list 1 to %d values", ErrInvalidMandate, spec.Name, MaxResourceValues)
	}
	seen := make(map[string]bool, len(r.Values))
	for _, v := range r.Values {
		if v == "" || len(v) > MaxResourceLength || strings.TrimSpace(v) != v {
			return fmt.Errorf("%w: resource %q for %s must be 1 to %d characters with no surrounding spaces",
				ErrInvalidMandate, v, spec.Name, MaxResourceLength)
		}
		if seen[v] {
			return fmt.Errorf("%w: resource %q is listed twice for %s", ErrInvalidMandate, v, spec.Name)
		}
		seen[v] = true
	}
	return nil
}

func parameterOf(spec domain.ActionSpec, name string) (domain.ParameterSpec, bool) {
	for _, p := range spec.Parameters {
		if strings.EqualFold(p.Name, name) {
			return p, true
		}
	}
	return domain.ParameterSpec{}, false
}

// isNumeric reports whether a declared parameter type can be summed.
// Deliberately conservative: an unrecognised type is not summable,
// because a limit that silently sums nothing is worse than one that
// refuses to be created.
func isNumeric(t string) bool {
	switch strings.ToLower(strings.TrimSpace(t)) {
	case "integer", "int", "number":
		return true
	default:
		return false
	}
}

func containsFold(haystack []string, needle string) bool {
	for _, h := range haystack {
		if strings.EqualFold(h, needle) {
			return true
		}
	}
	return false
}

// validateHolders: one to MaxHolders named agents, each once, the first
// being the Card's subject.
func validateHolders(m Mandate) error {
	hs := m.HolderList()
	if len(hs) > MaxHolders {
		return fmt.Errorf("%w: a Card can be shared by at most %d agents", ErrInvalidMandate, MaxHolders)
	}
	if hs[0] != m.Subject {
		return fmt.Errorf("%w: subject must be the first holder", ErrInvalidMandate)
	}
	seen := make(map[string]bool, len(hs))
	for _, h := range hs {
		if strings.TrimSpace(h) == "" {
			return fmt.Errorf("%w: a holder must name an agent", ErrInvalidMandate)
		}
		if seen[h] {
			return fmt.Errorf("%w: %s is named twice; each agent holds a Card once", ErrInvalidMandate, h)
		}
		seen[h] = true
	}
	return nil
}

// UncappedAmounts names each action the mandate grants with a parameter
// isAmount says is the amount it moves, that the mandate bounds neither per
// action nor in a total, as "action(parameter)". A Card that only counts
// calls of a tool that moves money lets each call move any amount
// (kiff/tafs#138). Which parameter is an amount is the caller's to say, from
// what KIFF knows explicitly (a unit, a permit target's amount argument, a
// gateway tool's amount argument), never from a type alone: a read's
// required integer (a page, a limit) isn't one. Parameter names match without
// regard to case, as everywhere else on a Card.
func UncappedAmounts(m Mandate, actions []domain.ActionSpec, isAmount func(action string, p domain.ParameterSpec) bool) []string {
	summed := func(q Quantity, param string) bool {
		return q.Kind == KindSum && strings.EqualFold(q.Parameter, param)
	}
	var out []string
	for _, g := range m.Grants {
		for _, a := range actions {
			if a.Name != g.Action {
				continue
			}
			for _, p := range a.Parameters {
				if !isAmount(a.Name, p) {
					continue
				}
				bounded := false
				for _, l := range g.PerAction {
					bounded = bounded || summed(l.Quantity, p.Name)
				}
				for _, ag := range m.Aggregates {
					bounded = bounded || summed(ag.Quantity, p.Name)
				}
				if !bounded {
					out = append(out, g.Action+"("+p.Name+")")
				}
			}
		}
	}
	return out
}
