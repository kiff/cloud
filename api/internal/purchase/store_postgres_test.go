package purchase

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kiff/cloud/api/internal/testdb"
)

func testStore(t *testing.T) (*Store, *time.Time) {
	t.Helper()
	url := testdb.URL(t)
	if url == "" {
		t.Skip("set " + testdb.EnvVar + " to run Postgres tests")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := ApplySchema(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `TRUNCATE purchase.cases, purchase.room, purchase.authorizations, purchase.accounts, purchase.events`); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 28, 9, 15, 0, 0, time.UTC)
	s := &Store{Pool: pool, Now: func() time.Time { return now }}
	if err := s.SetAccount(ctx, "t_acme", "acct_acme"); err != nil {
		t.Fatal(err)
	}
	return s, &now
}

// newCase is the contract's case under another id (and, for a second
// purchase on the same Card, another checkout).
func newCase(t *testing.T, id string) Case {
	c, _ := contractCase(t)
	c.CaseID = id
	c.Reference.Checkout = "chk_" + id
	return c
}

func open(t *testing.T, s *Store, c Case) string {
	t.Helper()
	d, err := s.Open(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

var owner = Decision{By: ByOwner, Signature: "sig_1", ApproverRole: "owner", Card: CardRef{ID: "card_9f2c", Version: 4}, Limits: Limits{PerDay: 20000}}

// A stale answer is refused: another version or another digest.
func TestADecisionNamesTheCurrentVersionAndDigest(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	c := newCase(t, "pc_stale")
	d1 := open(t, s, c)
	c2 := c
	c2.Version = 2
	c2.Delivery.Line1 = "1 other street"
	d2 := open(t, s, c2)
	if err := s.Decide(ctx, c.Tenant, c.CaseID, 1, d1, owner); !errors.Is(err, ErrStale) {
		t.Fatalf("the old version: %v", err)
	}
	if err := s.Decide(ctx, c.Tenant, c.CaseID, 2, d1, owner); !errors.Is(err, ErrStale) {
		t.Fatalf("the old digest: %v", err)
	}
	if err := s.Decide(ctx, c.Tenant, c.CaseID, 2, d2, owner); err != nil {
		t.Fatal(err)
	}
	if r, _ := s.Get(ctx, c.Tenant, c.CaseID, 1); r.State != StateSuperseded {
		t.Fatalf("version 1: %s", r.State)
	}
}

// Two decisions racing for a Card's last room: one wins, the other's case
// stays held, and no room is spent twice.
func TestTwoPurchasesForTheLastRoomReserveItOnce(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	limits := owner
	limits.Limits.PerDay = 10000 // room for one €86.20 case, not two
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		c := newCase(t, fmt.Sprintf("pc_race_%d", i))
		d := open(t, s, c)
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = s.Decide(ctx, c.Tenant, c.CaseID, 1, d, limits)
		}(i)
	}
	wg.Wait()
	won, taken := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			won++
		case errors.Is(err, ErrRoomTaken):
			taken++
		default:
			t.Fatal(err)
		}
	}
	if won != 1 || taken != 1 {
		t.Fatalf("won %d, room taken %d", won, taken)
	}
}

// Within limits: the per-purchase limit, the category, and a count limit, so
// a loop of small buys can't drain the day.
func TestWithinLimitsChecksEachLimitAndTheCount(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	within := Decision{By: ByCardLimits, Card: CardRef{ID: "card_9f2c", Version: 4},
		Limits: Limits{PerPurchase: 10000, PerDay: 100000, PerHourCount: 2, Categories: []string{"5943"}}}
	for i := 0; i < 3; i++ {
		c := newCase(t, fmt.Sprintf("pc_count_%d", i))
		d := open(t, s, c)
		err := s.Decide(ctx, c.Tenant, c.CaseID, 1, d, within)
		if i < 2 && err != nil {
			t.Fatalf("purchase %d: %v", i, err)
		}
		if i == 2 && !errors.Is(err, ErrOutsideLimits) {
			t.Fatalf("the third in an hour: %v", err)
		}
	}
	over := within
	over.Limits.PerPurchase = 5000
	c := newCase(t, "pc_over")
	if err := s.Decide(ctx, c.Tenant, c.CaseID, 1, open(t, s, c), over); !errors.Is(err, ErrOutsideLimits) {
		t.Fatalf("over the per-purchase limit: %v", err)
	}
	other := within
	other.Limits.Categories = []string{"5651"}
	c = newCase(t, "pc_category")
	if err := s.Decide(ctx, c.Tenant, c.CaseID, 1, open(t, s, c), other); !errors.Is(err, ErrOutsideLimits) {
		t.Fatalf("another category: %v", err)
	}
}

