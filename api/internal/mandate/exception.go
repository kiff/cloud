package mandate

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"
)

// The work comes home: an action outside a Card set
// to come home is held for the Card's owner as an Exception. The owner
// approves it once, changes the Card, or rejects it; silence is refusal.

// What a Card does with work outside it.
const (
	OnExceedOwner  = "owner"  // held for the owner (the default for new Cards)
	OnExceedRefuse = "refuse" // refused, as Cards first did

	// OnExceedKeep asks Issue to keep the Card's current policy, read
	// under the Card's lock in the same transaction as the write: an
	// existing Card keeps what it has, a new Card gets owner. The issue
	// API sends it when a request does not name a policy, so an older
	// client's reissue cannot overwrite a concurrent explicit change
	// with a value it read earlier. Never stored.
	OnExceedKeep = "keep"
)

// ComesHome reports whether work outside this Card is held for its owner.
// Anything but "owner" refuses: a Card read before the policy existed,
// or with an unknown value, keeps the original behaviour.
func (m Mandate) ComesHome() bool { return m.OnExceed == OnExceedOwner }

// Outcomes of the exception flow.
const (
	// OutcomeOwnerRequired: the action is outside a Card set to come
	// home, and is held for the owner. Nothing is authorized.
	OutcomeOwnerRequired Outcome = "owner_required"
	// OutcomeOwnerRejected: the owner rejected the held action.
	OutcomeOwnerRejected Outcome = "owner_rejected"
	// OutcomeOwnerExpired: nobody answered the held action before its
	// Card's hold expiry. Nothing was authorized; a new
	// attempt is a new proposal, held anew.
	OutcomeOwnerExpired Outcome = "owner_expired"
	// OutcomeWithdrawn: the agent withdrew the held call when its run was
	// cancelled. Nothing was authorized, and the same proposal is
	// never re-decided.
	OutcomeWithdrawn      Outcome = "withdrawn"
	OutcomeApprovedNoRoom Outcome = "approved_no_room"
)

// ExceptionStatus is where a held exception is in its life.
type ExceptionStatus string

const (
	ExceptionHeld        ExceptionStatus = "held"
	ExceptionApproved    ExceptionStatus = "approved"    // approve once, not yet used
	ExceptionRejected    ExceptionStatus = "rejected"    // terminal
	ExceptionChanged     ExceptionStatus = "changed"     // the Card changed; re-decided on the next call
	ExceptionConsumed    ExceptionStatus = "consumed"    // the approval was used; terminal
	ExceptionInvalidated ExceptionStatus = "invalidated" // revoked, expired or moved Card; unused authority withdrawn
	ExceptionExpired     ExceptionStatus = "expired"     // not answered before its hold expiry; terminal
)

// ReasonWithdrawn is the reason on a hold its agent withdrew. The
// hold is ExceptionInvalidated, like one whose Card was revoked: it stops
// waiting and authorizes nothing.
const ReasonWithdrawn = "withdrawn by its agent: the run that made the call was cancelled"

// Withdrawn reports whether the hold's agent withdrew it.
func (e Exception) Withdrawn() bool {
	return e.Status == ExceptionInvalidated && e.Reason == ReasonWithdrawn
}

// ExpiryTellWindow is how long after its expiry a call recorded expired
// without an alert is still told. Older ones (expired before the sweep
// existed, say) aren't announced late.
const ExpiryTellWindow = time.Hour

// withdraw is Withdraw's rule, shared by the stores.
func withdraw(e *Exception, agent string, now time.Time) error {
	switch {
	case e.Subject != agent:
		return ErrExceptionNotFound
	case e.Withdrawn():
		return nil
	case e.Expired(now):
		return ErrExceptionExpired
	case e.Status != ExceptionHeld && e.Status != ExceptionApproved:
		// Approved means not yet sent: KIFF sends it only when the agent
		// collects it, which a cancelled run never does. Consumed was sent.
		return ErrExceptionState
	}
	resolve(e, ExceptionInvalidated, agent, now)
	e.Reason = ReasonWithdrawn
	return nil
}

// Breach is one Card an action falls outside, and how.
type Breach struct {
	MandateID string
	Outcome   Outcome // per_action_exceeded, limit_reached or resource_not_on_card
	Quantity  Quantity
	Limit     int64
	Used      int64 // for limit_reached: what the window had used
	Draw      int64 // what this action carries
	Reason    string
	// Resource and Value are, for resource_not_on_card, the scoped
	// parameter and the value the call sent ("" when it sent none).
	Resource string `json:",omitempty"`
	Value    string `json:",omitempty"`
}

