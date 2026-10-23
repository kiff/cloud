package mandate_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/mandate"
)

func seeded(t *testing.T, m mandate.Mandate) *mandate.MemoryStore {
	t.Helper()
	s := mandate.NewMemoryStore()
	if err := s.Issue(context.Background(), m, "tester"); err != nil {
		t.Fatalf("Issue: %v", err)
	}
	return s
}

func at(base time.Time) func() time.Time { return func() time.Time { return base } }

// The draw is recorded as part of authorizing, so a sequence of
// individually-compliant proposals cannot exceed the limit in total.
func TestDrawsAccumulateAcrossProposals(t *testing.T) {
	s := seeded(t, spendMandate(10000)) // €100
	a := mandate.NewAuthorizer(s, nil, at(base))
	ctx := context.Background()

	for i := 1; i <= 4; i++ {
		d, err := a.Authorize(ctx, "t1", fmt.Sprintf("prop-%d", i),
			req("agent://refund-agent", "REFUND_INVOICE", "PAID", map[string]int64{"amount_cents": 2500}))
		if err != nil {
			t.Fatalf("proposal %d: %v", i, err)
		}
		if !d.Allowed() {
			t.Fatalf("proposal %d refused early: %+v", i, d)
		}
	}
	d, err := a.Authorize(ctx, "t1", "prop-5",
		req("agent://refund-agent", "REFUND_INVOICE", "PAID", map[string]int64{"amount_cents": 1}))
	if err != nil {
		t.Fatalf("proposal 5: %v", err)
	}
	if d.Allowed() {
		t.Fatalf("the limit was exhausted; proposal 5 must be refused: %+v", d)
	}
	if d.Used != 10000 {
		t.Errorf("used = %d, want 10000", d.Used)
	}
}

func TestUncountableDrawRecordsNothing(t *testing.T) {
	store := seeded(t, spendMandate(5000))
	a := mandate.NewAuthorizer(store, nil, at(base))
	r := req("agent://refund-agent", "REFUND_INVOICE", "PAID", nil)
	r.Uncountable = map[string]bool{"amount_cents": true}
	d, err := a.Authorize(context.Background(), "t1", "uncountable-prop", r)
	if err != nil || d.Outcome != mandate.OutcomeUncountableDraw {
		t.Fatalf("decision = %+v, err = %v", d, err)
	}
	draws, err := store.ProposalDraws(context.Background(), "t1", "uncountable-prop")
	if err != nil || len(draws) != 0 {
		t.Fatalf("draws = %v, err = %v", draws, err)
	}
}

// A proposal id reaching Authorize again consumes the limit once. The
// decide path answers a genuine retry from its recorded decision before
// it gets here; one that does get here is refused as spent, and
// either way the draw is recorded once.
func TestRetryingAProposalDrawsOnce(t *testing.T) {
	s := seeded(t, spendMandate(10000))
	a := mandate.NewAuthorizer(s, nil, at(base))
	ctx := context.Background()
	r := req("agent://refund-agent", "REFUND_INVOICE", "PAID", map[string]int64{"amount_cents": 6000})

	if d, err := a.Authorize(ctx, "t1", "prop-same", r); err != nil || !d.Allowed() {
		t.Fatalf("first: err=%v decision=%+v", err, d)
	}
	for i := 0; i < 2; i++ {
		if d, err := a.Authorize(ctx, "t1", "prop-same", r); err != nil || d.Outcome != mandate.OutcomeProposalReused {
			t.Fatalf("attempt %d: err=%v decision=%+v, want proposal_reused", i, err, d)
		}
	}
	used, err := s.Used(ctx, "t1", "mdt-1", "sum(amount_cents)", base.Add(-time.Hour), "")
	if err != nil {
		t.Fatalf("Used: %v", err)
	}
	if used != 6000 {
		t.Errorf("used = %d after three identical attempts, want 6000", used)
	}
}

