// Package mandate bounds what a named subject may do cumulatively.
//
// Every other check in the decide path is a property of one
// proposal: is the entity in an allowed state, does the caller hold the
// permission, do the parameters satisfy their constraints, has a human
// approved. A mandate is the only check whose answer depends on *other*
// requests — on what this subject has already done today.
//
// That is the point. Per-action ceilings bound the size of one mistake.
// Only a cumulative limit bounds variance, which is the quantity that
// grows as prediction gets cheap (Agrawal, Gans & Goldfarb, NBER w24626).
//
// # Not about money
//
// A mandate bounds any countable consequence. `count` bounds
// invocations — three DELETE_CUSTOMER an hour, two DEPLOY_SERVICE a day.
// `sum(p)` bounds the running total of one declared numeric parameter,
// of which money is one instance. Nothing in this package knows what a
// currency is: it sums int64s, and the unit is a label on a statement.
//
// # A mandate constrains; it does not confer
//
// A mandate is an *additional* bound. Permissions still decide whether a
// subject may act at all; a mandate caps how much. Where no mandate covers
// a (subject, action), the check here is a no-op, so issuing a first
// mandate can never break a running tenant.
//
// The stronger posture — no mandate, no authority — is declared per
// action: an action with requires_authority (domain.Spec) is refused
// unless an issued mandate covers it. Every tool the MCP gateway governs
// declares it, so an agent holding no Card for a tool cannot call it.
package mandate

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// QuantityKind is what a mandate counts. Two forms only: a third would
// be the beginning of a policy language, and this is not one.
type QuantityKind string

const (
	// KindCount counts invocations of the granted actions. Applies to
	// every action, needs no parameters, and is the form that bounds
	// irreversible non-monetary work.
	KindCount QuantityKind = "count"
	// KindSum totals one declared numeric parameter across invocations.
	KindSum QuantityKind = "sum"
)

// Quantity is `count` or `sum(<parameter>)`.
type Quantity struct {
	Kind QuantityKind
	// Parameter is the declared parameter name summed by KindSum, and
	// empty for KindCount.
	Parameter string
}

func (q Quantity) String() string {
	if q.Kind == KindSum {
		return fmt.Sprintf("sum(%s)", q.Parameter)
	}
	return string(KindCount)
}

// Key identifies a quantity for accounting. Stable across restarts and
// safe as a storage key.
func (q Quantity) Key() string { return q.String() }

var (
	ErrQuantitySyntax = errors.New("mandate: quantity must be `count` or `sum(<parameter>)`")
	ErrUnknownWindow  = errors.New("mandate: unknown window")
)

// ParseQuantity reads the authored form.
func ParseQuantity(s string) (Quantity, error) {
	s = strings.TrimSpace(s)
	if s == string(KindCount) {
		return Quantity{Kind: KindCount}, nil
	}
	if rest, ok := strings.CutPrefix(s, "sum("); ok {
		name, closed := strings.CutSuffix(rest, ")")
		name = strings.TrimSpace(name)
		if !closed || name == "" || strings.ContainsAny(name, "()") {
			return Quantity{}, fmt.Errorf("%w: got %q", ErrQuantitySyntax, s)
		}
		return Quantity{Kind: KindSum, Parameter: name}, nil
	}
	return Quantity{}, fmt.Errorf("%w: got %q", ErrQuantitySyntax, s)
}

// Window is the period a limit resets over.
type Window string

const (
	// WindowCalendarDay resets at midnight in the tenant's location. It
	// is the default because a limit nobody can explain is a limit
	// nobody trusts, and "per day" is how people read a statement.
	WindowCalendarDay Window = "calendar_day"
	// WindowRollingHour is the last 60 minutes. Harder to game at a
	// boundary, harder to explain.
	WindowRollingHour Window = "rolling_1h"
	// WindowRollingDay is the last 24 hours.
	WindowRollingDay Window = "rolling_24h"
	// WindowLifetime is the Card's whole life: "EUR 200 for this
	// job until Friday". It never resets. It counts every draw the Card
	// has made, not only those since ValidFrom, so a reissue that moves
	// ValidFrom (or switches a daily total to a lifetime one) can never
	// silently reset what was used; the Card's validity ends it.
	WindowLifetime Window = "lifetime"
)

// lifetimeEnd is the end recorded on a lifetime charge: it never resets.
// The Card's own expiry or revocation is what stops it being used.
var lifetimeEnd = time.Date(9999, 12, 31, 0, 0, 0, 0, time.UTC)

// lifetimeHorizon is how far a forecast looks ahead for a lifetime total
// on a Card without an end date.
const lifetimeHorizon = 30 * 24 * time.Hour

