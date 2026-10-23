package mandate

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Authorizer evaluates a proposal against a subject's mandates and, on
// an allowed outcome, records the draw.
//
// Evaluating and drawing is a read-modify-write. Two proposals arriving
// together could each read the same usage, each find room, and both be
// allowed — taking the total past the limit. The window is small and
// the consequence is precisely what a limit exists to prevent, so the
// pair is held under a per-mandate lock.
type Authorizer struct {
	store  Store
	locker Locker
	now    func() time.Time

	// afterDiscover runs between finding a subject's Cards and locking
	// them, and beforeWrite between deciding and recording the draws.
	// Tests use them to commit a move, or lose a connection, in exactly
	// those gaps.
	afterDiscover func()
	beforeWrite   func()
}

// NewAuthorizer returns nil when store is nil, so a caller can treat
// "no mandate store" as "this deployment has no mandates" rather than
// having to nil-check at every call site.
//
// A nil locker means NoLock, which is correct for a single process and
// wrong across replicas. Production passes idempotency.AdvisoryLocker.
func NewAuthorizer(store Store, locker Locker, now func() time.Time) *Authorizer {
	if store == nil {
		return nil
	}
	if locker == nil {
		locker = NoLock{}
	}
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &Authorizer{store: store, locker: locker, now: now}
}

// Authorize decides whether a proposal fits within the subject's
// mandates, and records the draw when it does.
//
// The contract the decide path depends on:
//
//   - A returned error means the answer could not be established, and
//     the caller MUST refuse. This is the opposite default to the
//     billing limiter; see ErrUsageUnavailable.
//   - A Decision with Allowed() false means refuse, with Reason
//     carrying something a person can act on.
//   - Allowed() true means the draw has already been recorded. There is
//     no separate commit step, and no window in which an authorization
//     exists without having consumed budget.
//
// The draw is recorded at authorization rather than at execution,
// because nothing reports execution back. That over-counts
// when an integrator does not run what it was authorized for, which is
// the correct direction to be wrong in: the alternative under-counts,
// and a limit that can be exceeded is not a limit.
func (a *Authorizer) Authorize(ctx context.Context, tenantID, proposalID string, req Request) (Decision, error) {
	if a == nil {
		return Decision{Outcome: OutcomeNotCovered}, nil
	}
	now := a.now()

	// A proposal id that already drew on a card is spent. A
	// genuine retry of that authorization never gets here: the decide
	// path answers it from the recorded decision first. So an id with a
	// draw reaching Authorize is either a reuse or a retry whose cached
	// answer could not be read, and neither can be told apart from a
	// different request under the same id. Refuse both, whatever the
	// cards, entity or amount are now, and leave the draw untouched.
	// Re-evaluating would exclude the old draw, find room, and overwrite
	// it: a new allowed the ledger counts as the same single draw.
	//
	// Asked before anything about the subject's own cards: the id is
	// spent tenant-wide, so a subject with no cards at all cannot reuse
	// it either. Returning not_covered first would hand a spent id a
	// fresh allowed on any action that does not require a card.
	// An owner-approved exception already used under this id: an
	// identical retry gets the same authorization back (one grant
	// consumed, one ledger entry, a caller whose response was lost
	// recovers it); a different request under the id is refused.
	// Consumed is terminal, so reading it outside the locks is safe.
	if proposalID != "" {
		ex, found, err := a.store.GetException(ctx, tenantID, proposalID)
		if err != nil {
			return Decision{}, fmt.Errorf("%w: exception for proposal %s: %v", ErrUsageUnavailable, proposalID, err)
		}
		if found && ex.Status == ExceptionConsumed {
			if !ex.Matches(req) {
				return proposalReusedForException(proposalID), nil
			}
			return redeemedDecision(ex), nil
		}
	}

	if proposalID != "" {
		prior, err := a.store.ProposalDraws(ctx, tenantID, proposalID)
		if err != nil {
			return Decision{}, fmt.Errorf("%w: prior draws for proposal %s: %v", ErrUsageUnavailable, proposalID, err)
		}
		if len(prior) > 0 {
			// Redemption commits its draws and consumed answer atomically. It
			// may have committed between the first answer read and this lookup.
			ex, found, err := a.store.GetException(ctx, tenantID, proposalID)
			if err != nil {
				return Decision{}, fmt.Errorf("%w: reread consumed approval: %v", ErrUsageUnavailable, err)
			}
			if found && ex.Status == ExceptionConsumed {
				if !ex.Matches(req) {
					return proposalReusedForException(proposalID), nil
				}
				return redeemedDecision(ex), nil
			}
			first := prior[0]
			for _, d := range prior[1:] {
				if d.DrawnAt.Before(first.DrawnAt) {
					first = d
				}
			}
			return Decision{
				Outcome:   OutcomeProposalReused,
				MandateID: first.MandateID,
				Reason: fmt.Sprintf("proposal id %s already drew on mandate %s at %s; a new authorization needs a new proposal id",
					proposalID, first.MandateID, first.DrawnAt.UTC().Format(time.RFC3339)),
			}, nil
		}
	}

	// Every mandate naming the subject, including lapsed ones: Evaluate
	// has to see a revoked mandate to report it, rather than mistaking
	// it for no mandate at all.
	//
	// Discover, lock, re-read. The Cards
	// are found before any lock is held, so by the time the locks are
	// taken a Card may have moved to another holder, been revoked, or a
	// new one issued. Every Card found is locked, in a stable order; the
	// set is read again under the locks and must be the same set, or
	// discovery starts over. Reading the terms under the lock is what
	// stops a request that found the old holder from drawing after a
	// move committed.
	if cl, ok := a.store.(CardLocker); ok {
		return a.authorizeInTx(ctx, cl, tenantID, proposalID, req, now)
	}
	ms, release, err := a.lockCards(ctx, tenantID, req.Subject)
	if err != nil {
		return Decision{}, err
	}
	defer release()
	return a.decide(ctx, a.store, ms, tenantID, proposalID, req, now)
}

