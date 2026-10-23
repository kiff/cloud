package mandate

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"slices"
	"sort"
	"time"
)

// ErrNotFound is returned when a mandate id is unknown to the store.
var ErrNotFound = errors.New("mandate: not found")
var ErrActive = errors.New("mandate: active cards cannot be archived")

// Draw is one consumption of authority, recorded when a proposal is
// authorized.
//
// ProposalID is the idempotency anchor. A retried proposal carrying the
// same id must not consume the limit twice, and because the draw is
// keyed on it that falls out of an upsert rather than needing separate
// dedup logic.
type Draw struct {
	MandateID string
	TenantID  string
	// Subject is the agent whose action drew: on a shared Card, which of
	// its holders. Draws recorded before Cards could be shared carry the
	// holder the Card had when the column was added.
	Subject     string
	ProposalID  string
	QuantityKey string
	Amount      int64
	DrawnAt     time.Time

	// Action and EntityID are what the draw was for, recorded so a
	// statement can say "ISSUE_REFUND on ord_8812" rather than showing
	// a proposal id and asking the reader to go and look it up. A
	// statement of movements nobody can recognise is a log, not a
	// statement.
	//
	// Empty on draws recorded before this existed; the surface renders
	// that as an unnamed movement rather than inventing one.
	Action   string
	EntityID string
	// Original accounting terms, fixed at authorization. Legacy draws keep
	// version zero and are never eligible for inferred effect compensation.
	Measure           Measure
	AccountingVersion int
	Window            Window
	WindowStart       time.Time
	// ChargedLimits retains each original limit independently when one
	// quantity has multiple windows or measures. The quantity is drawn once.
	ChargedLimits []ChargedLimit
}

type ChargedLimit struct {
	Measure     Measure
	Window      Window
	WindowStart time.Time
	// Fixed at authorization, including the original calendar timebase.
	// Compensation must never infer these from a later Card revision.
	WindowEnd time.Time
	Location  string
}

// Revision is one issuance of a mandate, recorded as it happened.
//
// A mandate row is upserted in place, so re-issuing an id to change a
// ceiling overwrites what it used to be. Without this the product
// could not say what the authority was yesterday, or who changed it —
// and "more room is an owner's decision" is not a claim you can make
// about a change that leaves no trace.
//
// It stores the state *after* the change rather than a diff. Two
// consecutive revisions are the before and after of one decision, and
// a snapshot cannot drift out of step with the row the way a
// separately-written diff can.
type Revision struct {
	TenantID  string
	MandateID string
	Number    int64
	At        time.Time
	By        string
	// Subject is who held the Card after this issuance. Empty on
	// revisions recorded before holders were recorded; a change of
	// holder between two revisions is a move.
	Subject string
	// Holders is every holder after this issuance, Subject first; empty
	// on revisions recorded before Cards could be shared.
	Holders []string
	// OnExceed is the Card's policy for work outside it after this
	// issuance; empty on revisions recorded before the policy existed.
	OnExceed   string
	Grants     []Grant
	Aggregates []Aggregate
	ValidUntil time.Time
}

// ErrProposalSpent is returned by RecordAll when the proposal id has
// already authorized a draw in the tenant. Nothing is written. Authorize
// turns it into OutcomeProposalReused: it is the atomic half of the
// spent-id rule, for the request that lost a race the lookup could not
// see.
var ErrProposalSpent = errors.New("mandate: proposal id already authorized a draw")

// ErrCardRevised: a Card was revised since the terms being issued were
// shown, so they would overwrite a revision nobody reviewed with them.
var ErrCardRevised = errors.New("mandate: the Card was revised since this change was made")

