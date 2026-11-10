package thune_test

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kiff/cloud/gateway/internal/invocation"
	"github.com/kiff/cloud/gateway/internal/testdb"
	"github.com/kiff/cloud/gateway/internal/thune"
)

var t0 = time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)

// processor is a fake payment processor: one account's charges and their
// refunds, as its test mode would answer. It counts every refund made, so
// a test can say nothing else moved.
type processor struct {
	mu      sync.Mutex
	charges map[string]*thune.Facts
	made    int
	// delay hides refunds younger than it from reads, as a processor's
	// list can lag its writes.
	delay  time.Duration
	madeAt map[string]time.Time
	now    func() time.Time
	reads  atomic.Int32
}

func newProcessor(now func() time.Time) *processor {
	return &processor{charges: map[string]*thune.Facts{
		"ch_1": {Account: "acct_1", Charge: "ch_1", Currency: "eur", Captured: 10000},
	}, madeAt: map[string]time.Time{}, now: now}
}

func (p *processor) ReadCharge(_ context.Context, _, account, charge string) (thune.Facts, error) {
	p.reads.Add(1)
	p.mu.Lock()
	defer p.mu.Unlock()
	c, ok := p.charges[charge]
	if !ok || c.Account != account {
		return thune.Facts{}, fmt.Errorf("no such charge")
	}
	f := *c
	f.Refunds = nil
	for _, r := range c.Refunds {
		if p.now().Sub(p.madeAt[r.ID]) >= p.delay {
			f.Refunds = append(f.Refunds, r)
		}
	}
	f.ReadAt = p.now()
	return f, nil
}

// refund makes a refund at the processor: KIFF's, with its marker, or the
// owner's in the dashboard, with none.
func (p *processor) refund(charge string, amount int64, marker string) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.made++
	id := fmt.Sprintf("re_%d", p.made)
	c := p.charges[charge]
	c.Refunds = append(c.Refunds, thune.Refund{ID: id, Amount: amount, Status: thune.RefundPending, Marker: marker})
	p.madeAt[id] = p.now()
	return id
}

func (p *processor) settle(refundID, status string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, c := range p.charges {
		for i := range c.Refunds {
			if c.Refunds[i].ID == refundID {
				c.Refunds[i].Status = status
			}
		}
	}
}

func (p *processor) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.made
}

// calls records which lost calls thune settled as sent.
type calls struct {
	mu    sync.Mutex
	found []string
}

func (c *calls) Found(_ context.Context, key string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.found = append(c.found, key)
	return nil
}

type env struct {
	store *thune.Store
	proc  *processor
	gate  *thune.Gate
	watch *thune.Watcher
	calls *calls
	now   *time.Time
	mu    *sync.Mutex
}