// Decided reports whether a proposal id has already been decided on a
// Card: it drew, or it has an exception (held, approved, consumed,
// rejected or expired). The permit gate refuses to bind such an id for
// the first time, so a call held before its tool moved to
// verify mode cannot be released with other arguments.
func (a *Authorizer) Decided(ctx context.Context, tenantID, proposalID string) (bool, error) {
	if a == nil || proposalID == "" {
		return false, nil
	}
	if _, found, err := a.store.GetException(ctx, tenantID, proposalID); err != nil || found {
		return found, err
	}
	draws, err := a.store.ProposalDraws(ctx, tenantID, proposalID)
	return len(draws) > 0, err
}

// errCardsChanged rolls back a Card transaction whose re-read found a
// different set of Cards than discovery did.
var errCardsChanged = errors.New("mandate: cards changed while being locked")

// authorizeInTx is Authorize for a store that locks Cards inside the
// transaction that writes the ledger. The re-read, the usage the
// decision is based on and the draws are all on that one transaction,
// holding the Cards' locks until it commits. If the connection is lost
// the locks and the uncommitted draws go together, and the commit error
// makes the answer a refusal: an authorization whose draw may not have
// been recorded is never returned as allowed.
func (a *Authorizer) authorizeInTx(ctx context.Context, cl CardLocker, tenantID, proposalID string, req Request, now time.Time) (Decision, error) {
	for attempt := 0; attempt < lockAttempts; attempt++ {
		found, err := a.store.ForSubject(ctx, tenantID, req.Subject)
		if err != nil {
			return Decision{}, fmt.Errorf("%w: load mandates: %v", ErrUsageUnavailable, err)
		}
		if len(found) == 0 {
			// Nothing covers this subject. Permissions already decided
			// whether it may act at all.
			return Decision{Outcome: OutcomeNotCovered}, nil
		}
		if a.afterDiscover != nil {
			a.afterDiscover()
		}
		ids := make([]string, 0, len(found))
		for _, m := range found {
			ids = append(ids, m.ID)
		}
		var d Decision
		err = cl.WithCards(ctx, tenantID, ids, func(tx CardTx) error {
			locked, err := tx.ForSubject(ctx, tenantID, req.Subject)
			if err != nil {
				return fmt.Errorf("%w: reload mandates: %v", ErrUsageUnavailable, err)
			}
			if !sameCards(found, locked) {
				return errCardsChanged
			}
			d, err = a.decide(ctx, tx, locked, tenantID, proposalID, req, now)
			return err
		})
		switch {
		case errors.Is(err, errCardsChanged):
			continue
		case errors.Is(err, ErrUsageUnavailable):
			return Decision{}, err
		case err != nil:
			return Decision{}, fmt.Errorf("%w: card transaction for proposal %s: %v", ErrUsageUnavailable, proposalID, err)
		}
		return d, nil
	}
	return Decision{}, fmt.Errorf("%w: the cards for %s kept changing while being locked", ErrUsageUnavailable, req.Subject)
}