// Store holds mandates and the draws against them.
//
// Every method is tenant-scoped at the call boundary: an implementation
// must make it impossible to read one tenant's mandates or usage from
// another's request, rather than filtering after the fact.
type Store interface {
	// The owner's side of held exceptions, and the exception methods
	// the authorizer uses through CardTx (the in-process path passes
	// the store itself as its CardTx).
	ExceptionStore
	GetException(ctx context.Context, tenantID, proposalID string) (Exception, bool, error)
	SaveException(ctx context.Context, e Exception) error
	Redeem(ctx context.Context, e Exception, draws []Draw, entries []ExceptionEntry, at time.Time) error

	// Issue records a mandate and the revision that produced it.
	//
	// The caller has already validated it against the domain's action
	// contracts. `by` names the management identity that issued it and
	// is recorded on the revision; an empty string is accepted rather
	// than refused, because failing to attribute a change is not a
	// reason to lose the change.
	//
	// The mandate and its revision are written together: a ceiling
	// that changed with no revision beside it is the gap this exists
	// to close.
	Issue(ctx context.Context, m Mandate, by string) error

	// IssueAll issues several Cards of one tenant as Issue does, in one
	// transaction holding all their locks: all of them, or none. A
	// version's Cards are revised together this way (kiff/tafs#107).
	// expect, when set, is each revised Card's latest revision number as
	// the owner was shown it: read under the locks, a Card that moved on
	// refuses the whole issue with ErrCardRevised. retire are Cards revoked
	// from at in the same transaction, under the same locks, as Revoke does
	// (expect can name them too): a version going back drops the Cards it
	// didn't hold. ErrNotFound for one that doesn't exist.
	IssueAll(ctx context.Context, ms []Mandate, by string, expect map[string]int64, retire []string, at time.Time) error

	// Revisions returns every issuance of one mandate, oldest first.
	// Append-only: nothing rewrites or removes a revision.
	Revisions(ctx context.Context, tenantID, mandateID string) ([]Revision, error)

	// Revoke marks a mandate withdrawn from at. Revoking an already
	// revoked mandate is not an error — revocation is a state, not an
	// event, and an operator retrying under uncertainty must not be
	// punished for it.
	Revoke(ctx context.Context, tenantID, mandateID string, at time.Time) error

	// Move gives a Card to another holder, keeping its id, terms, usage
	// and statement, and records a revision naming the new holder. It
	// reads and writes the Card under its lock. ErrNotFound,
	// ErrMoveInactive or ErrMoveSameHolder when it refuses. It is
	// SetHolders with a one-agent list.
	Move(ctx context.Context, tenantID, mandateID, to, by string, now time.Time) (Mandate, error)

	// SetHolders replaces the Card's holders: adding, removing and replacing are this one
	// operation, under the Card's lock, recorded as a revision. Exceptions
	// held or approved for an agent that no longer holds the Card are
	// invalidated (holder_changed); everyone else's stay. ErrNotFound,
	// ErrMoveInactive, ErrMoveSameHolder (the list did not change) or
	// ErrInvalidMandate (a bad list) when it refuses.
	//
	// expected is the holder list the change was built from, compared
	// with the current one under the lock: if they differ the change is
	// refused with ErrHoldersChanged, so two owners editing the list at
	// once cannot silently undo each other. nil skips the
	// check; only Move passes nil, because "this agent alone" does not
	// depend on who held the Card before.
	SetHolders(ctx context.Context, tenantID, mandateID string, holders, expected []string, by string, now time.Time) (Mandate, error)

	// Archive removes a revoked or expired mandate from the working
	// inventory without deleting its statement or audit history.
	Archive(ctx context.Context, tenantID, mandateID string, at time.Time) error

	// ForSubject returns every mandate naming a subject, in force or
	// not.
	//
	// Deliberately not filtered to the active ones. Evaluate has to see
	// a revoked or expired mandate in order to report it as such; a
	// store that filtered first would make a lapsed mandate look like
	// no mandate, which turns an expiry into a widening of authority.
	ForSubject(ctx context.Context, tenantID, subject string) ([]Mandate, error)

	// List returns every mandate for a tenant, in force or not, newest
	// first. Used by the statement surface, which has to show a
	// revoked mandate rather than hiding what happened under it.
	List(ctx context.Context, tenantID string) ([]Mandate, error)

	// Used totals the draws against one mandate and quantity since an
	// instant, ignoring any draw already recorded for
	// excludeProposalID.
	//
	// The exclusion is what makes a retry idempotent. A retried
	// proposal carries the same id, and counting its own earlier draw
	// against it would refuse the retry of something already
	// authorized. Excluding it means the retry recomputes the identical
	// answer and re-records the identical rows, which also repairs a
	// draw that was only partly written before a failure.
	//
	// It must return an error rather than zero when it cannot answer: a
	// zero would read as "plenty of room left".
	Used(ctx context.Context, tenantID, mandateID, quantityKey string, since time.Time, excludeProposalID string) (int64, error)

	// ProposalDraws returns every draw a proposal id recorded in the
	// tenant, on any mandate, whenever drawn. Authorize uses it to
	// refuse a spent proposal id. An error, not an empty result, when
	// it cannot answer.
	ProposalDraws(ctx context.Context, tenantID, proposalID string) ([]Draw, error)

	// Record writes a draw. Idempotent on (tenant, mandate, proposal):
	// the same proposal recorded twice consumes the limit once.
	Record(ctx context.Context, d Draw) error

	// RecordAll writes every draw one authorization produced, all or
	// none, and claims their proposal id for the tenant in the same
	// atomic step. If the id is already claimed it writes nothing and
	// returns ErrProposalSpent. All draws passed share one tenant and
	// one proposal id.
	//
	// One proposal can draw against several aggregates and several
	// covering mandates. Written one at a time, a failure partway
	// leaves the earlier draws standing while the proposal itself is
	// refused — a movement on a statement for an action that never
	// ran, marked authorized, which is the one thing a statement must
	// never say. The batch is atomic so the ledger holds all of an
	// authorization or none of it.
	RecordAll(ctx context.Context, draws []Draw) error

	// Draws returns the draws against one mandate since an instant,
	// oldest first, for the statement.
	Draws(ctx context.Context, tenantID, mandateID string, since time.Time) ([]Draw, error)
}