func newEnv(t *testing.T) *env {
	t.Helper()
	pool := testdb.Pool(t)
	ctx := context.Background()
	if err := invocation.ApplySchema(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if err := thune.ApplySchema(ctx, pool); err != nil {
		t.Fatal(err)
	}
	// Each test starts from empty tables: the database is the package's.
	if _, err := pool.Exec(ctx, `TRUNCATE gateway.thune_claims, gateway.thune_charge_locks, gateway.thune_adjustments, gateway.thune_refunds, gateway.thune_events, gateway.thune_watch_work`); err != nil {
		t.Fatal(err)
	}
	e := &env{mu: &sync.Mutex{}}
	now := t0
	e.now = &now
	clock := func() time.Time { e.mu.Lock(); defer e.mu.Unlock(); return *e.now }
	e.store = thune.NewStore(pool)
	e.store.SetClock(clock)
	e.proc = newProcessor(clock)
	e.calls = &calls{}
	e.gate = &thune.Gate{Store: e.store, Reader: e.proc}
	e.gate.Calls = e.calls
	e.watch = &thune.Watcher{Store: e.store, Calls: e.calls}
	return e
}

func (e *env) advance(d time.Duration) {
	e.mu.Lock()
	defer e.mu.Unlock()
	*e.now = e.now.Add(d)
}

var req = thune.Identity{TenantID: "t1", Account: "acct_1", Object: "ch_1", Kind: thune.KindRefund, Request: "ticket-7"}

// sendRefund is what the gateway does for an allowed refund: it records
// sending, the processor refunds with the marker, and the call ends with
// the outcome lost or not.
func (e *env) sendRefund(t *testing.T, key string, amount int64, lost bool) {
	t.Helper()
	ctx := context.Background()
	won, err := e.gate.Sending(ctx, key)
	if err != nil || !won {
		t.Fatalf("sending %s: won=%v err=%v", key, won, err)
	}
	e.proc.refund("ch_1", amount, key)
	end := thune.ExecSent
	if lost {
		end = thune.ExecUnknown
	}
	if err := e.gate.After(ctx, req, key, end); err != nil {
		t.Fatal(err)
	}
}

func (e *env) drain(t *testing.T) {
	t.Helper()
	for i := 0; i < 10; i++ {
		n, err := e.watch.RunAccount(context.Background(), "t1", "acct_1", e.proc)
		if err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			return
		}
	}
}

// The claim holds under a real race: many inserts at once, one wins.
func TestTheClaimHoldsUnderARealRace(t *testing.T) {
	e := newEnv(t)
	const n = 24
	var wins atomic.Int32
	holders := make(chan string, n)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			r, err := e.store.Claim(context.Background(), req, fmt.Sprintf("tc-%02d", i))
			if err != nil {
				t.Errorf("claim: %v", err)
				return
			}
			if r.Won {
				wins.Add(1)
			}
			holders <- r.Holder
		}(i)
	}
	close(start)
	wg.Wait()
	close(holders)
	if wins.Load() != 1 {
		t.Fatalf("%d claims won, want exactly 1", wins.Load())
	}
	first := <-holders
	for h := range holders {
		if h != first {
			t.Fatalf("callers saw different holders: %s and %s", first, h)
		}
	}
}

// One charge's refunds run one at a time: two calls at once, one lock.
func TestTheChargeLockHoldsUnderARace(t *testing.T) {
	e := newEnv(t)
	const n = 16
	var wins atomic.Int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			ok, err := e.store.LockCharge(context.Background(), "t1", "acct_1", "ch_1", fmt.Sprintf("tc-%02d", i), time.Minute)
			if err != nil {
				t.Errorf("lock: %v", err)
			}
			if ok {
				wins.Add(1)
			}
		}(i)
	}
	close(start)
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatalf("%d locks taken, want 1", wins.Load())
	}
	// A crashed holder's lock frees itself once its lease passes.
	e.advance(time.Minute)
	if ok, err := e.store.LockCharge(context.Background(), "t1", "acct_1", "ch_1", "tc-late", time.Minute); err != nil || !ok {
		t.Fatalf("after the lease: ok=%v err=%v", ok, err)
	}
}