// Exception is one action held for a Card's owner.
type Exception struct {
	ID          string
	TenantID    string
	ProposalID  string
	Subject     string
	Action      string
	EntityID    string
	Fingerprint string
	// Request is what was asked, kept so Change Card can compute the
	// smallest change that fits it and redemption can bind to it.
	Request   Request
	Breaches  []Breach
	Status    ExceptionStatus
	Reason    string // why it was invalidated
	CreatedAt time.Time
	// ExpiresAt is when a held exception stops waiting for its owner:
	// CreatedAt plus the shortest hold expiry of the Cards it names. Nil
	// for exceptions held before expiry existed, which never expire.
	ExpiresAt         *time.Time
	ResolvedBy        string
	ResolvedAt        *time.Time
	ConsumedAt        *time.Time
	AccountingVersion int
	LegacyApproval    *LegacyApproval
	PriorApprovals    []ApprovalRecord
	ExternalAnswer    *ExternalAnswer
	// Refusal is the next step and note given with a rejection;
	// nil for a plain refusal and for every other answer.
	Refusal *Refusal `json:",omitempty"`
	// Covering is the complete applicable set at cumulative redemption,
	// including Cards whose thresholds were not waived. It survives replay.
	Covering []string
}

// Expired reports whether a held exception has passed its expiry
// unanswered. Once answered, an exception does not expire: approve once
// stands until it is used.
func (e Exception) Expired(now time.Time) bool {
	return (e.Status == ExceptionHeld && e.ExpiresAt != nil && !now.Before(*e.ExpiresAt)) ||
		e.Status == ExceptionExpired
}

// StatusAt is the exception's status as of now: a held exception past
// its expiry reads as expired before anything has written that down.
func (e Exception) StatusAt(now time.Time) ExceptionStatus {
	if e.Expired(now) {
		return ExceptionExpired
	}
	return e.Status
}

// expire records that a held exception passed its expiry unanswered.
func expire(e *Exception) {
	at := e.CreatedAt
	if e.ExpiresAt != nil {
		at = *e.ExpiresAt
	}
	e.Status, e.ResolvedBy, e.ResolvedAt = ExceptionExpired, "", &at
	e.Reason = "not answered before the hold expired"
}

// holdExpiry is when a hold naming these breaches stops waiting: the
// shortest hold expiry of the Cards it is outside of.
func holdExpiry(breaches []Breach, cards map[string]Mandate, from time.Time) time.Time {
	d := time.Duration(0)
	for _, b := range breaches {
		if h := cards[b.MandateID].HoldFor(); d == 0 || h < d {
			d = h
		}
	}
	if d == 0 {
		d = DefaultHoldExpiry
	}
	return from.Add(d).UTC()
}

// MandateIDs are the Cards the exception is outside of.
func (e Exception) MandateIDs() []string {
	ids := make([]string, 0, len(e.Breaches))
	for _, b := range e.Breaches {
		ids = append(ids, b.MandateID)
	}
	return ids
}

// ExceptionEntry is authority used outside a Card on its owner's say-so.
// It is not a draw: the Card's balance is untouched, and the statement
// totals these separately as "approved outside the Card".
type ExceptionEntry struct {
	TenantID    string
	ExceptionID string
	MandateID   string
	// Subject is the agent whose action it was (see Draw.Subject).
	Subject           string
	ProposalID        string
	QuantityKey       string
	Amount            int64
	RecordedAt        time.Time
	Action            string
	EntityID          string
	AccountingVersion int
}

var (
	ErrExceptionNotFound = errors.New("mandate: no such exception")
	// ErrExceptionState is returned when an answer does not apply to the
	// exception as it now stands: answering one already answered, or
	// changing a Card for one that is not held.
	ErrExceptionState = errors.New("mandate: the exception is not in a state that allows this")
	// ErrExceptionExpired is returned when the owner answers a hold that
	// passed its expiry unanswered.
	ErrExceptionExpired   = errors.New("mandate: the hold expired before it was answered")
	ErrCumulativeApproval = errors.New("mandate: approve once cannot waive a cumulative ceiling; change the Card or reject")
)

// ExceptionID is deterministic per tenant and proposal, so every retry
// of a held proposal finds the same exception and a race to create it
// meets on one row.
func ExceptionID(tenantID, proposalID string) string {
	sum := sha256.Sum256([]byte(tenantID + "\x00" + proposalID))
	return "exc-" + hex.EncodeToString(sum[:10])
}

// fingerprintV2 marks a fingerprint that includes the string parameters.
// One without it was recorded before, from the numeric ones only.
const fingerprintV2 = "v2:"

