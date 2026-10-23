package mandate

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// TrialSuffix marks the holder of a trial's Cards (kiff/tafs#173). While
// its owner tries a version, the version's Cards are issued as trial Cards
// held by TrialSubject(agent), never by the agent: a call decided for the
// agent never sees them, and a trial call is decided on them alone, with
// their own counters.
const TrialSuffix = "~trial"

// TrialSubject is the holder of agent's trial Cards.
func TrialSubject(agent string) string { return agent + TrialSuffix }

// TrialAgent is the agent whose trial Cards subject holds, and whether
// subject is such a holder.
func TrialAgent(subject string) (string, bool) { return strings.CutSuffix(subject, TrialSuffix) }

// Trial decides req against ms as Authorize decides a fresh proposal, and
// records nothing: no draw, no held exception, nothing for an owner to
// answer (kiff/tafs#107). A trial of an agent's next version uses it to
// show what each of its writes would meet. Outside a Card set to come home
// is OutcomeOwnerRequired, with the breaches but no exception; outside a
// Card set to refuse is that Card's refusal, as in Authorize, where refuse
// takes precedence.
func Trial(ms []Mandate, req Request, usage Usage, now time.Time) (Decision, error) {
	d, err := Evaluate(ms, req, usage, now)
	if err != nil || d.Allowed() || !ownerBreach(d.Outcome) {
		return d, err
	}
	breaches, ok, err := Breaches(ms, req, usage, now)
	if err != nil {
		return Decision{}, err
	}
	if !ok || len(breaches) == 0 {
		return d, nil
	}
	byID := make(map[string]Mandate, len(ms))
	for _, m := range ms {
		byID[m.ID] = m
	}
	reasons := make([]string, 0, len(breaches))
	for _, b := range breaches {
		if !byID[b.MandateID].ComesHome() {
			return Decision{Outcome: b.Outcome, MandateID: b.MandateID, Quantity: b.Quantity,
				Limit: b.Limit, Used: b.Used, Draw: b.Draw, Reason: b.Reason}, nil
		}
		reasons = append(reasons, b.MandateID+": "+b.Reason)
	}
	b := breaches[0]
	return Decision{Outcome: OutcomeOwnerRequired, Breaches: breaches,
		MandateID: b.MandateID, Quantity: b.Quantity, Limit: b.Limit, Used: b.Used, Draw: b.Draw,
		Reason: "outside the card, would be held for its owner: " + strings.Join(reasons, "; ")}, nil
}

// TrialDraw is what a trial's allowed write would have drawn on one of a
// Card's limits. It is kept apart from the real ledger, under its trial
// run, so the trial's later writes see it as a live queue would.
type TrialDraw struct {
	MandateID   string
	QuantityKey string
	Amount      int64
	DrawnAt     time.Time
}

// TrialKeep is how long a trial's simulated draws are kept.
const TrialKeep = 48 * time.Hour

// TrialLedger keeps each trial run's simulated draws (kiff/tafs#107).
// WithTrial runs fn under the trial's lock, so its writes are decided one
// at a time, each on the draws of those before it.
type TrialLedger interface {
	WithTrial(ctx context.Context, tenantID, subject, trialID string, now time.Time, fn func(TrialDraws) error) error
}

// TrialDraws are one trial run's simulated draws, read and added under
// its lock.
type TrialDraws interface {
	Used(mandateID, quantityKey string, since time.Time) (int64, error)
	Add(draws []TrialDraw) error
}

// ErrNoTrialRun is a trial call that names no trial run: without one, its
// earlier writes can't be counted, so it is not decided.
var ErrNoTrialRun = errors.New("mandate: a trial call names its trial run")