// The acceptance scenario (thune design v3 §10).
func TestAcceptanceTwoTafsOneCrashOneDashboardRefund(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()

	// 1-2. A customer asks for €20 back on a €100 charge. The first taf
	// takes the claim, KIFF allows €20, and the taf crashes after the
	// call left, before the answer came.
	b1, err := e.gate.Before(ctx, req, thune.Ask{Amount: 2000}, "tc-first")
	if err != nil {
		t.Fatal(err)
	}
	if !b1.Claim.Won || b1.Case.Verdict != thune.VerdictAllow || b1.Case.Authorized != 2000 {
		t.Fatalf("first taf: %+v", b1)
	}
	e.sendRefund(t, "tc-first", 2000, true)

	// 3. The second taf asks for the same refund and is told the first
	// is handling it. It sends nothing.
	e.advance(5 * time.Second)
	b2, err := e.gate.Before(ctx, req, thune.Ask{Amount: 2000}, "tc-second")
	if err != nil {
		t.Fatal(err)
	}
	if b2.Claim.Won || b2.Claim.Holder != "tc-first" || b2.Case.Verdict != thune.VerdictRefuse ||
		!slices.Equal(b2.Case.Reasons, []string{thune.ReasonClaimed}) || b2.Claim.HolderExecution != thune.ExecSent {
		// The second taf's own read of the charge found the first taf's
		// refund by its marker: the lost call is already known as sent.
		t.Fatalf("second taf: %+v", b2)
	}
	if err := e.gate.After(ctx, req, "tc-second", thune.ExecRefused); err != nil {
		t.Fatal(err)
	}
	// Refusing the second never frees the first's claim.
	if r, _ := e.store.Claim(ctx, req, "tc-third"); r.Won {
		t.Fatal("the first taf's claim was freed by the second's refusal")
	}

	// 4. The owner, not knowing, refunds €20 in the processor's dashboard.
	e.proc.refund("ch_1", 2000, "")

	// 5. tafs then reports, without anyone's help.
	e.drain(t)
	a, err := e.store.Get(ctx, "tc-first")
	if err != nil {
		t.Fatal(err)
	}
	if a.Execution != thune.ExecSent || a.Financial != thune.FinPending || a.RefundID == "" {
		t.Fatalf("the first taf's refund: %+v", a)
	}
	if !slices.Equal(e.calls.found, []string{"tc-first"}) {
		t.Fatalf("lost call settled as sent: %v", e.calls.found)
	}
	rep, err := e.store.ChargeReport(ctx, "t1", "acct_1", "ch_1")
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.KIFF) != 1 || rep.KIFF[0].Key != "tc-first" || rep.KIFF[0].Amount != 2000 {
		t.Fatalf("KIFF's refund: %+v", rep.KIFF)
	}
	if len(rep.Outside) != 1 || rep.Outside[0].Amount != 2000 || rep.Outside[0].Key != "" {
		t.Fatalf("refunded outside KIFF: %+v", rep.Outside)
	}
	if rep.Left != 6000 || rep.Captured != 10000 {
		t.Fatalf("left %d of %d, want 6000 of 10000", rep.Left, rep.Captured)
	}
	if !rep.RefundedTwice {
		t.Fatal("not reported as refunded twice")
	}
	// The Card counted €20 once: one allowed adjustment, authorized 2000;
	// the second taf's was refused with nothing authorized.
	b, _ := e.store.Get(ctx, "tc-second")
	if a.Authorized != 2000 || b.Authorized != 0 || b.Execution != thune.ExecRefused {
		t.Fatalf("counted: first %d, second %d (%s)", a.Authorized, b.Authorized, b.Execution)
	}

	// 6. And nothing else moved: reading again, the watch, the second taf
	// retrying, none sends a refund.
	e.advance(time.Hour)
	e.drain(t)
	if _, err := e.gate.Before(ctx, req, thune.Ask{Amount: 2000}, "tc-second"); err != nil {
		t.Fatal(err)
	}
	if e.proc.count() != 2 {
		t.Fatalf("%d refunds at the processor, want 2 (KIFF's and the dashboard's)", e.proc.count())
	}
	if again, _ := e.store.ChargeReport(ctx, "t1", "acct_1", "ch_1"); len(again.KIFF) != 1 || len(again.Outside) != 1 {
		t.Fatalf("read again: %+v", again)
	}
}