// Locker serialises the read-then-write around one mandate.
//
// Evaluating a limit and recording the draw is a read-modify-write, so
// two proposals arriving together could both read the same usage and
// both be allowed, taking the total past the limit. The window for that
// is small and the consequence is exactly the thing a limit exists to
// prevent, so the critical section is held under a per-mandate lock.
//
// idempotency.AdvisoryLocker already implements this shape over a
// dedicated Postgres pool; it is an interface here so the package does
// not depend on a database to be tested.
type Locker interface {
	Acquire(ctx context.Context, key string) (release func(), err error)
	// AcquireAll takes every key's lock at once, in a stable order, and
	// releases them together. Used for the per-Card locks a decision
	// needs.
	AcquireAll(ctx context.Context, keys []string) (release func(), err error)
}

// CardLockKey is the lock every read that authorizes against a Card and
// every write that changes what it authorizes must hold: draws, issue
// and reissue (which can change the holder), moves and revocation.
// Keyed by the Card, not its holder, so a move cannot let the old and
// new holder take different locks over the same balance.
func CardLockKey(tenantID, mandateID string) string {
	return "mandate-card:" + tenantID + ":" + mandateID
}

// NoLock is the single-process default. Safe for tests and a single
// API instance; not safe across replicas, which is why production
// passes the advisory locker.
type NoLock struct{}

func (NoLock) Acquire(context.Context, string) (func(), error) { return func() {}, nil }

func (NoLock) AcquireAll(context.Context, []string) (func(), error) { return func() {}, nil }

// CardTx is the store as seen from inside a transaction that holds a set
// of Cards' locks: the authoritative re-read, the usage it evaluates and
// the draws it records all happen on that one transaction.
type CardTx interface {
	ForSubject(ctx context.Context, tenantID, subject string) ([]Mandate, error)
	Used(ctx context.Context, tenantID, mandateID, quantityKey string, since time.Time, excludeProposalID string) (int64, error)
	RecordAll(ctx context.Context, draws []Draw) error

	// GetException returns the exception held for a proposal, if any.
	GetException(ctx context.Context, tenantID, proposalID string) (Exception, bool, error)
	// SaveException creates or replaces an exception, keyed by id.
	SaveException(ctx context.Context, e Exception) error
	// Redeem claims the proposal, charges every applicable Card, appends
	// exception provenance and marks the approval consumed, all or nothing.
	// Version-zero entries preserve the historical outside-balance model.
	Redeem(ctx context.Context, e Exception, draws []Draw, entries []ExceptionEntry, at time.Time) error
}

