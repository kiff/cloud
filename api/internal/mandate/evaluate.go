package mandate

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// Outcome is what the mandate check contributes to a decision.
type Outcome string

const (
	// OutcomeWithinLimit means every applicable cap has room.
	OutcomeWithinLimit Outcome = "within_limit"
	// OutcomeNotCovered means no active mandate applies. The check is a
	// no-op; permissions already decided whether the subject may act.
	OutcomeNotCovered Outcome = "not_covered"
	// OutcomeLimitReached means an applicable cap has no room for this
	// draw.
	OutcomeLimitReached Outcome = "limit_reached"
	// OutcomeExpired means a mandate covers the action but is revoked
	// or outside its validity window.
	OutcomeExpired Outcome = "expired"
	// OutcomeProposalReused means this proposal id already drew on a
	// card. The id is spent: authorizing it again would return a fresh
	// allowed while the ledger still counted one draw.
	OutcomeProposalReused Outcome = "proposal_reused"
	// OutcomeUncountableDraw means a covering sum card cannot count a
	// present parameter exactly. It must not authorize a zero draw.
	OutcomeUncountableDraw Outcome = "uncountable_draw"

	// OutcomePerActionExceeded is an action larger than a covering
	// grant's per-action limit allows. Its own outcome rather than
	// limit_reached: the Card may have plenty left, and the answer to
	// the person is "this one is too big", not "the Card is spent".
	OutcomePerActionExceeded Outcome = "per_action_exceeded"

	// OutcomeResourceOutside is a call on a resource a covering grant
	// does not name: refund_order for order_id ord_9 under a Card
	// for ord_123 only. Outside the Card, like a per-action breach: held
	// for its owner or refused, never "not covered".
	OutcomeResourceOutside Outcome = "resource_not_on_card"

	// OutcomeUnreadableResource is a call whose scoped parameter cannot be
	// matched exactly: longer than any scope can list, or sent twice in
	// different cases. Refused, never held: an owner approving it could
	// not tell one such call from another.
	OutcomeUnreadableResource Outcome = "unreadable_resource"
)

// Request is the proposal being evaluated, reduced to what a mandate
// cares about.
type Request struct {
	Subject string
	// Domain is the current name (slug) of the domain the request is in;
	// DomainID is that domain's immutable id. A card issued with a
	// DomainID matches the id, so it survives a rename; one issued before
	// matches the name.
	Domain      string
	DomainID    string
	Environment string
	Action      string
	EntityState string
	// EntityID is what the action is being taken on, carried so the
	// draw can name it. It takes no part in the decision.
	EntityID string
	// Parameters are the proposal's numeric parameters, already
	// validated against the action contract by the time this runs.
	Parameters map[string]int64
	// Uncountable names present proposal parameters that could not be
	// represented exactly as int64. Only a covering sum card consults it.
	Uncountable map[string]bool
	// Resources are the proposal's string parameters, keyed by lowercase
	// name, that a grant's resource scope can name.
	Resources map[string]string `json:",omitempty"`
	// Unreadable are string parameters no scope can match exactly, keyed
	// by lowercase name: a value longer than MaxResourceLength, or a name
	// sent in two cases. Each carries a digest of what was sent, so two
	// different calls never look the same to an approval; a scope that
	// names one refuses the call (OutcomeUnreadableResource).
	Unreadable map[string]string `json:",omitempty"`
	// TrialDraft is the draft whose trial made the call (kiff/tafs#173),
	// whose Cards Subject (its TrialSubject) holds. Kept on a held call
	// for its owner's record; it takes no part in the decision.
	TrialDraft string `json:",omitempty"`
	// Observe is true when the tenant is in observe mode. The caller
	// executes anyway, so an action outside a Card is reported as
	// owner_required but not held: there is nothing for an owner to
	// answer.
	Observe bool `json:"-"`
}