func decided(t *testing.T, s *Store, id string) Case {
	t.Helper()
	c := newCase(t, id)
	if err := s.Decide(context.Background(), c.Tenant, c.CaseID, 1, open(t, s, c), owner); err != nil {
		t.Fatal(err)
	}
	return c
}

// The same case executed twice at once gives one execution; the second
// caller gets the first's state.
func TestTheSameCaseExecutedTwiceAtOnceGivesOneExecution(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	c := decided(t, s, "pc_lease")
	var wg sync.WaitGroup
	taken := make([]bool, 2)
	for i := range taken {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, ok, err := s.Lease(ctx, c.Tenant, c.CaseID, 1, fmt.Sprintf("exe_%d", i))
			if err != nil {
				t.Error(err)
			}
			taken[i] = ok
		}(i)
	}
	wg.Wait()
	if taken[0] == taken[1] {
		t.Fatalf("leases taken: %v", taken)
	}
}

// An unknown outcome blocks every retry until it's reconciled: as not
// ordered, the same case goes on once; as ordered, never again.
func TestAnUnknownOutcomeBlocksARetryUntilReconciled(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	for _, ordered := range []bool{false, true} {
		c := decided(t, s, fmt.Sprintf("pc_unknown_%v", ordered))
		if _, ok, _ := s.Lease(ctx, c.Tenant, c.CaseID, 1, "exe_1"); !ok {
			t.Fatal("first lease")
		}
		if err := s.Finish(ctx, c.Tenant, c.CaseID, 1, "exe_1", OutcomeUnknown, ""); err != nil {
			t.Fatal(err)
		}
		if r, ok, _ := s.Lease(ctx, c.Tenant, c.CaseID, 1, "exe_2"); ok || r.State != StateUnknown {
			t.Fatalf("a retry while unknown: %v %s", ok, r.State)
		}
		if err := s.Reconcile(ctx, c.Tenant, c.CaseID, 1, ordered); err != nil {
			t.Fatal(err)
		}
		r, ok, _ := s.Lease(ctx, c.Tenant, c.CaseID, 1, "exe_2")
		if ordered && (ok || r.State != StateDone) {
			t.Fatalf("reconciled as ordered, then a retry: %v %s", ok, r.State)
		}
		if !ordered && !ok {
			t.Fatalf("reconciled as not ordered, the retry: %s", r.State)
		}
	}
}

// An unknown outcome with an authorization standing never returns to
// approved: it ends, not bought.
func TestAnUnknownCaseWithAStandingAuthorizationEnds(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	c := decided(t, s, "pc_standing")
	s.Lease(ctx, c.Tenant, c.CaseID, 1, "exe_1")
	if err := s.AttachCard(ctx, c.Tenant, c.CaseID, 1, "ic_standing"); err != nil {
		t.Fatal(err)
	}
	if a := s.Authorize(ctx, auth("iauth_s", "ic_standing", 8420)); !a.Approved {
		t.Fatalf("authorization: %+v", a)
	}
	s.Finish(ctx, c.Tenant, c.CaseID, 1, "exe_1", OutcomeUnknown, "")
	if err := s.Reconcile(ctx, c.Tenant, c.CaseID, 1, false); err != nil {
		t.Fatal(err)
	}
	if r, _ := s.Get(ctx, c.Tenant, c.CaseID, 1); r.State != StateNotBought {
		t.Fatalf("state %s", r.State)
	}
}

func auth(id, card string, amount int64) Auth {
	return Auth{ID: id, Card: card, Account: "acct_acme", Amount: amount, Currency: "EUR", MerchantName: "PAPETERIE LUMIERE LYON", MerchantCountry: "FR",
		Category: "stationery_office_supplies_printing_and_writing_paper", Verification: "exempted"}
}

func executing(t *testing.T, s *Store, id, card string) Case {
	t.Helper()
	ctx := context.Background()
	c := decided(t, s, id)
	if _, ok, err := s.Lease(ctx, c.Tenant, c.CaseID, 1, "exe_"+id); !ok || err != nil {
		t.Fatalf("lease: %v %v", ok, err)
	}
	if err := s.AttachCard(ctx, c.Tenant, c.CaseID, 1, card); err != nil {
		t.Fatal(err)
	}
	return c
}