// Concurrent proposals must not both pass a limit only one fits under.
// This is the read-modify-write the lock exists for; with NoLock and a
// single process the mutex inside MemoryStore is not enough, because
// the gap is between the read and the write.
func TestConcurrentProposalsCannotBothPassOneSlot(t *testing.T) {
	s := seeded(t, spendMandate(10000))
	// A real deployment passes idempotency.AdvisoryLocker here; a
	// mutex is the same contract in one process.
	a := mandate.NewAuthorizer(s, &mutexLocker{}, at(base))
	ctx := context.Background()

	const n = 8
	var wg sync.WaitGroup
	allowed := make([]bool, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			d, err := a.Authorize(ctx, "t1", fmt.Sprintf("prop-%d", i),
				req("agent://refund-agent", "REFUND_INVOICE", "PAID", map[string]int64{"amount_cents": 6000}))
			allowed[i] = err == nil && d.Allowed()
		}(i)
	}
	wg.Wait()

	got := 0
	for _, ok := range allowed {
		if ok {
			got++
		}
	}
	if got != 1 {
		t.Fatalf("%d of %d concurrent proposals were allowed against a limit that fits one", got, n)
	}
	used, _ := s.Used(ctx, "t1", "mdt-1", "sum(amount_cents)", base.Add(-time.Hour), "")
	if used > 10000 {
		t.Errorf("recorded usage %d exceeds the limit 10000", used)
	}
}

// A store that cannot answer must refuse, never allow.
func TestAuthorizeFailsClosedWhenUsageIsUnavailable(t *testing.T) {
	s := seeded(t, spendMandate(10000))
	s.FailUsageWith(errors.New("postgres is unreachable"))
	a := mandate.NewAuthorizer(s, nil, at(base))

	_, err := a.Authorize(context.Background(), "t1", "prop-1",
		req("agent://refund-agent", "REFUND_INVOICE", "PAID", map[string]int64{"amount_cents": 1}))
	if !errors.Is(err, mandate.ErrUsageUnavailable) {
		t.Fatalf("err = %v, want ErrUsageUnavailable", err)
	}
}

// Revocation stops future draws immediately.
func TestRevocationStopsFurtherAuthorization(t *testing.T) {
	s := seeded(t, spendMandate(1000000))
	a := mandate.NewAuthorizer(s, nil, at(base))
	ctx := context.Background()
	r := req("agent://refund-agent", "REFUND_INVOICE", "PAID", map[string]int64{"amount_cents": 100})

	if d, err := a.Authorize(ctx, "t1", "p1", r); err != nil || !d.Allowed() {
		t.Fatalf("before revocation: err=%v d=%+v", err, d)
	}
	if err := s.Revoke(ctx, "t1", "mdt-1", base.Add(-time.Second)); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	d, err := a.Authorize(ctx, "t1", "p2", r)
	if err != nil {
		t.Fatalf("after revocation: %v", err)
	}
	if d.Allowed() {
		t.Fatalf("a revoked mandate authorized an action: %+v", d)
	}
}

// Revoking twice must not extend authority to the later instant.
func TestRevokingTwiceKeepsTheEarlierInstant(t *testing.T) {
	s := seeded(t, spendMandate(1000))
	ctx := context.Background()
	early, late := base.Add(-2*time.Hour), base.Add(-time.Minute)
	if err := s.Revoke(ctx, "t1", "mdt-1", early); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if err := s.Revoke(ctx, "t1", "mdt-1", late); err != nil {
		t.Fatalf("Revoke again: %v", err)
	}
	all, _ := s.List(ctx, "t1")
	if len(all) != 1 || all[0].RevokedAt == nil || !all[0].RevokedAt.Equal(early) {
		t.Errorf("RevokedAt = %v, want the earlier instant %v", all[0].RevokedAt, early)
	}
}