// Usage answers what a subject has already drawn against one aggregate
// inside a window. Implementations read the draw ledger.
//
// An error must not be swallowed. A mandate that cannot read its own
// usage cannot bound anything, and the caller is required to refuse —
// see Evaluate's contract and ErrUsageUnavailable.
type Usage interface {
	Used(mandateID string, q Quantity, since time.Time) (int64, error)
}

// UsageFunc adapts a function to Usage.
type UsageFunc func(mandateID string, q Quantity, since time.Time) (int64, error)

func (f UsageFunc) Used(id string, q Quantity, since time.Time) (int64, error) {
	return f(id, q, since)
}

// ErrUsageUnavailable is returned when current usage cannot be
// established.
//
// The caller MUST refuse the proposal on this error. That is the
// opposite default to usage.ProposalLimiter, which allows when
// uncertain — correctly, because blocking a customer's production
// traffic over *our* billing meter is the worse failure
// (TestLimiter_FlushFailureIsAllowedWhenUncertain pins it).
//
// A mandate is not a meter. It is the authority itself, and allowing
// when uncertain means acting with authority nobody could confirm. The
// two limiters sit next to each other in this tree with opposite
// defaults on purpose; see TestMandateFailsClosedUnlikeTheUsageLimiter.
var ErrUsageUnavailable = errors.New("mandate: current usage unavailable; refuse")

// Decision is the result of evaluating one proposal.
type Decision struct {
	Outcome Outcome
	// MandateID names the mandate that produced a non-covering outcome.
	MandateID string
	// Quantity and Limit describe the cap that was reached.
	Quantity Quantity
	Limit    int64
	Used     int64
	// Draw is what this proposal would consume if allowed.
	Draw   int64
	Reason string
	// ExceptionID names the held exception for OutcomeOwnerRequired and
	// OutcomeOwnerRejected; Breaches lists every Card it is outside of.
	ExceptionID string
	Breaches    []Breach
	// ExceptionExpiresAt is when a held exception stops waiting for its
	// owner, for OutcomeOwnerRequired and OutcomeOwnerExpired.
	ExceptionExpiresAt *time.Time
	// Refusal is, for OutcomeOwnerRejected, the next step and note the
	// owner gave with the refusal; nil for a plain refusal.
	Refusal *Refusal
	// NewlyHeld is true only on the decision that first held the work
	// for its owner, never on a retry of it, so the owner is told once.
	NewlyHeld bool
	// NextSteps, on the decision that first held the work, lists the next
	// steps its Cards offer the owner besides approve and reject,
	// so the held-call email can show them.
	NextSteps []string
	// Covering names the Cards that covered an allowed request, for the
	// execution permit's card claim.
	Covering []string
	// Consumed is true only on the decision whose request itself used the
	// authority: a fresh evaluation that drew on the Cards, or the request
	// that redeemed an owner's approval. A later answer for the same
	// proposal (an approval already redeemed) is false, so it can never
	// mint an execution permit.
	Consumed bool
}

// Allowed reports whether the decide path may continue.
func (d Decision) Allowed() bool {
	return d.Outcome == OutcomeWithinLimit || d.Outcome == OutcomeNotCovered
}

// DrawFor computes what one proposal consumes against a quantity.
//
// A sum over a parameter the proposal did not carry draws zero rather
// than erroring: the action contract decides whether a parameter is
// required, and a mandate must not become a second, silent way to make
// one mandatory.
func DrawFor(q Quantity, params map[string]int64) int64 {
	if q.Kind == KindCount {
		return 1
	}
	var draw int64
	found := false
	for name, value := range params {
		if strings.EqualFold(name, q.Parameter) {
			if found {
				// Ambiguous case variants must never pick one value
				// nondeterministically. Evaluate refuses a negative draw.
				return -1
			}
			draw, found = value, true
		}
	}
	return draw
}