// RequestFingerprint identifies what a proposal asked for: who, where,
// what and on which entity, with every numeric and every string parameter
// (a digest for one too long to list). An approval binds to it; a retry
// under the same proposal id with a different fingerprint is a different
// request and is refused.
func RequestFingerprint(req Request) string {
	var b strings.Builder
	b.WriteString(numericFingerprintInput(req))
	for _, set := range []struct {
		tag string
		m   map[string]string
	}{{"r", req.Resources}, {"u", req.Unreadable}} {
		names := make([]string, 0, len(set.m))
		for k := range set.m {
			names = append(names, k)
		}
		sort.Strings(names)
		for _, k := range names {
			fmt.Fprintf(&b, "|%s:%d:%s=%d:%s", set.tag, len(k), k, len(set.m[k]), set.m[k])
		}
	}
	sum := sha256.Sum256([]byte(b.String()))
	return fingerprintV2 + hex.EncodeToString(sum[:])
}

// legacyFingerprint is RequestFingerprint as it was before string
// parameters were part of it, kept to recognise exceptions recorded then.
func legacyFingerprint(req Request) string {
	sum := sha256.Sum256([]byte(numericFingerprintInput(req)))
	return hex.EncodeToString(sum[:])
}

func numericFingerprintInput(req Request) string {
	names := make([]string, 0, len(req.Parameters))
	for k := range req.Parameters {
		names = append(names, strings.ToLower(k))
	}
	sort.Strings(names)
	var b strings.Builder
	fmt.Fprintf(&b, "%s|%s|%s|%s|%s|%s", req.Subject, req.DomainID, strings.ToLower(req.Domain),
		strings.ToLower(req.Environment), strings.ToUpper(req.Action), req.EntityID)
	for _, n := range names {
		for k, v := range req.Parameters {
			if strings.EqualFold(k, n) {
				fmt.Fprintf(&b, "|%s=%d", n, v)
			}
		}
	}
	return b.String()
}

// Matches reports whether req is the request this exception was recorded
// for. Every exception recorded since resource scopes carries a v2 fingerprint and
// matches only the exact request, string parameters included.
//
// One recorded before matches on the numeric fingerprint, as it always
// did, when the request has no string parameters. With string parameters
// it can no longer be told whether they are the ones held, so it matches
// only while unanswered: the request is then evaluated in full and held
// again under its exact fingerprint. An answered one (approved, consumed,
// rejected) does not match, and the caller gets proposal_reused rather
// than an answer given for a request nobody saw.
func (e Exception) Matches(req Request) bool {
	if strings.HasPrefix(e.Fingerprint, fingerprintV2) {
		return e.Fingerprint == RequestFingerprint(req)
	}
	if e.Fingerprint != legacyFingerprint(req) {
		return false
	}
	return len(req.Resources) == 0 && len(req.Unreadable) == 0 || e.Status == ExceptionHeld
}

// Breaches returns every covering Card the request falls outside, and
// how: each per-action limit it exceeds and each total it would take
// past its limit. Evaluate reports the first; the exception flow needs
// all of them, so approving one Card can never waive another.
//
// ok is false when the request is not a matter for the owner at all: a
// negative or ambiguous amount, or one that cannot be counted. Those
// keep Evaluate's refusal.
func Breaches(ms []Mandate, req Request, usage Usage, now time.Time) (out []Breach, ok bool, err error) {
	for _, m := range ms {
		if !appliesTo(m, req) || !m.Active(now) {
			continue
		}
		scoped := map[string]bool{}
		for _, g := range m.grantsFor(req.Action, req.EntityState) {
			if g.Resources == nil {
				continue
			}
			param := strings.ToLower(g.Resources.Parameter)
			if _, unreadable := req.Unreadable[param]; unreadable {
				return nil, false, nil // refused, never a question for the owner
			}
			value, present := req.Resources[param]
			if scoped[param] || present && slices.Contains(g.Resources.Values, value) {
				continue
			}
			scoped[param] = true
			out = append(out, Breach{MandateID: m.ID, Outcome: OutcomeResourceOutside,
				Resource: param, Value: value, Reason: resourceReason(g.Resources.Parameter, value, present)})
		}
		for _, g := range m.grantsFor(req.Action, req.EntityState) {
			for _, lim := range g.PerAction {
				for name := range req.Uncountable {
					if strings.EqualFold(name, lim.Quantity.Parameter) {
						return nil, false, nil
					}
				}
				amount := DrawFor(lim.Quantity, req.Parameters)
				if amount < 0 {
					return nil, false, nil
				}
				if amount > lim.Max {
					out = append(out, Breach{MandateID: m.ID, Outcome: OutcomePerActionExceeded, Quantity: lim.Quantity,
						Limit: lim.Max, Draw: amount,
						Reason: fmt.Sprintf("%s of %d is above the per-action limit of %d", lim.Quantity, amount, lim.Max)})
				}
			}
		}
		for _, agg := range m.Aggregates {
			if agg.Quantity.Kind == KindSum {
				for name := range req.Uncountable {
					if strings.EqualFold(name, agg.Quantity.Parameter) {
						return nil, false, nil
					}
				}
			}
			draw := DrawFor(agg.Quantity, req.Parameters)
			if draw < 0 {
				return nil, false, nil
			}
			since, err := agg.Window.Start(now, m.Loc())
			if err != nil {
				return nil, false, err
			}
			used, err := limitUsed(usage, m, agg, now, since)
			if err != nil {
				return nil, false, fmt.Errorf("%w: mandate %s: %v", ErrUsageUnavailable, m.ID, err)
			}
			ceiling := agg.Ceiling(m, now)
			if !withinCeiling(used, draw, ceiling) {
				out = append(out, Breach{MandateID: m.ID, Outcome: OutcomeLimitReached, Quantity: agg.Quantity,
					Limit: ceiling, Used: used, Draw: draw,
					Reason: fmt.Sprintf("%s limit %d per %s reached: %d used, this action draws %d",
						agg.Quantity, ceiling, agg.Window, used, draw)})
			}
		}
	}
	return out, true, nil
}