// An approval waits three hours, a dashboard refund lands meanwhile, and
// the owner is asked again with the new remainder.
func TestAnApprovalWaitsAndTheSourceChanges(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	b, err := e.gate.Before(ctx, req, thune.Ask{Amount: 2000}, "tc-held")
	if err != nil {
		t.Fatal(err)
	}
	if b.Case.Verdict != thune.VerdictAllow {
		t.Fatalf("case: %+v", b.Case)
	}
	// The Card holds it for the owner (over a limit).
	if err := e.gate.Held(ctx, req, "tc-held"); err != nil {
		t.Fatal(err)
	}
	e.advance(3 * time.Hour)
	e.proc.refund("ch_1", 3000, "")
	// The owner approves; the retry reads again before sending.
	r, err := e.gate.Before(ctx, req, thune.Ask{Amount: 2000}, "tc-held")
	if err != nil {
		t.Fatal(err)
	}
	if !r.Retry || !r.Void || r.Case.Facts.Remainder() != 7000 || !slices.Equal(r.Case.Reasons, []string{thune.ReasonFactsChanged}) {
		t.Fatalf("retry: %+v", r)
	}
	if err := e.gate.After(ctx, req, "tc-held", thune.ExecVoid); err != nil {
		t.Fatal(err)
	}
	if won, _ := e.gate.Sending(ctx, "tc-held"); won {
		t.Fatal("a void approval moved to sending")
	}
	// The new call is a new case, with the new remainder, and the charge
	// now has a refund, so it is the owner's again.
	n, err := e.gate.Before(ctx, req, thune.Ask{Amount: 2000}, "tc-again")
	if err != nil {
		t.Fatal(err)
	}
	if !n.Claim.Won || n.Case.Verdict != thune.VerdictAsk || n.Case.Facts.Remainder() != 7000 ||
		!slices.Equal(n.Case.Reasons, []string{thune.ReasonSecondRefund}) {
		t.Fatalf("asked again: %+v", n)
	}
	if e.proc.count() != 1 {
		t.Fatalf("%d refunds, want only the dashboard's", e.proc.count())
	}
}

// An approval read again with unchanged facts goes ahead.
func TestAnApprovalWithUnchangedFactsIsSent(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if _, err := e.gate.Before(ctx, req, thune.Ask{Amount: 2000}, "tc-held"); err != nil {
		t.Fatal(err)
	}
	if err := e.gate.Held(ctx, req, "tc-held"); err != nil {
		t.Fatal(err)
	}
	e.advance(3 * time.Hour)
	r, err := e.gate.Before(ctx, req, thune.Ask{Amount: 2000}, "tc-held")
	if err != nil {
		t.Fatal(err)
	}
	if !r.Retry || r.Void || r.Case.Authorized != 2000 {
		t.Fatalf("retry: %+v", r)
	}
	e.sendRefund(t, "tc-held", 2000, false)
	if e.proc.count() != 1 {
		t.Fatalf("%d refunds", e.proc.count())
	}
}

// A crash before sending releases the claim, and the second taf completes
// the refund once.
func TestACrashBeforeSendingReleasesTheClaim(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if _, err := e.gate.Before(ctx, req, thune.Ask{Amount: 2000}, "tc-first"); err != nil {
		t.Fatal(err)
	}
	// The gateway ends the call not_sent (its permit wasn't issued, the
	// tool was unreachable, or it restarted while deciding).
	if err := e.gate.After(ctx, req, "tc-first", thune.ExecNotSent); err != nil {
		t.Fatal(err)
	}
	b, err := e.gate.Before(ctx, req, thune.Ask{Amount: 2000}, "tc-second")
	if err != nil {
		t.Fatal(err)
	}
	if !b.Claim.Won || b.Case.Verdict != thune.VerdictAllow {
		t.Fatalf("second taf: %+v", b)
	}
	e.sendRefund(t, "tc-second", 2000, false)
	// And a third is told the second handles it.
	c, err := e.gate.Before(ctx, req, thune.Ask{Amount: 2000}, "tc-third")
	if err != nil {
		t.Fatal(err)
	}
	if c.Claim.Won || c.Claim.Holder != "tc-second" {
		t.Fatalf("third taf: %+v", c)
	}
	if e.proc.count() != 1 {
		t.Fatalf("%d refunds, want 1", e.proc.count())
	}
}