// A nil authorizer is the "no mandates in this deployment" case and
// must not refuse anything.
func TestNilAuthorizerIsANoOp(t *testing.T) {
	var a *mandate.Authorizer
	d, err := a.Authorize(context.Background(), "t1", "p1",
		req("agent://x", "REFUND_INVOICE", "PAID", nil))
	if err != nil || !d.Allowed() {
		t.Fatalf("err=%v d=%+v, want an allowed no-op", err, d)
	}
	if mandate.NewAuthorizer(nil, nil, nil) != nil {
		t.Error("NewAuthorizer(nil store) must return nil")
	}
}

// One tenant's usage must never be visible to another.
func TestUsageIsTenantScoped(t *testing.T) {
	s := seeded(t, spendMandate(10000))
	other := spendMandate(10000)
	other.TenantID = "t2"
	if err := s.Issue(context.Background(), other, "tester"); err != nil {
		t.Fatalf("Issue: %v", err)
	}
	a := mandate.NewAuthorizer(s, nil, at(base))
	ctx := context.Background()
	r := req("agent://refund-agent", "REFUND_INVOICE", "PAID", map[string]int64{"amount_cents": 9000})

	if d, err := a.Authorize(ctx, "t1", "p1", r); err != nil || !d.Allowed() {
		t.Fatalf("t1: err=%v d=%+v", err, d)
	}
	// t2 shares the mandate id and has spent nothing of its own.
	if d, err := a.Authorize(ctx, "t2", "p2", r); err != nil || !d.Allowed() {
		t.Fatalf("t2 was charged for t1's draw: err=%v d=%+v", err, d)
	}
}

type mutexLocker struct{ mu sync.Mutex }

func (l *mutexLocker) Acquire(context.Context, string) (func(), error) {
	l.mu.Lock()
	return l.mu.Unlock, nil
}

func (l *mutexLocker) AcquireAll(context.Context, []string) (func(), error) {
	l.mu.Lock()
	return l.mu.Unlock, nil
}

// One proposal can draw against several aggregates. Written one at a
// time, a failure partway leaves the earlier draws standing while the
// proposal is refused — a movement on a statement, marked authorized,
// for an action that never was.
func TestDrawsOfOneAuthorizationAreAllOrNothing(t *testing.T) {
	ctx := context.Background()
	s := mandate.NewMemoryStore()
	if err := s.Issue(ctx, mandate.Mandate{
		ID: "mdt-1", TenantID: "t1", Subject: "agent://a",
		Grants: []mandate.Grant{{Action: "REFUND_ORDER"}},
		Aggregates: []mandate.Aggregate{
			{Quantity: mandate.Quantity{Kind: mandate.KindCount}, Limit: 10, Window: mandate.WindowCalendarDay},
			{Quantity: mandate.Quantity{Kind: mandate.KindSum, Parameter: "amount"}, Limit: 1000, Window: mandate.WindowCalendarDay},
		},
	}, "tester"); err != nil {
		t.Fatalf("issue: %v", err)
	}
	s.FailRecordWith(errors.New("ledger unavailable"))

	a := mandate.NewAuthorizer(s, mandate.NoLock{}, func() time.Time { return time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC) })
	_, err := a.Authorize(ctx, "t1", "p1", mandate.Request{
		Subject: "agent://a", Action: "REFUND_ORDER",
		Parameters: map[string]int64{"amount": 400},
	})
	if err == nil {
		t.Fatal("a ledger that could not record the draws still authorized the action")
	}

	// Nothing partial left behind: the statement must not carry a
	// movement for a proposal that was refused.
	draws, derr := s.Draws(ctx, "t1", "mdt-1", time.Time{})
	if derr != nil {
		t.Fatalf("draws: %v", derr)
	}
	if len(draws) != 0 {
		t.Errorf("a refused authorization left %d movement(s) on the statement", len(draws))
	}
}