// breachedIDs is the set of Cards a breach list names.
func breachedIDs(bs []Breach) map[string]bool {
	ids := make(map[string]bool, len(bs))
	for _, b := range bs {
		ids[b.MandateID] = true
	}
	return ids
}

// ownerRequired is the decision returned while an exception is held.
func ownerRequired(e Exception) Decision {
	d := Decision{Outcome: OutcomeOwnerRequired, ExceptionID: e.ID, Breaches: e.Breaches, ExceptionExpiresAt: e.ExpiresAt}
	if len(e.Breaches) > 0 {
		b := e.Breaches[0]
		d.MandateID, d.Quantity, d.Limit, d.Used, d.Draw = b.MandateID, b.Quantity, b.Limit, b.Used, b.Draw
	}
	reasons := make([]string, 0, len(e.Breaches))
	for _, b := range e.Breaches {
		reasons = append(reasons, b.MandateID+": "+b.Reason)
	}
	d.Reason = "outside the card, held for its owner: " + strings.Join(reasons, "; ")
	return d
}

// ChangeFor is the smallest change to one Card that lets the held action
// fit it, given what the Card has used now: each per-action limit the
// action exceeds is raised to the action's amount, each total it would
// pass is raised by exactly the shortfall, and a resource it names that a
// scope leaves out is added to that scope.
func ChangeFor(m Mandate, req Request, usage Usage, now time.Time) (Mandate, bool, error) {
	changed := false
	grants := make([]Grant, len(m.Grants))
	for i, g := range m.Grants {
		g.PerAction = append([]PerActionLimit(nil), g.PerAction...)
		grants[i] = g
	}
	for i, g := range grants {
		if !grantMatches(g, req.Action, req.EntityState) {
			continue
		}
		for j, lim := range g.PerAction {
			if amount := DrawFor(lim.Quantity, req.Parameters); amount > lim.Max {
				grants[i].PerAction[j].Max = amount
				changed = true
			}
		}
	}
	aggs := append([]Aggregate(nil), m.Aggregates...)
	for i, agg := range aggs {
		draw := DrawFor(agg.Quantity, req.Parameters)
		since, err := agg.Window.Start(now, m.Loc())
		if err != nil {
			return m, false, err
		}
		used, err := limitUsed(usage, m, agg, now, since)
		if err != nil {
			return m, false, fmt.Errorf("%w: mandate %s: %v", ErrUsageUnavailable, m.ID, err)
		}
		if used+draw > agg.Limit {
			aggs[i].Limit = used + draw
			changed = true
		}
	}
	m.Grants, m.Aggregates = grants, aggs
	m, added := withHeldResource(m, req)
	return m, changed || added, nil
}

// withHeldResource adds the resource a held call names to each scope of m
// it is outside of, for the call's action and state. A call that names
// none, or one too long to list, cannot be made to fit by a change.
func withHeldResource(m Mandate, req Request) (Mandate, bool) {
	changed := false
	grants := make([]Grant, len(m.Grants))
	copy(grants, m.Grants)
	for i, g := range grants {
		r := g.Resources
		if r == nil || !grantMatches(g, req.Action, req.EntityState) {
			continue
		}
		value, present := req.Resources[strings.ToLower(r.Parameter)]
		if present && !slices.Contains(r.Values, value) && len(r.Values) < MaxResourceValues {
			grants[i].Resources = &ResourceScope{Parameter: r.Parameter, Values: append(slices.Clone(r.Values), value)}
			changed = true
		}
	}
	m.Grants = grants
	return m, changed
}

func grantMatches(g Grant, action, entityState string) bool {
	if !strings.EqualFold(g.Action, action) {
		return false
	}
	if len(g.EntityStates) == 0 {
		return true
	}
	for _, s := range g.EntityStates {
		if strings.EqualFold(s, entityState) {
			return true
		}
	}
	return false
}