// Trial decides req for trial run trialID against the subject's Cards as
// they stand, on their real usage plus the trial's own earlier allowed
// writes, and adds this write's draws to the trial when it is allowed. A
// held or refused write adds nothing, as in a live queue. Nothing real is
// drawn or held, so it takes no Card lock. An error means the answer
// could not be established; the caller treats it as a refusal.
func (a *Authorizer) Trial(ctx context.Context, tenantID, trialID string, req Request) (Decision, error) {
	return a.TrialWith(ctx, tenantID, trialID, req, nil, nil)
}

// TrialWith is Trial against the subject's Cards as a revision draft would
// leave them (kiff/tafs#107): each of cards replaces the subject's Card of
// the same id, its revision, or is added, a new Card, and each Card in
// retire is gone, as a restore leaves it. A revised Card keeps its id, so
// what it used so far counts, as it will once confirmed.
func (a *Authorizer) TrialWith(ctx context.Context, tenantID, trialID string, req Request, cards []Mandate, retire []string) (Decision, error) {
	if a == nil {
		return Decision{Outcome: OutcomeNotCovered}, nil
	}
	if trialID == "" {
		return Decision{}, ErrNoTrialRun
	}
	ledger, ok := a.store.(TrialLedger)
	if !ok {
		return Decision{}, fmt.Errorf("%w: this store keeps no trials", ErrUsageUnavailable)
	}
	ms, err := a.store.ForSubject(ctx, tenantID, req.Subject)
	if err != nil {
		return Decision{}, fmt.Errorf("%w: load mandates: %v", ErrUsageUnavailable, err)
	}
	ms = slices.DeleteFunc(slices.Clone(ms), func(m Mandate) bool { return slices.Contains(retire, m.ID) })
	for _, c := range cards {
		if i := slices.IndexFunc(ms, func(m Mandate) bool { return m.ID == c.ID }); i >= 0 {
			ms[i] = AsIssuedOver(c, &ms[i])
		} else {
			ms = append(ms, AsIssuedOver(c, nil))
		}
	}
	if len(ms) == 0 {
		return Decision{Outcome: OutcomeNotCovered}, nil
	}
	now := a.now()
	var d Decision
	err = ledger.WithTrial(ctx, tenantID, req.Subject, trialID, now, func(t TrialDraws) error {
		var err error
		d, err = Trial(ms, req, withTrialDraws(usageFor(ctx, a.store, tenantID, ""), t), now)
		if err != nil || d.Outcome != OutcomeWithinLimit {
			return err
		}
		var add []TrialDraw
		for _, dr := range drawsFor(ms, nil, tenantID, "", req, now) {
			add = append(add, TrialDraw{MandateID: dr.MandateID, QuantityKey: dr.QuantityKey, Amount: dr.Amount, DrawnAt: now})
		}
		return t.Add(add)
	})
	if err != nil {
		return Decision{}, fmt.Errorf("%w: trial draws: %v", ErrUsageUnavailable, err)
	}
	return d, nil
}

// withTrialDraws is real usage plus a trial's own simulated draws.
func withTrialDraws(real Usage, t TrialDraws) Usage {
	used := UsageFunc(func(id string, q Quantity, since time.Time) (int64, error) {
		n, err := real.Used(id, q, since)
		if err != nil {
			return 0, err
		}
		k, err := t.Used(id, q.Key(), since)
		return n + k, err
	})
	r, ok := real.(limitUsage)
	if !ok {
		return used
	}
	return limitUsage{Usage: used, read: func(m Mandate, agg Aggregate, now time.Time) (int64, error) {
		n, err := r.read(m, agg, now)
		if err != nil {
			return 0, err
		}
		since, err := agg.Window.Start(now, m.Loc())
		if err != nil {
			return 0, err
		}
		k, err := t.Used(m.ID, agg.Quantity.Key(), since)
		return n + k, err
	}}
}