// decide evaluates a request against Cards already read under their
// locks and records its draws through tx, which is the Card transaction
// or, for a store without one, the store itself.
func (a *Authorizer) decide(ctx context.Context, tx CardTx, ms []Mandate, tenantID, proposalID string, req Request, now time.Time) (Decision, error) {
	if len(ms) == 0 {
		// Nothing covers this subject. Permissions already decided
		// whether it may act at all.
		return Decision{Outcome: OutcomeNotCovered}, nil
	}

	// This proposal's own earlier draws are excluded, so a retry
	// recomputes the same answer instead of being refused by itself.
	usage := usageFor(ctx, tx, tenantID, proposalID)

	// The exception already held for this proposal, if any, read under
	// the Card locks.
	fp := RequestFingerprint(req)
	var ex Exception
	var found bool
	if proposalID != "" {
		var err error
		ex, found, err = tx.GetException(ctx, tenantID, proposalID)
		if err != nil {
			return Decision{}, fmt.Errorf("%w: exception for proposal %s: %v", ErrUsageUnavailable, proposalID, err)
		}
	}
	if found {
		if !ex.Matches(req) {
			return proposalReusedForException(proposalID), nil
		}
		// Withdrawn by its agent: refused for good, like a
		// rejection. Never re-decided, so a retry can neither hold it again
		// nor let it through once it fits the Card, and the record stays
		// withdrawn.
		if ex.Withdrawn() {
			return withdrawnDecision(ex), nil
		}
		if ex.Status == ExceptionApproved && ex.AccountingVersion == 0 {
			requireFreshConsent(&ex)
			if ex.Expired(now) {
				expire(&ex)
			} else {
				cards := make(map[string]Mandate, len(ms))
				for _, m := range ms {
					cards[m.ID] = m
				}
				if reason := grantStillStands(ex, cards, now); reason != "" {
					resolve(&ex, ExceptionInvalidated, "", now)
					ex.Reason = reason
				}
			}
			if err := tx.SaveException(ctx, ex); err != nil {
				return Decision{}, fmt.Errorf("%w: preserve legacy consent: %v", ErrUsageUnavailable, err)
			}
			if ex.Status == ExceptionExpired {
				return ownerExpired(ex), nil
			}
			if ex.Status == ExceptionHeld {
				d := ownerRequired(ex)
				d.Reason = ex.Reason
				return d, nil
			}
		}
		switch ex.Status {
		case ExceptionConsumed:
			return redeemedDecision(ex), nil
		case ExceptionRejected:
			return Decision{Outcome: OutcomeOwnerRejected, ExceptionID: ex.ID, Breaches: ex.Breaches, Refusal: ex.Refusal,
				Reason: "the card's owner rejected this action; nothing was authorized"}, nil
		}
		// Not answered before the hold expired: refused, and
		// for good. It is not re-decided, even if it would fit the Card
		// now; a new attempt is a new proposal, held anew. Expiry has no
		// write of its own, so the first retry after it records it.
		if ex.Expired(now) {
			if ex.Status != ExceptionExpired {
				expire(&ex)
				if err := tx.SaveException(ctx, ex); err != nil {
					return Decision{}, fmt.Errorf("%w: %v", ErrUsageUnavailable, err)
				}
			}
			return ownerExpired(ex), nil
		}
		if ex.Status == ExceptionApproved && ex.ExternalAnswer != nil {
			reader := any(tx)
			if _, ok := reader.(externalGrantReader); !ok {
				reader = a.store
			}
			if err := lockExternalApprovalPolicy(ctx, reader, ex); err != nil {
				return Decision{}, fmt.Errorf("%w: external policy lock: %v", ErrUsageUnavailable, err)
			}
			changed, err := refreshExternalApproval(ctx, reader, &ex, now)
			if err != nil {
				return Decision{}, fmt.Errorf("%w: external approval: %v", ErrUsageUnavailable, err)
			}
			if changed {
				if err := tx.SaveException(ctx, ex); err != nil {
					return Decision{}, err
				}
				if ex.Status == ExceptionExpired {
					return ownerExpired(ex), nil
				}
			}
		}
		switch ex.Status {
		case ExceptionHeld, ExceptionApproved:
			// The Cards it names must still be in force and held by this
			// subject, read here under their locks. Revocation and moves
			// withdraw it when they commit; expiry has no write of its
			// own, so it is withdrawn here, before anything is decided.
			held := make(map[string]Mandate, len(ms))
			for _, m := range ms {
				held[m.ID] = m
			}
			if reason := grantStillStands(ex, held, now); reason != "" {
				resolve(&ex, ExceptionInvalidated, "", now)
				ex.Reason = reason
				if err := tx.SaveException(ctx, ex); err != nil {
					return Decision{}, fmt.Errorf("%w: %v", ErrUsageUnavailable, err)
				}
			}
		}
	}

	if found && ex.Status == ExceptionHeld && ex.LegacyApproval != nil && ex.ResolvedAt == nil {
		d := ownerRequired(ex)
		d.Reason = ex.Reason
		return d, nil
	}

	d, err := Evaluate(ms, req, usage, now)
	if err != nil {
		return Decision{}, err
	}
	if d.Allowed() && d.Outcome != OutcomeNotCovered {
		// It fits the Cards as they stand: the owner changed a Card, or
		// the window reset since it came home. It draws like any other
		// authorized action, and the exception leaves the owner's queue.
		if found && ex.Status != ExceptionChanged {
			resolve(&ex, ExceptionChanged, "", now)
			ex.Reason = "fits the card as it now stands"
		}
		if found {
			if err := tx.SaveException(ctx, ex); err != nil {
				return Decision{}, fmt.Errorf("%w: %v", ErrUsageUnavailable, err)
			}
		}
		return a.drawAll(ctx, tx, ms, nil, tenantID, proposalID, req, now, d)
	}
	if !ownerBreach(d.Outcome) || proposalID == "" {
		return d, nil
	}

	breaches, ok, err := Breaches(ms, req, usage, now)
	if err != nil {
		return Decision{}, err
	}
	if !ok || len(breaches) == 0 {
		return d, nil
	}

	// Refuse takes precedence: a Card whose owner chose "it stops"
	// refuses, and approving an exception can never waive it.
	byID := make(map[string]Mandate, len(ms))
	for _, m := range ms {
		byID[m.ID] = m
	}
	for _, b := range breaches {
		if !byID[b.MandateID].ComesHome() {
			return Decision{Outcome: b.Outcome, MandateID: b.MandateID, Quantity: b.Quantity,
				Limit: b.Limit, Used: b.Used, Draw: b.Draw, Reason: b.Reason}, nil
		}
	}

	if found && ex.Status == ExceptionApproved {
		// Approve once, used now. It still stands only if every Card it
		// named is in force and held by this subject (read under their
		// locks), and nothing outside the approved set is breached.
		if reason := grantStillStands(ex, byID, now); reason != "" {
			ex.Status, ex.Reason = ExceptionInvalidated, reason
		} else if reviewedThresholds(breaches, ex.Breaches) && cumulativeBreaches(breaches) {
			// Approval reserved no room. Keep the reviewed answer unconsumed;
			// the locked redemption must wait for a deliberate revision or
			// normal window aging, never waive the total or send anything.
			return approvedNoRoom(ex, breaches), nil
		} else if reviewedThresholds(breaches, ex.Breaches) {
			return a.redeem(ctx, tx, ms, ex, breaches, tenantID, proposalID, req, now)
		}
	}

	// Hold it for the owner. Observe mode reports without holding: the
	// caller executes anyway, so there is nothing to answer.
	expires := holdExpiry(breaches, byID, now)
	e := Exception{
		ID: ExceptionID(tenantID, proposalID), TenantID: tenantID, ProposalID: proposalID,
		Subject: req.Subject, Action: req.Action, EntityID: req.EntityID, Fingerprint: fp,
		Request: req, Breaches: breaches, Status: ExceptionHeld, CreatedAt: now, ExpiresAt: &expires,
		AccountingVersion: CumulativeAccountingVersion,
	}
	if req.Observe {
		return ownerRequired(e), nil
	}
	if found && (ex.Status == ExceptionHeld || ex.Status == ExceptionApproved) {
		// A retry of a held action: it keeps its place and its expiry, so
		// retrying never buys more time.
		e.CreatedAt, e.ExpiresAt = ex.CreatedAt, ex.ExpiresAt
		e.LegacyApproval = ex.LegacyApproval
	}
	if found {
		if ex.Status == ExceptionHeld && ex.ExternalAnswer != nil && ex.ExternalAnswer.Decision == "defer" && ReviewState(e) == ReviewState(ex) {
			// An identical retry is still the review the service deferred.
			// Keep its evidence; it grants no authority and buys no time.
			e.ExternalAnswer = ex.ExternalAnswer
		} else {
			preserveApproval(&ex)
		}
		e.PriorApprovals = ex.PriorApprovals
		e.LegacyApproval = ex.LegacyApproval
	}
	if e.Expired(now) {
		expire(&e)
	}
	if err := tx.SaveException(ctx, e); err != nil {
		return Decision{}, fmt.Errorf("%w: hold exception for proposal %s: %v", ErrUsageUnavailable, proposalID, err)
	}
	if e.Status == ExceptionExpired {
		return ownerExpired(e), nil
	}
	d = ownerRequired(e)
	d.NewlyHeld = !(found && ex.Status == ExceptionHeld)
	if d.NewlyHeld {
		d.NextSteps = NextStepsFor(e, ms)
	}
	return d, nil
}