// The live authorization approves only the matching case, once: a second
// authorization, another amount, currency, shop or category, an hour later,
// or an unknown card is declined; a repeat of the same id answers the same.
func TestTheLiveAuthorizationApprovesOnlyTheMatchingCaseOnce(t *testing.T) {
	s, now := testStore(t)
	ctx := context.Background()
	executing(t, s, "pc_auth", "ic_auth")
	if a := s.Authorize(ctx, auth("iauth_1", "ic_auth", 8570)); !a.Approved {
		t.Fatalf("the matching authorization: %+v", a)
	}
	if a := s.Authorize(ctx, auth("iauth_1", "ic_auth", 8570)); !a.Approved {
		t.Fatalf("its repeat: %+v", a)
	}
	if a := s.Authorize(ctx, auth("iauth_2", "ic_auth", 100)); a.Approved || a.Reason != DeclineTwice {
		t.Fatalf("a second: %+v", a)
	}
	inc := auth("iauth_3", "ic_auth", 100)
	inc.Incremental = true
	if a := s.Authorize(ctx, inc); a.Approved || a.Reason != DeclineAmount {
		t.Fatalf("an incremental past the maximum: %+v", a)
	}
	executing(t, s, "pc_auth_b", "ic_b")
	for name, a := range map[string]Auth{
		"over the maximum":  auth("x1", "ic_b", 8621),
		"another currency":  func() Auth { a := auth("x2", "ic_b", 100); a.Currency = "GBP"; return a }(),
		"another shop":      func() Auth { a := auth("x3", "ic_b", 100); a.MerchantName = "LOOKALIKE PAPETERIE"; return a }(),
		"another country":   func() Auth { a := auth("x4", "ic_b", 100); a.MerchantCountry = "BE"; return a }(),
		"another category":  func() Auth { a := auth("x5", "ic_b", 100); a.Category = "clothing_stores"; return a }(),
		"a required check":  func() Auth { a := auth("x6", "ic_b", 100); a.Verification = "required"; return a }(),
		"an unknown card":   auth("x7", "ic_nobody", 100),
		"incremental first": func() Auth { a := auth("x8", "ic_b", 100); a.Incremental = true; return a }(),
		"another account":   func() Auth { a := auth("x10", "ic_b", 100); a.Account = "acct_other"; return a }(),
	} {
		if got := s.Authorize(ctx, a); got.Approved {
			t.Errorf("%s: approved", name)
		}
	}
	*now = now.Add(16 * time.Minute)
	if a := s.Authorize(ctx, auth("x9", "ic_b", 100)); a.Approved || a.Reason != DeclineExpired {
		t.Fatalf("after 15 minutes: %+v", a)
	}
}

// Two authorizations on one card at once: one is approved.
func TestTwoAuthorizationsAtOnceAreApprovedOnce(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	executing(t, s, "pc_twice", "ic_twice")
	var wg sync.WaitGroup
	answers := make([]Answer, 2)
	for i := range answers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			answers[i] = s.Authorize(ctx, auth(fmt.Sprintf("iauth_t%d", i), "ic_twice", 8420))
		}(i)
	}
	wg.Wait()
	if answers[0].Approved == answers[1].Approved {
		t.Fatalf("answers: %+v", answers)
	}
}

// An approved case unused in its time ends, its room released.
func TestAnUnusedCaseExpiresAndReleasesItsRoom(t *testing.T) {
	s, now := testStore(t)
	ctx := context.Background()
	c := decided(t, s, "pc_expire")
	*now = now.Add(15*time.Minute + time.Second)
	if n, err := s.Expire(ctx); err != nil || n != 1 {
		t.Fatalf("expired %d %v", n, err)
	}
	r, _ := s.Get(ctx, c.Tenant, c.CaseID, 1)
	var used int64
	s.Pool.QueryRow(ctx, `SELECT used FROM purchase.room WHERE card_id = $1`, c.Card.ID).Scan(&used)
	if r.State != StateExpired || r.Ended != EndedNotUsedInTime || used != 0 {
		t.Fatalf("state %s ended %s room used %d", r.State, r.Ended, used)
	}
}

// Two within-limits decisions at once on a Card's last count: one passes,
// the other is held for the owner, never both.
func TestTwoWithinLimitsDecisionsAtOnceCountEachOther(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	within := Decision{By: ByCardLimits, Card: CardRef{ID: "card_9f2c", Version: 4},
		Limits: Limits{PerPurchase: 10000, PerDay: 1000000, PerHourCount: 1, Categories: []string{"5943"}}}
	for round := 0; round < 5; round++ {
		if _, err := s.Pool.Exec(ctx, `TRUNCATE purchase.cases, purchase.room`); err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		errs := make([]error, 2)
		for i := range errs {
			c := newCase(t, fmt.Sprintf("pc_counted_%d_%d", round, i))
			d := open(t, s, c)
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				errs[i] = s.Decide(ctx, c.Tenant, c.CaseID, 1, d, within)
			}(i)
		}
		wg.Wait()
		passed := 0
		for _, err := range errs {
			if err == nil {
				passed++
			} else if !errors.Is(err, ErrOutsideLimits) {
				t.Fatal(err)
			}
		}
		if passed != 1 {
			t.Fatalf("round %d: %d passed a count of 1: %v", round, passed, errs)
		}
	}
}