// A crash while the lock is held: the lease frees the charge.
func TestACrashedHoldersChargeLockExpires(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if _, err := e.gate.Before(ctx, req, thune.Ask{Amount: 2000}, "tc-crashed"); err != nil {
		t.Fatal(err)
	}
	other := req
	other.Request = "ticket-8"
	if _, err := e.gate.Before(ctx, other, thune.Ask{Amount: 1000}, "tc-other"); err != thune.ErrChargeBusy {
		t.Fatalf("while locked: %v", err)
	}
	e.advance(3 * time.Minute)
	if _, err := e.gate.Before(ctx, other, thune.Ask{Amount: 1000}, "tc-other"); err != nil {
		t.Fatalf("after the lease: %v", err)
	}
}

// A refund fails two days later: its money is recorded as failed, for
// apps/api to give the room back and tell the owner, and its request is
// free again.
func TestARefundFailsDaysLater(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if _, err := e.gate.Before(ctx, req, thune.Ask{Amount: 2000}, "tc-1"); err != nil {
		t.Fatal(err)
	}
	e.sendRefund(t, "tc-1", 2000, false)
	e.drain(t)
	a, _ := e.store.Get(ctx, "tc-1")
	if a.Financial != thune.FinPending {
		t.Fatalf("financial %s", a.Financial)
	}
	e.advance(48 * time.Hour)
	e.proc.settle(a.RefundID, thune.RefundFailed)
	e.drain(t)
	a, _ = e.store.Get(ctx, "tc-1")
	if a.Execution != thune.ExecSent || a.Financial != thune.FinFailed {
		t.Fatalf("after failing: %+v", a)
	}
	// Read again: the watch is over.
	e.advance(time.Hour)
	reads := e.proc.reads.Load()
	e.drain(t)
	if e.proc.reads.Load() != reads {
		t.Fatal("a refund whose money ended is still being read")
	}
	// A failed refund is no first refund: the next case isn't the owner's.
	b, err := e.gate.Before(ctx, req, thune.Ask{Amount: 2000}, "tc-2")
	if err != nil {
		t.Fatal(err)
	}
	if b.Case.Verdict != thune.VerdictAllow || b.Case.Facts.Remainder() != 10000 {
		t.Fatalf("after a failed refund: %+v", b.Case)
	}
}

// A second partial refund on a charge is held, shown beside the first.
func TestASecondPartialRefundIsHeld(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if _, err := e.gate.Before(ctx, req, thune.Ask{Amount: 2000}, "tc-1"); err != nil {
		t.Fatal(err)
	}
	e.sendRefund(t, "tc-1", 2000, false)
	other := req
	other.Request = "ticket-8"
	b, err := e.gate.Before(ctx, other, thune.Ask{Amount: 1500}, "tc-2")
	if err != nil {
		t.Fatal(err)
	}
	if b.Case.Verdict != thune.VerdictAsk || !slices.Equal(b.Case.Reasons, []string{thune.ReasonSecondRefund}) ||
		b.Case.Authorized != 1500 || len(b.Case.Facts.Refunds) != 1 || b.Case.Facts.Refunds[0].Marker != "tc-1" {
		t.Fatalf("second partial: %+v", b.Case)
	}
	a, _ := e.store.Get(ctx, "tc-2")
	if a.Execution != thune.ExecHeld {
		t.Fatalf("execution %s, want held", a.Execution)
	}
}