// drawAll writes the draws for every covering Card not in skip, and
// returns d. The same all-or-nothing, same-Cards rule as always.
func (a *Authorizer) drawAll(ctx context.Context, tx CardTx, ms []Mandate, skip map[string]bool, tenantID, proposalID string, req Request, now time.Time, d Decision) (Decision, error) {
	draws := drawsFor(ms, skip, tenantID, proposalID, req, now)
	if len(draws) > 0 {
		if a.beforeWrite != nil {
			a.beforeWrite()
		}
		if err := tx.RecordAll(ctx, draws); err != nil {
			if errors.Is(err, ErrProposalSpent) {
				// Lost a race to another authorization under the same
				// id, which the lookup above could not yet see.
				return Decision{
					Outcome: OutcomeProposalReused,
					Reason:  fmt.Sprintf("proposal id %s was authorized by another request at the same time; a new authorization needs a new proposal id", proposalID),
				}, nil
			}
			return Decision{}, fmt.Errorf("%w: record draws for proposal %s: %v", ErrUsageUnavailable, proposalID, err)
		}
	}
	d.Consumed = true
	return d, nil
}

// redeem uses a reviewed per-action waiver once. Every applicable Card
// draws its full quantities; exception entries record provenance only.
func (a *Authorizer) redeem(ctx context.Context, tx CardTx, ms []Mandate, ex Exception, breaches []Breach, tenantID, proposalID string, req Request, now time.Time) (Decision, error) {
	breached := breachedIDs(breaches)
	ex.Covering = nil
	for _, m := range ms {
		if appliesTo(m, req) && m.Active(now) {
			ex.Covering = append(ex.Covering, m.ID)
		}
	}
	draws := drawsFor(ms, nil, tenantID, proposalID, req, now)
	var entries []ExceptionEntry
	for _, draw := range draws {
		if !breached[draw.MandateID] {
			continue
		}
		entries = append(entries, ExceptionEntry{TenantID: tenantID, ExceptionID: ex.ID, MandateID: draw.MandateID,
			Subject: req.Subject, ProposalID: proposalID, QuantityKey: draw.QuantityKey, Amount: draw.Amount, RecordedAt: now,
			Action: req.Action, EntityID: req.EntityID, AccountingVersion: CumulativeAccountingVersion})
	}
	if a.beforeWrite != nil {
		a.beforeWrite()
	}
	if err := tx.Redeem(ctx, ex, draws, entries, now); err != nil {
		if errors.Is(err, ErrProposalSpent) {
			return proposalReusedForException(proposalID), nil
		}
		return Decision{}, fmt.Errorf("%w: redeem exception %s: %v", ErrUsageUnavailable, ex.ID, err)
	}
	d := redeemedDecision(ex)
	d.Consumed = true // this request turned the approval into a draw
	return d, nil
}