// WithTrial runs fn on one trial's simulated draws, under the trials' lock.
func (s *MemoryStore) WithTrial(_ context.Context, tenantID, subject, trialID string, _ time.Time, fn func(TrialDraws) error) error {
	s.trialMu.Lock()
	defer s.trialMu.Unlock()
	if s.trials == nil {
		s.trials = map[string][]TrialDraw{}
	}
	k := key(tenantID, subject, trialID)
	t := &memoryTrial{draws: append([]TrialDraw(nil), s.trials[k]...)}
	if err := fn(t); err != nil {
		return err
	}
	s.trials[k] = t.draws
	return nil
}

type memoryTrial struct{ draws []TrialDraw }

func (t *memoryTrial) Used(mandateID, quantityKey string, since time.Time) (int64, error) {
	var n int64
	for _, d := range t.draws {
		if d.MandateID == mandateID && d.QuantityKey == quantityKey && !d.DrawnAt.Before(since) {
			n += d.Amount
		}
	}
	return n, nil
}

func (t *memoryTrial) Add(draws []TrialDraw) error {
	t.draws = append(t.draws, draws...)
	return nil
}

// WithTrial runs fn on one trial's simulated draws in a transaction
// holding the trial's lock, and drops the account's trial draws older than
// TrialKeep before now.
func (s *PostgresStore) WithTrial(ctx context.Context, tenantID, subject, trialID string, now time.Time, fn func(TrialDraws) error) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, "trial|"+tenantID+"|"+subject+"|"+trialID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM cloud_meta.trial_draws WHERE tenant_id = $1 AND drawn_at < $2`, tenantID, now.Add(-TrialKeep)); err != nil {
		return err
	}
	if err := fn(&postgresTrial{ctx: ctx, tx: tx, tenantID: tenantID, subject: subject, trialID: trialID}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

type postgresTrial struct {
	ctx                        context.Context
	tx                         pgx.Tx
	tenantID, subject, trialID string
}

func (t *postgresTrial) Used(mandateID, quantityKey string, since time.Time) (int64, error) {
	var n int64
	err := t.tx.QueryRow(t.ctx, `SELECT COALESCE(SUM(amount), 0) FROM cloud_meta.trial_draws
WHERE tenant_id = $1 AND trial_id = $2 AND subject = $3 AND mandate_id = $4 AND quantity_key = $5 AND drawn_at >= $6`,
		t.tenantID, t.trialID, t.subject, mandateID, quantityKey, since).Scan(&n)
	return n, err
}

func (t *postgresTrial) Add(draws []TrialDraw) error {
	for _, d := range draws {
		if _, err := t.tx.Exec(t.ctx, `INSERT INTO cloud_meta.trial_draws
(tenant_id, trial_id, subject, mandate_id, quantity_key, amount, drawn_at) VALUES ($1, $2, $3, $4, $5, $6, $7)`,
			t.tenantID, t.trialID, t.subject, d.MandateID, d.QuantityKey, d.Amount, d.DrawnAt); err != nil {
			return err
		}
	}
	return nil
}

// AsIssuedOver is c, a draft's Card, as issuing it would leave it over cur,
// the Card of its id in force (nil for a new one): a policy, hold expiry or
// time zone left to keep takes cur's (owner, the default and the account's
// for a new Card), and its totals' measures resolve, as Issue does. A trial of a draft, and what
// KIFF shows of one, read its Cards this way (kiff/tafs#107), so what
// they say matches what confirming it issues.
func AsIssuedOver(c Mandate, cur *Mandate) Mandate {
	c = resolvedMeasures(c)
	if c.OnExceed == OnExceedKeep || c.OnExceed == "" {
		c.OnExceed = OnExceedOwner
		if cur != nil {
			c.OnExceed = cur.OnExceed
		}
	}
	c.OnExceed = policyOf(c)
	if c.Location == "" {
		if cur != nil {
			c.Location = cur.Location
		} else {
			c.Location = c.DefaultLocation
		}
	}
	if c.HoldExpiry == HoldExpiryKeep {
		c.HoldExpiry = 0
		if cur != nil {
			c.HoldExpiry = cur.HoldExpiry
		}
	}
	return c
}