// ExceptionStore is the owner's side of held exceptions.
type ExceptionStore interface {
	// Exceptions lists a tenant's exceptions, newest first; an empty
	// status lists all of them.
	Exceptions(ctx context.Context, tenantID string, status ExceptionStatus) ([]Exception, error)
	// Exception returns one exception by id, or ErrExceptionNotFound.
	Exception(ctx context.Context, tenantID, id string) (Exception, error)
	// ApproveOnce, Reject and ChangeCard answer a held exception under
	// the locks of every Card it names. ChangeCard issues the smallest
	// change to each Card that lets the action fit (see ChangeFor),
	// recorded as revisions, and leaves the exception to be re-decided.
	ApproveOnce(ctx context.Context, tenantID, id, by string, now time.Time) (Exception, error)
	// Reject may carry a Refusal: a next step one of the named Cards
	// offers (ErrNextStepNotOnCard otherwise) and a note for the agent.
	Reject(ctx context.Context, tenantID, id, by string, now time.Time, r Refusal) (Exception, error)
	ChangeCard(ctx context.Context, tenantID, id, by string, now time.Time) (Exception, error)
	// Withdraw is an agent taking back one of its own held calls,
	// when the run that made it is cancelled: the hold stops waiting and
	// can no longer be answered, so nothing is forwarded. Only the agent
	// that made the call can (ErrExceptionNotFound for any other). A held
	// call, or one approved but not yet collected (nothing was sent; the
	// approval is kept in its history), can be; ErrExceptionState once it
	// was sent, refused or re-decided, ErrExceptionExpired once expired.
	// Withdrawing it again returns it unchanged.
	Withdraw(ctx context.Context, tenantID, id, agent string, now time.Time) (Exception, error)
	// ExceptionEntries returns authority used outside a Card since an
	// instant, for the statement's separate total.
	ExceptionEntries(ctx context.Context, tenantID, mandateID string, since time.Time) ([]ExceptionEntry, error)
}

// HoldExpirer records the expiry of held calls nobody answered, so
// it can be told as it happens: expiry has no write of its own otherwise
// (the first retry after it records it).
type HoldExpirer interface {
	// ExpireHeld returns, across tenants and at most limit, the calls whose
	// expiry is at or before before and whose expiry hasn't been told:
	// held ones, which it marks expired, and ones a retry already recorded
	// as expired. Each is returned once. One whose expiry is more than
	// ExpiryTellWindow before before is recorded expired and marked told,
	// but not returned: it isn't announced late.
	ExpireHeld(ctx context.Context, before time.Time, limit int) ([]Exception, error)
}

// CardLocker is implemented by a store that can lock Cards inside the
// transaction that writes the ledger.
// The authorizer uses it when available: a lock held on a separate
// session can be lost while the write still commits, and then two
// holders can spend the same balance. PostgresStore implements it.
type CardLocker interface {
	WithCards(ctx context.Context, tenantID string, mandateIDs []string, fn func(CardTx) error) error
}

// Why a move was refused.
var (
	ErrMoveInactive   = errors.New("mandate: card is revoked or outside its validity window")
	ErrMoveSameHolder = errors.New("mandate: card is already held by that subject")
	// ErrHoldersChanged: the Card's holders are no longer the list the
	// change was built from.
	ErrHoldersChanged = errors.New("mandate: the card's holders changed since they were read")
)

// checkSetHolders is the rule both stores apply before changing a Card's
// holders.
func checkSetHolders(m Mandate, holders, expected []string, now time.Time) error {
	if !m.Active(now) {
		return ErrMoveInactive
	}
	if expected != nil && !slices.Equal(m.HolderList(), expected) {
		return ErrHoldersChanged
	}
	if len(holders) == 0 {
		return fmt.Errorf("%w: a Card needs at least one holder", ErrInvalidMandate)
	}
	if err := validateHolders(m.withHolders(holders)); err != nil {
		return err
	}
	if slices.Equal(m.HolderList(), holders) {
		return ErrMoveSameHolder
	}
	return nil
}

// sortedCardLockIDs hashes Card lock keys to advisory-lock ids, drops
// duplicates and sorts them, so every transaction takes a given set of
// Card locks in the same order and two cannot deadlock.
func sortedCardLockIDs(keys []string) []int64 {
	seen := make(map[int64]bool, len(keys))
	ids := make([]int64, 0, len(keys))
	for _, k := range keys {
		h := fnv.New64a()
		_, _ = h.Write([]byte(k))
		id := int64(h.Sum64())
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}