// Covers answers "does this agent hold a card for this action" without
// touching it: no draw is recorded, any entity state counts (the decide
// path asks before state is known), and a revoked card still counts, so
// an unbound key cannot use the revocation to act in the holder's name.
func TestCoversReadsWithoutDrawing(t *testing.T) {
	s := seeded(t, spendMandate(10000))
	a := mandate.NewAuthorizer(s, nil, at(base))
	ctx := context.Background()
	paid := req("agent://refund-agent", "REFUND_INVOICE", "PAID", map[string]int64{"amount_cents": 2500})

	for _, tc := range []struct {
		name string
		r    mandate.Request
		want bool
	}{
		{"holder, covered action and state", paid, true},
		{"another agent", req("agent://other", "REFUND_INVOICE", "PAID", nil), false},
		{"an action the card does not grant", req("agent://refund-agent", "VOID_INVOICE", "PAID", nil), false},
		{"a state the card does not grant still counts", req("agent://refund-agent", "REFUND_INVOICE", "DRAFT", nil), true},
	} {
		got, err := a.Covers(ctx, "t1", tc.r)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got != tc.want {
			t.Errorf("%s: Covers = %v, want %v", tc.name, got, tc.want)
		}
	}
	if used, _ := s.Used(ctx, "t1", "mdt-1", spendMandate(0).Aggregates[0].Quantity.Key(), base.Add(-48*time.Hour), ""); used != 0 {
		t.Errorf("Covers drew %d; it must record nothing", used)
	}

	if err := s.Revoke(ctx, "t1", "mdt-1", base.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got, _ := a.Covers(ctx, "t1", paid); !got {
		t.Error("a revoked card must still count as covering its holder's action")
	}

	var nilAuth *mandate.Authorizer
	if got, err := nilAuth.Covers(ctx, "t1", paid); got || err != nil {
		t.Errorf("nil authorizer: Covers = %v, %v; want false, nil", got, err)
	}
}

// A proposal id that drew on a card is spent once the reuse
// window has passed. Reusing it must not produce a fresh allowed, and
// must not refresh the recorded draw; a new id draws anew.
func TestSpentProposalIDCannotAuthorizeAgain(t *testing.T) {
	testSpentProposalID(t, mandate.NewMemoryStore())
}

func testSpentProposalID(t *testing.T, s mandate.Store) {
	t.Helper()
	ctx := context.Background()
	m := spendMandate(5000)
	m.TenantID = tenantOf(t, s)
	if err := s.Issue(ctx, m, "tester"); err != nil {
		t.Fatalf("Issue: %v", err)
	}
	clock := base
	a := mandate.NewAuthorizer(s, nil, func() time.Time { return clock })
	r := req("agent://refund-agent", "REFUND_INVOICE", "PAID", map[string]int64{"amount_cents": 5000})

	if d, err := a.Authorize(ctx, m.TenantID, "prop-A", r); err != nil || !d.Allowed() {
		t.Fatalf("first authorization: %+v %v", d, err)
	}

	// prop-A is spent: the same request moments later, a changed amount
	// or entity, or the same request a day later — none authorizes.
	bigger := req("agent://refund-agent", "REFUND_INVOICE", "PAID", map[string]int64{"amount_cents": 1})
	bigger.EntityID = "inv-other"
	for _, c := range []struct {
		name string
		at   time.Time
		r    mandate.Request
	}{
		{"same request", base.Add(time.Minute), r},
		{"changed amount and entity", base.Add(time.Minute), bigger},
		{"after a day", base.Add(25 * time.Hour), r},
	} {
		clock = c.at
		d, err := a.Authorize(ctx, m.TenantID, "prop-A", c.r)
		if err != nil {
			t.Fatal(err)
		}
		if d.Allowed() || d.Outcome != mandate.OutcomeProposalReused {
			t.Fatalf("%s: outcome %s, want proposal_reused", c.name, d.Outcome)
		}
	}

	draws, err := s.Draws(ctx, m.TenantID, m.ID, base.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(draws) != 1 || draws[0].Amount != 5000 || !draws[0].DrawnAt.Equal(base) {
		t.Fatalf("the original draw changed: %+v", draws)
	}

	// A new id is a new authorization and meets the saturated limit.
	clock = base.Add(time.Minute)
	d, err := a.Authorize(ctx, m.TenantID, "prop-B", r)
	if err != nil {
		t.Fatal(err)
	}
	if d.Outcome != mandate.OutcomeLimitReached {
		t.Errorf("new id on a saturated card: outcome %s, want limit_reached", d.Outcome)
	}

	// Spent even once the card no longer covers it.
	if err := s.Revoke(ctx, m.TenantID, m.ID, base.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	clock = base.Add(3 * time.Minute)
	if d, err := a.Authorize(ctx, m.TenantID, "prop-A", r); err != nil || d.Outcome != mandate.OutcomeProposalReused {
		t.Errorf("after revocation: %+v %v, want proposal_reused", d, err)
	}
}

// tenantOf gives the memory store its fixture tenant; the Postgres
// variant seeds and returns its own.
func tenantOf(t *testing.T, s mandate.Store) string {
	t.Helper()
	if _, ok := s.(*mandate.MemoryStore); ok {
		return "t1"
	}
	return pgSpentTenant
}

// A spent id is spent tenant-wide: a subject that holds no cards at all
// cannot reuse it either, or not_covered would hand it a fresh allowed
// on any action that does not require a card.
func TestSpentProposalIDIsSpentForASubjectWithNoCards(t *testing.T) {
	s := seeded(t, spendMandate(10000))
	a := mandate.NewAuthorizer(s, nil, at(base))
	ctx := context.Background()

	if d, err := a.Authorize(ctx, "t1", "prop-A",
		req("agent://refund-agent", "REFUND_INVOICE", "PAID", map[string]int64{"amount_cents": 100})); err != nil || !d.Allowed() {
		t.Fatalf("holder: %+v %v", d, err)
	}
	d, err := a.Authorize(ctx, "t1", "prop-A",
		req("agent://no-cards", "REFUND_INVOICE", "PAID", map[string]int64{"amount_cents": 100}))
	if err != nil {
		t.Fatal(err)
	}
	if d.Allowed() || d.Outcome != mandate.OutcomeProposalReused {
		t.Errorf("subject with no cards reusing a spent id: outcome %s, want proposal_reused", d.Outcome)
	}
	// An unused id for that subject is still simply not covered.
	if d, _ := a.Authorize(ctx, "t1", "prop-B",
		req("agent://no-cards", "REFUND_INVOICE", "PAID", nil)); d.Outcome != mandate.OutcomeNotCovered {
		t.Errorf("fresh id, no cards: outcome %s, want not_covered", d.Outcome)
	}
}

// Concurrent authorizations under one new proposal id, with no lock at
// all (NoLock, as when the caller's outer lock is unavailable): exactly
// one is allowed and draws; every other is proposal_reused. The claim
// RecordAll makes is what holds, not the lock.
func TestConcurrentSameIDAuthorizesOnce(t *testing.T) {
	testConcurrentSameID(t, mandate.NewMemoryStore())
}

func testConcurrentSameID(t *testing.T, s mandate.Store) {
	t.Helper()
	ctx := context.Background()
	m := spendMandate(1_000_000)
	m.TenantID = tenantOf(t, s)
	m.ID = "mdt-concurrent"
	if err := s.Issue(ctx, m, "tester"); err != nil {
		t.Fatal(err)
	}
	a := mandate.NewAuthorizer(s, nil, at(base)) // nil locker: NoLock
	r := req("agent://refund-agent", "REFUND_INVOICE", "PAID", map[string]int64{"amount_cents": 100})

	const n = 16
	var wg sync.WaitGroup
	outcomes := make([]mandate.Outcome, n)
	errs := make([]error, n)
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			d, err := a.Authorize(ctx, m.TenantID, "prop-race", r)
			outcomes[i], errs[i] = d.Outcome, err
		}(i)
	}
	close(start)
	wg.Wait()

	allowed := 0
	for i := range outcomes {
		if errs[i] != nil {
			t.Fatalf("attempt %d: %v", i, errs[i])
		}
		switch outcomes[i] {
		case mandate.OutcomeWithinLimit:
			allowed++
		case mandate.OutcomeProposalReused:
		default:
			t.Errorf("attempt %d: outcome %s", i, outcomes[i])
		}
	}
	if allowed != 1 {
		t.Errorf("%d concurrent authorizations allowed under one id, want 1", allowed)
	}
	draws, err := s.Draws(ctx, m.TenantID, m.ID, base.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(draws) != 1 {
		t.Errorf("draws = %d, want 1", len(draws))
	}
}

// An empty request domain is not a wildcard. A card scoped to a
// domain does not cover, or draw for, a request whose domain is unknown
// or different; it does for its own domain.
func TestDomainScopedCardNeedsAMatchingRequestDomain(t *testing.T) {
	s := seeded(t, spendMandate(10000)) // Domain "billing"
	a := mandate.NewAuthorizer(s, nil, at(base))
	ctx := context.Background()
	r := req("agent://refund-agent", "REFUND_INVOICE", "PAID", map[string]int64{"amount_cents": 100})

	for _, d := range []string{"", "payouts"} {
		r.Domain = d
		dec, err := a.Authorize(ctx, "t1", "prop-"+d, r)
		if err != nil {
			t.Fatal(err)
		}
		if dec.Outcome != mandate.OutcomeNotCovered {
			t.Errorf("domain %q: outcome %s, want not_covered", d, dec.Outcome)
		}
		if covered, _ := a.Covers(ctx, "t1", r); covered {
			t.Errorf("domain %q: Covers = true, want false", d)
		}
	}
	if used, _ := s.Used(ctx, "t1", "mdt-1", spendMandate(0).Aggregates[0].Quantity.Key(), base.Add(-48*time.Hour), ""); used != 0 {
		t.Errorf("drew %d for requests outside the card's domain", used)
	}

	r.Domain = "billing"
	if dec, _ := a.Authorize(ctx, "t1", "prop-billing", r); !dec.Allowed() || dec.Outcome == mandate.OutcomeNotCovered {
		t.Errorf("own domain: outcome %s, want within limit", dec.Outcome)
	}
}

// The same for environments. A card scoped to one environment does
// not cover, or draw for, a request whose environment is unknown or
// different; it does for its own.
func TestEnvironmentScopedCardNeedsAMatchingRequestEnvironment(t *testing.T) {
	s := seeded(t, spendMandate(10000)) // Environment "prod"
	a := mandate.NewAuthorizer(s, nil, at(base))
	ctx := context.Background()
	r := req("agent://refund-agent", "REFUND_INVOICE", "PAID", map[string]int64{"amount_cents": 100})

	for _, env := range []string{"", "staging"} {
		r.Environment = env
		dec, err := a.Authorize(ctx, "t1", "prop-env-"+env, r)
		if err != nil {
			t.Fatal(err)
		}
		if dec.Outcome != mandate.OutcomeNotCovered {
			t.Errorf("environment %q: outcome %s, want not_covered", env, dec.Outcome)
		}
		if covered, _ := a.Covers(ctx, "t1", r); covered {
			t.Errorf("environment %q: Covers = true, want false", env)
		}
	}
	if used, _ := s.Used(ctx, "t1", "mdt-1", spendMandate(0).Aggregates[0].Quantity.Key(), base.Add(-48*time.Hour), ""); used != 0 {
		t.Errorf("drew %d for requests outside the card's environment", used)
	}

	r.Environment = "prod"
	if dec, _ := a.Authorize(ctx, "t1", "prop-env-prod", r); !dec.Allowed() || dec.Outcome == mandate.OutcomeNotCovered {
		t.Errorf("own environment: outcome %s, want within limit", dec.Outcome)
	}
}

// A card with no environment is not scoped by one: it applies whatever
// the request's environment, known or not.
func TestUnscopedEnvironmentCardAppliesInAnyEnvironment(t *testing.T) {
	m := spendMandate(10000)
	m.Environment = ""
	a := mandate.NewAuthorizer(seeded(t, m), nil, at(base))
	r := req("agent://refund-agent", "REFUND_INVOICE", "PAID", map[string]int64{"amount_cents": 100})
	for _, env := range []string{"", "prod", "staging"} {
		r.Environment = env
		if dec, _ := a.Authorize(context.Background(), "t1", "prop-any-"+env, r); dec.Outcome == mandate.OutcomeNotCovered {
			t.Errorf("environment %q: not_covered, want the card to apply", env)
		}
	}
}

// A card draws only if it is one Evaluate decided with. When one card
// allows a proposal, the subject's cards for another domain, another
// environment, or revoked ones must not draw, even though they grant the
// same action.
func TestOnlyCoveringCardsDraw(t *testing.T) {
	own := spendMandate(10000) // billing / prod
	otherDomain := spendMandate(10000)
	otherDomain.ID, otherDomain.Domain = "mdt-other-domain", "payouts"
	otherEnv := spendMandate(10000)
	otherEnv.ID, otherEnv.Environment = "mdt-other-env", "staging"
	revoked := spendMandate(10000)
	revoked.ID, revoked.ValidUntil = "mdt-revoked", base.Add(-time.Hour)

	s := seeded(t, own)
	for _, m := range []mandate.Mandate{otherDomain, otherEnv, revoked} {
		if err := s.Issue(context.Background(), m, "tester"); err != nil {
			t.Fatalf("issue %s: %v", m.ID, err)
		}
	}
	a := mandate.NewAuthorizer(s, nil, at(base))
	ctx := context.Background()
	r := req("agent://refund-agent", "REFUND_INVOICE", "PAID", map[string]int64{"amount_cents": 100})
	if dec, err := a.Authorize(ctx, "t1", "prop-only-covering", r); err != nil || !dec.Allowed() {
		t.Fatalf("authorize: %v %v", dec.Outcome, err)
	}
	key := own.Aggregates[0].Quantity.Key()
	for _, id := range []string{"mdt-other-domain", "mdt-other-env", "mdt-revoked"} {
		if used, _ := s.Used(ctx, "t1", id, key, base.Add(-48*time.Hour), ""); used != 0 {
			t.Errorf("%s drew %d for a proposal it does not cover", id, used)
		}
	}
	if used, _ := s.Used(ctx, "t1", "mdt-1", key, base.Add(-48*time.Hour), ""); used != 100 {
		t.Errorf("the covering card drew %d, want 100", used)
	}
}

// A card issued with its domain's immutable id matches that id,
// under whatever name the domain has now; another domain that carries
// the card's old name does not inherit it; an unknown id matches
// nothing. A card issued before ids were recorded matches by name.
func TestCardDomainIDSurvivesARename(t *testing.T) {
	byID := spendMandate(10000) // Domain "billing"
	byID.DomainID = "dom-billing"
	a := mandate.NewAuthorizer(seeded(t, byID), nil, at(base))
	ctx := context.Background()
	r := req("agent://refund-agent", "REFUND_INVOICE", "PAID", map[string]int64{"amount_cents": 100})

	for _, c := range []struct {
		name, domainName, domainID string
		covered                    bool
	}{
		{"renamed, same id", "billing-eu", "dom-billing", true},
		{"old name, other domain", "billing", "dom-other", false},
		{"unknown id", "billing", "", false},
	} {
		r.Domain, r.DomainID = c.domainName, c.domainID
		if covered, _ := a.Covers(ctx, "t1", r); covered != c.covered {
			t.Errorf("%s: covered = %v, want %v", c.name, covered, c.covered)
		}
	}

	legacy := mandate.NewAuthorizer(seeded(t, spendMandate(10000)), nil, at(base))
	r.Domain, r.DomainID = "billing", "dom-anything"
	if covered, _ := legacy.Covers(ctx, "t1", r); !covered {
		t.Error("a card with no recorded id must keep matching by name")
	}
}