// Start returns the instant from which usage counts toward the window.
// Callers sum draws in [Start, now].
func (w Window) Start(now time.Time, loc *time.Location) (time.Time, error) {
	if loc == nil {
		loc = time.UTC
	}
	switch w {
	case WindowCalendarDay:
		t := now.In(loc)
		return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc), nil
	case WindowRollingHour:
		return now.Add(-time.Hour), nil
	case WindowRollingDay:
		return now.Add(-24 * time.Hour), nil
	case WindowLifetime:
		// The zero time: every draw the Card has made counts.
		return time.Time{}, nil
	default:
		return time.Time{}, fmt.Errorf("%w: %q", ErrUnknownWindow, w)
	}
}

// Aggregate is one cumulative cap.
type Aggregate struct {
	Quantity Quantity
	Limit    int64
	Window   Window
	// Measure is independent of the quantity. Omitted values use the
	// defaults: counts measure authorized attempts, sums measure effects.
	// Each draw stores its effective measure; edits cannot reinterpret it.
	Measure Measure
	// ExtraRoom is a confirmed revision with fixed dates. Expiration changes
	// the ceiling, never the usage ledger or the lifetime of a held call.
	ExtraRoom []RoomGrant `json:",omitempty"`
}

// Grant scopes a mandate to actions. In this version a grant says which
// actions the cap applies to; it does not itself confer authority (see
// the package comment).
type Grant struct {
	Action string
	// EntityStates optionally narrows the grant further. Empty means
	// the grant applies in whatever states the action contract already
	// allows — the mandate never widens what the domain permits.
	EntityStates []string
	// PerAction caps what a single action under this grant may carry,
	// whatever the Card's totals have left (`per_action_max`). "Up to €500 each" is the
	// limit a person reads first, and the one that catches the single
	// large mistake a total cannot. Empty means no per-action limit.
	PerAction []PerActionLimit
	// Resources optionally names what the action may be taken on:
	// refund_order only for order_id ord_123. A call on anything else is
	// outside the Card, held or refused like a per-action breach, never
	// "not covered", which would widen authority. Nil means any resource,
	// as before.
	Resources *ResourceScope `json:",omitempty"`
	// NextSteps names what the Card's owner may answer instead of a plain
	// refusal when an action under this grant is held, e.g.
	// return_first. The agent reads the chosen step when it collects the
	// call. Empty means approve or refuse only, as before.
	NextSteps []string `json:",omitempty"`
}

// ResourceScope limits a grant to calls whose string parameter is one
// of Values. Values match exactly; the parameter name matches without
// regard to case, as everywhere else on a Card.
type ResourceScope struct {
	Parameter string
	Values    []string
}

// Resource scope bounds: a scope lists named things, not a pattern.
const (
	MaxResourceValues = 100
	MaxResourceLength = 256
)

// PerActionLimit is the most one action may carry of a summed parameter.
// Only `sum(<parameter>)`: a per-action count is always one.
type PerActionLimit struct {
	Quantity Quantity
	Max      int64
}

// grantsFor returns the grants that cover an action in an entity state,
// the same match Covers makes.
func (m Mandate) grantsFor(action, entityState string) []Grant {
	var out []Grant
	for _, g := range m.Grants {
		if !strings.EqualFold(g.Action, action) {
			continue
		}
		if len(g.EntityStates) == 0 {
			out = append(out, g)
			continue
		}
		for _, s := range g.EntityStates {
			if strings.EqualFold(s, entityState) {
				out = append(out, g)
				break
			}
		}
	}
	return out
}

// Mandate is a bounded, revocable grant of authority to one or more
// named agents, its holders.
type Mandate struct {
	ID       string
	TenantID string
	// Subject is the first holder: the only one on a Card that is not
	// shared, which is the default. Holders lists every holder, Subject
	// first; empty means Subject alone (Cards read before holders were
	// stored, and callers that set only Subject).
	Subject string
	Holders []string
	// Domain is the name of the domain the card was issued for, as it
	// was named then. DomainID is that domain's immutable id,
	// recorded at issuance; when set, the card applies to that domain
	// under whatever name it has now. Cards issued before it have no
	// DomainID and match by Domain, as they always did.
	Domain      string
	DomainID    string
	Environment string
	Grants      []Grant
	Aggregates  []Aggregate
	ValidFrom   time.Time
	ValidUntil  time.Time
	RevokedAt   *time.Time
	// ArchivedAt hides a lapsed card from the working inventory. It is
	// presentation state only: draws, revisions and receipts remain.
	ArchivedAt *time.Time
	// Location names the timezone calendar windows reset in. Empty is
	// UTC.
	Location string
	// DefaultLocation is resolved only for a new Card with no explicit
	// location. Reissues keep their current location under the Card lock.
	// It is an issuance input, never persisted or exposed as Card terms.
	DefaultLocation string `json:"-"`
	// OnExceed is what happens to work outside the Card: OnExceedOwner
	// holds it for the owner, OnExceedRefuse refuses it. Cards that
	// existed before the policy read as refuse.
	OnExceed string
	// HoldExpiry is how long work held for the owner waits for an answer
	// before it is refused. Zero is DefaultHoldExpiry;
	// HoldExpiryKeep asks Issue to keep the Card's current value.
	HoldExpiry time.Duration
}