// drawsFor is one draw per aggregate of every covering, in-force Card
// not in skip: exactly the Cards the decision was made with, so a Card for another domain, environment or state never draws.
func drawsFor(ms []Mandate, skip map[string]bool, tenantID, proposalID string, req Request, now time.Time) []Draw {
	var draws []Draw
	indexes := map[string]int{}
	for _, m := range ms {
		if skip[m.ID] || !appliesTo(m, req) || !m.Active(now) {
			continue
		}
		for _, agg := range m.Aggregates {
			amount := DrawFor(agg.Quantity, req.Parameters)
			if amount == 0 {
				continue
			}
			terms := chargedLimit(agg, m, now)
			id := key(m.ID, agg.Quantity.Key())
			if i, exists := indexes[id]; exists {
				draws[i].ChargedLimits = append(draws[i].ChargedLimits, terms)
				if draws[i].Measure != terms.Measure {
					draws[i].Measure = ""
				}
				if draws[i].Window != terms.Window {
					draws[i].Window, draws[i].WindowStart = "", time.Time{}
				}
				continue
			}
			indexes[id] = len(draws)
			draws = append(draws, Draw{
				MandateID:         m.ID,
				TenantID:          tenantID,
				Subject:           req.Subject,
				ProposalID:        proposalID,
				QuantityKey:       agg.Quantity.Key(),
				Amount:            amount,
				DrawnAt:           now,
				Action:            req.Action,
				EntityID:          req.EntityID,
				Measure:           agg.EffectiveMeasure(),
				AccountingVersion: CumulativeAccountingVersion,
				Window:            agg.Window,
				WindowStart:       terms.WindowStart,
				ChargedLimits:     []ChargedLimit{terms},
			})
		}
	}
	return draws
}