// Evaluate decides whether a request fits inside every mandate covering
// it.
//
// Mandates are conjunctive: where several cover one action, all must
// have room. A subject with a generous per-domain mandate and a tight
// per-action one is bound by the tighter, which is the only reading
// that makes issuing a second mandate a safe act.
// names reports whether a mandate names this request's subject, domain
// and environment.
func names(m Mandate, req Request) bool {
	if m.TenantID != "" && req.Subject != "" && !m.HeldBy(req.Subject) {
		return false
	}
	// A card scoped to a domain applies only to a request known to be
	// in that domain. An empty request domain is not a wildcard: it
	// would let a card issued for one domain authorize, and draw for,
	// the same action in another domain of the tenant.
	switch {
	case m.DomainID != "":
		// Matched by id, never by name: a rename keeps the card, and
		// another domain that now carries the card's old name does not
		// inherit it. An unknown request domain id matches nothing.
		if m.DomainID != req.DomainID {
			return false
		}
	case m.Domain != "" && !strings.EqualFold(m.Domain, req.Domain):
		return false
	}
	// The same for environments: a card scoped to one applies only to a
	// request known to be in it, never to one whose environment is
	// unknown.
	if m.Environment != "" && !strings.EqualFold(m.Environment, req.Environment) {
		return false
	}
	return true
}

// appliesTo reports whether a mandate names this request's subject,
// domain and environment and covers its action in the entity's state —
// in force or not.
func appliesTo(m Mandate, req Request) bool {
	return names(m, req) && m.Covers(req.Action, req.EntityState)
}

// AnyGrantsAction reports whether any of ms names the request's subject
// and grants its action, in any entity state and in force or not. It
// reads nothing and draws nothing. Deliberately wider than what one
// proposal would draw on: it answers "does this agent hold a card for
// this action at all", which the decide path must know before it has
// resolved the entity's state.
func AnyGrantsAction(ms []Mandate, req Request) bool {
	for _, m := range ms {
		if !names(m, req) {
			continue
		}
		for _, g := range m.Grants {
			if strings.EqualFold(g.Action, req.Action) {
				return true
			}
		}
	}
	return false
}

func Evaluate(ms []Mandate, req Request, usage Usage, now time.Time) (Decision, error) {
	covering := make([]Mandate, 0, len(ms))
	var expired *Mandate

	for i := range ms {
		m := ms[i]
		if !appliesTo(m, req) {
			continue
		}
		if !m.Active(now) {
			if expired == nil {
				expired = &ms[i]
			}
			continue
		}
		covering = append(covering, m)
	}

	if len(covering) == 0 {
		// An expired mandate that covers the action is reported rather
		// than ignored. Silently falling back to "no mandate applies"
		// would turn an expiry into a widening of authority, which is
		// the failure this primitive exists to prevent.
		if expired != nil {
			return Decision{
				Outcome:   OutcomeExpired,
				MandateID: expired.ID,
				Reason:    "the mandate covering this action is revoked or outside its validity window",
			}, nil
		}
		return Decision{Outcome: OutcomeNotCovered}, nil
	}

	// What the action is taken on, then how large it is: both are
	// outside the Card however much its totals have left, and both would
	// still refuse on a fresh Card.
	for _, m := range covering {
		if d, breached := resourceBreach(m, req); breached {
			return d, nil
		}
	}
	for _, m := range covering {
		if d, breached := perActionBreach(m, req); breached {
			return d, nil
		}
	}

	for _, m := range covering {
		for _, agg := range m.Aggregates {
			if err := agg.validateMeasure(); err != nil {
				return Decision{}, err
			}
			if agg.Quantity.Kind == KindSum {
				for name := range req.Uncountable {
					if strings.EqualFold(name, agg.Quantity.Parameter) {
						return Decision{
							Outcome:   OutcomeUncountableDraw,
							MandateID: m.ID,
							Quantity:  agg.Quantity,
							Reason:    fmt.Sprintf("%s cannot be counted as an integer", agg.Quantity),
						}, nil
					}
				}
			}
			draw := DrawFor(agg.Quantity, req.Parameters)
			ceiling := agg.Ceiling(m, now)
			if draw < 0 {
				return Decision{
					Outcome:   OutcomeLimitReached,
					MandateID: m.ID,
					Quantity:  agg.Quantity,
					Limit:     ceiling,
					Draw:      draw,
					Reason:    fmt.Sprintf("negative draw %d for %s", draw, agg.Quantity),
				}, nil
			}
			since, err := agg.Window.Start(now, m.Loc())
			if err != nil {
				return Decision{}, err
			}
			used, err := limitUsed(usage, m, agg, now, since)
			if err != nil {
				return Decision{}, fmt.Errorf("%w: mandate %s: %v", ErrUsageUnavailable, m.ID, err)
			}
			if !withinCeiling(used, draw, ceiling) {
				return Decision{
					Outcome:   OutcomeLimitReached,
					MandateID: m.ID,
					Quantity:  agg.Quantity,
					Limit:     ceiling,
					Used:      used,
					Draw:      draw,
					Reason: fmt.Sprintf("%s limit %d per %s reached: %d used, this action draws %d",
						agg.Quantity, ceiling, agg.Window, used, draw),
				}, nil
			}
		}
	}

	ids := make([]string, 0, len(covering))
	for _, m := range covering {
		ids = append(ids, m.ID)
	}
	return Decision{Outcome: OutcomeWithinLimit, Covering: ids}, nil
}