// An execution whose holder never finished becomes unknown past its lease.
func TestADeadLeaseHolderLeavesTheCaseUnknown(t *testing.T) {
	s, now := testStore(t)
	ctx := context.Background()
	c := decided(t, s, "pc_dead")
	if _, ok, _ := s.Lease(ctx, c.Tenant, c.CaseID, 1, "exe_dead"); !ok {
		t.Fatal("lease")
	}
	if n, _ := s.SweepLeases(ctx); n != 0 {
		t.Fatalf("swept a live lease: %d", n)
	}
	*now = now.Add(16 * time.Minute)
	if n, err := s.SweepLeases(ctx); err != nil || n != 1 {
		t.Fatalf("swept %d %v", n, err)
	}
	if r, ok, _ := s.Lease(ctx, c.Tenant, c.CaseID, 1, "exe_2"); ok || r.State != StateUnknown {
		t.Fatalf("after the sweep: %v %s", ok, r.State)
	}
}

// Not ordered with an authorization standing is unknown, and its room
// stays reserved.
func TestNotOrderedWithMoneyAuthorizedIsUnknown(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	c := executing(t, s, "pc_money_stands", "ic_money")
	if a := s.Authorize(ctx, auth("iauth_m", "ic_money", 8420)); !a.Approved {
		t.Fatalf("authorization %+v", a)
	}
	if err := s.Finish(ctx, c.Tenant, c.CaseID, 1, "exe_pc_money_stands", OutcomeNotOrdered, EndedAddressChanged); err != nil {
		t.Fatal(err)
	}
	r, _ := s.Get(ctx, c.Tenant, c.CaseID, 1)
	if r.State != StateUnknown || r.Reserved != c.Price.Max {
		t.Fatalf("state %s reserved %d", r.State, r.Reserved)
	}
}

// A hold is linked once to a held case, and its answer's line is that
// case version's.
func TestAHoldNamesItsCase(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	c := newCase(t, "pc_hold")
	digest := open(t, s, c)
	if err := s.Hold(ctx, c.Tenant, c.CaseID, 1, "exc_1"); err != nil {
		t.Fatal(err)
	}
	if err := s.Hold(ctx, c.Tenant, c.CaseID, 1, "exc_2"); !errors.Is(err, ErrNotHeld) {
		t.Fatalf("a second hold: %v", err)
	}
	r, err := s.ByException(ctx, c.Tenant, "exc_1")
	if err != nil || r.Case.CaseID != c.CaseID || r.Digest != digest {
		t.Fatalf("by hold: %+v %v", r, err)
	}
	if _, err := s.ByException(ctx, "t_other", "exc_1"); !errors.Is(err, ErrNoCase) {
		t.Fatalf("another tenant's hold: %v", err)
	}
	if AnswerLine(r.Case.CaseID, r.Case.Version, r.Digest) != "purchase:pc_hold:1:"+digest {
		t.Fatal("line")
	}
}

// The owner's approval reserves room and counts, past the within-limits
// day: a within-limits purchase afterwards sees that room used.
func TestTheOwnersApprovalCountsWithoutTheWithinLimitsCeiling(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	big := newCase(t, "pc_owner_big")
	byOwner := Decision{By: ByOwner, Signature: "exc_1", Card: CardRef{ID: "card_9f2c", Version: 4}}
	if err := s.Decide(ctx, big.Tenant, big.CaseID, 1, open(t, s, big), byOwner); err != nil {
		t.Fatalf("the owner's approval past the day: %v", err)
	}
	within := Decision{By: ByCardLimits, Card: CardRef{ID: "card_9f2c", Version: 4}, Limits: Limits{PerPurchase: 10000, PerDay: 10000, PerHourCount: 5, Categories: []string{"5943"}}}
	small := newCase(t, "pc_within_after")
	if err := s.Decide(ctx, small.Tenant, small.CaseID, 1, open(t, s, small), within); !errors.Is(err, ErrRoomTaken) {
		t.Fatalf("within limits after the owner used the day: %v", err)
	}
}