// grantStillStands checks, under the Card locks, that every Card an
// approval named is still in force and held by the requesting subject.
// ms is what this subject holds now; a Card moved away is missing from
// it. "" means the approval stands; otherwise the invalidation reason.
func grantStillStands(ex Exception, held map[string]Mandate, now time.Time) string {
	for _, id := range ex.MandateIDs() {
		m, ok := held[id]
		switch {
		case !ok:
			return "holder_changed"
		case m.RevokedAt != nil && !now.Before(*m.RevokedAt):
			return "card_revoked"
		case !m.Active(now):
			return "card_expired"
		}
	}
	return ""
}

func redeemedDecision(ex Exception) Decision {
	if ex.AccountingVersion >= CumulativeAccountingVersion {
		return Decision{Outcome: OutcomeWithinLimit, ExceptionID: ex.ID, Breaches: ex.Breaches, Covering: ex.Covering,
			Reason: "approved once by the Card's owner; full quantities count against every applicable cumulative budget"}
	}
	return Decision{Outcome: OutcomeWithinLimit, ExceptionID: ex.ID, Breaches: ex.Breaches, Covering: ex.MandateIDs(),
		Reason: "approved once by the card's owner, outside the card; the card's balance is unchanged"}
}

// withdrawnDecision is a retry of a call its agent withdrew.
func withdrawnDecision(e Exception) Decision {
	return Decision{Outcome: OutcomeWithdrawn, ExceptionID: e.ID, Breaches: e.Breaches,
		Reason: "its agent withdrew this call when its run was cancelled; nothing was authorized"}
}