// perActionBreach checks one covering Card's per-action limits: those of
// every grant matching the request's action and state, so the tightest
// binds. A negative or ambiguous amount, or one that cannot be counted
// exactly, is refused rather than read as small.
func perActionBreach(m Mandate, req Request) (Decision, bool) {
	for _, g := range m.grantsFor(req.Action, req.EntityState) {
		for _, lim := range g.PerAction {
			for name := range req.Uncountable {
				if strings.EqualFold(name, lim.Quantity.Parameter) {
					return Decision{
						Outcome:   OutcomeUncountableDraw,
						MandateID: m.ID,
						Quantity:  lim.Quantity,
						Reason:    fmt.Sprintf("%s cannot be counted as an integer", lim.Quantity),
					}, true
				}
			}
			amount := DrawFor(lim.Quantity, req.Parameters)
			if amount < 0 {
				return Decision{
					Outcome:   OutcomePerActionExceeded,
					MandateID: m.ID,
					Quantity:  lim.Quantity,
					Limit:     lim.Max,
					Draw:      amount,
					Reason:    fmt.Sprintf("negative amount %d for %s", amount, lim.Quantity),
				}, true
			}
			if amount > lim.Max {
				return Decision{
					Outcome:   OutcomePerActionExceeded,
					MandateID: m.ID,
					Quantity:  lim.Quantity,
					Limit:     lim.Max,
					Draw:      amount,
					Reason:    fmt.Sprintf("%s of %d is above the per-action limit of %d", lim.Quantity, amount, lim.Max),
				}, true
			}
		}
	}
	return Decision{}, false
}

// resourceBreach checks one covering Card's resource scopes: those of
// every grant matching the request's action and state, so each binds. A
// scoped parameter the call does not carry is outside the scope.
func resourceBreach(m Mandate, req Request) (Decision, bool) {
	for _, g := range m.grantsFor(req.Action, req.EntityState) {
		if g.Resources == nil {
			continue
		}
		param := strings.ToLower(g.Resources.Parameter)
		if _, unreadable := req.Unreadable[param]; unreadable {
			return Decision{
				Outcome:   OutcomeUnreadableResource,
				MandateID: m.ID,
				Reason:    fmt.Sprintf("%s is too long, or sent more than once, to match this card's resources exactly", g.Resources.Parameter),
			}, true
		}
		value, present := req.Resources[param]
		if present && slices.Contains(g.Resources.Values, value) {
			continue
		}
		return Decision{
			Outcome:   OutcomeResourceOutside,
			MandateID: m.ID,
			Reason:    resourceReason(g.Resources.Parameter, value, present),
		}, true
	}
	return Decision{}, false
}

func resourceReason(param, value string, present bool) string {
	if !present {
		return fmt.Sprintf("the call names no %s, and this card applies only to the %s values it lists", param, param)
	}
	return fmt.Sprintf("%s %q is not one this card applies to", param, value)
}