// An unknown call is never resent, even when the watcher can't find the
// marker before the owner answers.
func TestAnUnknownCallIsNeverResent(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.proc.delay = 10 * time.Minute
	if _, err := e.gate.Before(ctx, req, thune.Ask{Amount: 2000}, "tc-lost"); err != nil {
		t.Fatal(err)
	}
	e.sendRefund(t, "tc-lost", 2000, true)
	e.drain(t)
	a, _ := e.store.Get(ctx, "tc-lost")
	if a.Execution != thune.ExecUnknown || a.Financial != thune.FinNone {
		t.Fatalf("before the marker shows: %+v", a)
	}
	// Nothing moves it back to sending: not a retry of the same call, not
	// the watch, not the same taf asking again.
	if won, _ := e.gate.Sending(ctx, "tc-lost"); won {
		t.Fatal("an unknown call moved to sending")
	}
	if b, err := e.gate.Before(ctx, req, thune.Ask{Amount: 2000}, "tc-lost"); err != nil || b.Case.Verdict == thune.VerdictAllow && !b.Retry {
		t.Fatalf("retry: %+v %v", b, err)
	}
	if won, _ := e.gate.Sending(ctx, "tc-lost"); won {
		t.Fatal("an unknown call moved to sending after a retry")
	}
	other := req
	other.Request = "ticket-7"
	if b, _ := e.gate.Before(ctx, other, thune.Ask{Amount: 2000}, "tc-new"); b.Claim.Won {
		t.Fatal("a new call took the lost call's claim")
	}
	if e.proc.count() != 1 {
		t.Fatalf("%d refunds, want 1", e.proc.count())
	}
	// Later the marker shows: the call is sent, and only then.
	e.advance(15 * time.Minute)
	e.drain(t)
	a, _ = e.store.Get(ctx, "tc-lost")
	if a.Execution != thune.ExecSent || a.Financial != thune.FinPending {
		t.Fatalf("after the marker shows: %+v", a)
	}
}

// An event delivered twice changes nothing the second time, and the same
// facts settled three times record each refund once.
func TestAnEventDeliveredTwiceChangesNothing(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	first, err := e.store.RecordEvent(ctx, "t1", "evt_1")
	if err != nil || !first {
		t.Fatalf("first: %v %v", first, err)
	}
	again, err := e.store.RecordEvent(ctx, "t1", "evt_1")
	if err != nil || again {
		t.Fatalf("again: %v %v", again, err)
	}
	// And settling the same facts twice tells the owner once.
	if _, err := e.gate.Before(ctx, req, thune.Ask{Amount: 2000}, "tc-1"); err != nil {
		t.Fatal(err)
	}
	e.sendRefund(t, "tc-1", 2000, false)
	e.proc.refund("ch_1", 500, "")
	f, _ := e.proc.ReadCharge(ctx, "t1", "acct_1", "ch_1")
	for i := 0; i < 3; i++ {
		if err := e.watch.Settle(ctx, "t1", f); err != nil {
			t.Fatal(err)
		}
	}
	var n int
	if err := e.store.Pool().QueryRow(ctx, `SELECT count(*) FROM gateway.thune_refunds WHERE origin='outside'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("outside refunds recorded %d times (%v)", n, err)
	}
	if rep, _ := e.store.ChargeReport(ctx, "t1", "acct_1", "ch_1"); !rep.RefundedTwice || rep.Left != 7500 {
		t.Fatalf("report: %+v", rep)
	}
}

// The connection is revoked while a refund is pending: it is marked as one
// thune can't watch, and the claim stays.
func TestARevokedConnectionStopsTheWatchAndKeepsTheClaim(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if _, err := e.gate.Before(ctx, req, thune.Ask{Amount: 2000}, "tc-1"); err != nil {
		t.Fatal(err)
	}
	e.sendRefund(t, "tc-1", 2000, false)
	keys, err := e.store.Unwatchable(ctx, "t1", "acct_1")
	if err != nil || !slices.Equal(keys, []string{"tc-1"}) {
		t.Fatalf("unwatchable: %v %v", keys, err)
	}
	reads := e.proc.reads.Load()
	e.drain(t)
	if e.proc.reads.Load() != reads {
		t.Fatal("an unwatchable refund was read")
	}
	if r, _ := e.store.Claim(ctx, req, "tc-2"); r.Won {
		t.Fatal("the claim was freed while money may have moved")
	}
}