// ownerExpired is the decision for a hold nobody answered in time.
func ownerExpired(e Exception) Decision {
	d := Decision{Outcome: OutcomeOwnerExpired, ExceptionID: e.ID, Breaches: e.Breaches, ExceptionExpiresAt: e.ExpiresAt,
		Reason: "nobody answered this action before its hold expired; nothing was authorized"}
	if e.ExpiresAt != nil {
		d.Reason = "nobody answered this action before its hold expired at " + e.ExpiresAt.UTC().Format(time.RFC3339) +
			"; nothing was authorized"
	}
	return d
}

func proposalReusedForException(proposalID string) Decision {
	return Decision{Outcome: OutcomeProposalReused,
		Reason: fmt.Sprintf("proposal id %s was already decided for a different request; a new action needs a new proposal id", proposalID)}
}

// Covers reports whether the request's subject holds any card granting
// its action, in any entity state and in force or not. It records
// nothing, so the decide path can ask it before the idempotency cache
// and before the entity's state is known: an unbound credential must
// not act as an agent that holds a card for the action, whether this
// proposal would draw on it or a replay would return a decision that
// did.
//
// An error means coverage could not be established; the caller must
// refuse, as for Authorize.
func (a *Authorizer) Covers(ctx context.Context, tenantID string, req Request) (bool, error) {
	if a == nil {
		return false, nil
	}
	ms, err := a.store.ForSubject(ctx, tenantID, req.Subject)
	if err != nil {
		return false, fmt.Errorf("%w: load mandates: %v", ErrUsageUnavailable, err)
	}
	return AnyGrantsAction(ms, req), nil
}

// lockAttempts bounds how often discovery restarts when the Card set
// keeps changing under it. Past that the answer cannot be established,
// and the check fails closed.
const lockAttempts = 3

// lockCards returns the subject's Cards read under their per-Card
// locks, and the release for those locks. An empty set holds no lock.
func (a *Authorizer) lockCards(ctx context.Context, tenantID, subject string) ([]Mandate, func(), error) {
	noop := func() {}
	for attempt := 0; attempt < lockAttempts; attempt++ {
		found, err := a.store.ForSubject(ctx, tenantID, subject)
		if err != nil {
			return nil, noop, fmt.Errorf("%w: load mandates: %v", ErrUsageUnavailable, err)
		}
		if len(found) == 0 {
			return nil, noop, nil
		}
		if a.afterDiscover != nil {
			a.afterDiscover()
		}
		release, err := a.locker.AcquireAll(ctx, cardLockKeys(tenantID, found))
		if err != nil {
			return nil, noop, fmt.Errorf("%w: lock: %v", ErrUsageUnavailable, err)
		}
		locked, err := a.store.ForSubject(ctx, tenantID, subject)
		if err != nil {
			release()
			return nil, noop, fmt.Errorf("%w: reload mandates: %v", ErrUsageUnavailable, err)
		}
		if sameCards(found, locked) {
			return locked, release, nil
		}
		release()
	}
	return nil, noop, fmt.Errorf("%w: the cards for %s kept changing while being locked", ErrUsageUnavailable, subject)
}

func cardLockKeys(tenantID string, ms []Mandate) []string {
	keys := make([]string, 0, len(ms))
	for _, m := range ms {
		keys = append(keys, CardLockKey(tenantID, m.ID))
	}
	return keys
}

// sameCards reports whether two reads found the same Card ids.
func sameCards(a, b []Mandate) bool {
	if len(a) != len(b) {
		return false
	}
	ids := make(map[string]bool, len(a))
	for _, m := range a {
		ids[m.ID] = true
	}
	for _, m := range b {
		if !ids[m.ID] {
			return false
		}
	}
	return true
}