// Hold expiry bounds: a held action never runs on silence.
const (
	DefaultHoldExpiry = 10 * time.Minute
	MinHoldExpiry     = time.Minute
	MaxHoldExpiry     = 7 * 24 * time.Hour

	// HoldExpiryKeep asks Issue to keep the Card's current hold expiry,
	// read under its lock, as OnExceedKeep does for the policy: a reissue
	// that does not name one cannot reset it. A new Card gets the
	// default. Never stored.
	HoldExpiryKeep time.Duration = -1
)

// HoldFor is how long work held under this Card waits for its owner.
func (m Mandate) HoldFor() time.Duration {
	if m.HoldExpiry <= 0 {
		return DefaultHoldExpiry
	}
	return m.HoldExpiry
}

// Covers reports whether this mandate applies to an action, and in the
// entity's current state.
func (m Mandate) Covers(action, entityState string) bool {
	for _, g := range m.Grants {
		if !strings.EqualFold(g.Action, action) {
			continue
		}
		if len(g.EntityStates) == 0 {
			return true
		}
		for _, s := range g.EntityStates {
			if strings.EqualFold(s, entityState) {
				return true
			}
		}
	}
	return false
}

// Active reports whether the mandate is in force at now.
func (m Mandate) Active(now time.Time) bool {
	if m.RevokedAt != nil && !now.Before(*m.RevokedAt) {
		return false
	}
	if !m.ValidFrom.IsZero() && now.Before(m.ValidFrom) {
		return false
	}
	if !m.ValidUntil.IsZero() && !now.Before(m.ValidUntil) {
		return false
	}
	return true
}

// Loc resolves the mandate's timezone, defaulting to UTC. An
// unparseable name resolves to UTC rather than erroring: a mandate
// should not stop bounding spend because a timezone database is stale.
func (m Mandate) Loc() *time.Location {
	if m.Location == "" {
		return time.UTC
	}
	if loc, err := time.LoadLocation(m.Location); err == nil {
		return loc
	}
	return time.UTC
}

// MaxHolders bounds how many agents can share one Card.
const MaxHolders = 20

// HolderList is every holder of the Card, the first one first.
func (m Mandate) HolderList() []string {
	if len(m.Holders) > 0 {
		return append([]string(nil), m.Holders...)
	}
	if m.Subject == "" {
		return nil
	}
	return []string{m.Subject}
}

// HeldBy reports whether agent is one of the Card's holders. Every check
// that used to compare the Card's subject with the requesting agent asks
// this instead: a Card applies to each of its holders.
func (m Mandate) HeldBy(agent string) bool {
	for _, h := range m.HolderList() {
		if h == agent {
			return true
		}
	}
	return false
}

// withHolders returns m holding exactly holders, Subject first.
func (m Mandate) withHolders(holders []string) Mandate {
	m.Holders = append([]string(nil), holders...)
	if len(holders) > 0 {
		m.Subject = holders[0]
	}
	return m
}

// removedHolders is who held before and does not after.
func removedHolders(before, after []string) []string {
	keep := make(map[string]bool, len(after))
	for _, h := range after {
		keep[h] = true
	}
	var out []string
	for _, h := range before {
		if !keep[h] {
			out = append(out, h)
		}
	}
	return out
}

// keepHolders resolves the holders a reissue writes, given the holders
// the Card has (before; nil for a new Card). Naming holders sets them. A
// reissue that names only a subject keeps a shared Card's holders when
// that subject is its first holder: changing a Card's limits must not
// quietly stop sharing it. Naming a different subject gives the Card to
// that agent alone, as a reissue always did.
func keepHolders(m Mandate, before []string) []string {
	if len(m.Holders) > 0 {
		return m.Holders
	}
	if len(before) > 0 && before[0] == m.Subject {
		return before
	}
	return []string{m.Subject}
}
