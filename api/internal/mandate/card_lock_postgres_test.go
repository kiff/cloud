package mandate_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kiff/cloud/api/internal/idempotency"
	"github.com/kiff/cloud/api/internal/mandate"
	"github.com/kiff/cloud/api/internal/testdb"
)

// Locking. PostgresStore takes each Card's lock with
// pg_advisory_xact_lock inside the transaction that re-reads the Card,
// evaluates and writes the draws; moves, reissues and revocations take
// the same lock inside their own write transactions.

func poolWith(t *testing.T, maxConns int32) *pgxpool.Pool {
	t.Helper()
	cfg, err := pgxpool.ParseConfig(testdb.URL(t))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	cfg.MaxConns = maxConns
	p, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(p.Close)
	return p
}

// countCard is a Card that allows `limit` refunds a day, held by holder.
func countCard(tenantID, id, holder string, limit int64) mandate.Mandate {
	m := pgMandate(tenantID, id)
	m.Subject = holder
	m.Aggregates = []mandate.Aggregate{{Quantity: mandate.Quantity{Kind: mandate.KindCount}, Limit: limit, Window: mandate.WindowCalendarDay}}
	return m
}

func refundBy(holder string) mandate.Request {
	return mandate.Request{Subject: holder, Domain: "billing", Environment: "prod",
		Action: "REFUND_INVOICE", EntityState: "PAID", EntityID: "inv-1",
		Parameters: map[string]int64{"amount_cents": 100}}
}

func drawCount(t *testing.T, s *mandate.PostgresStore, tenantID, id string) int64 {
	t.Helper()
	n, err := s.Used(context.Background(), tenantID, id, mandate.Quantity{Kind: mandate.KindCount}.Key(), base.Add(-48*time.Hour), "")
	if err != nil {
		t.Fatalf("used: %v", err)
	}
	return n
}

// A request that found the old holder's Card, then waited while the
// Card moved, must not draw on it when it resumes.
func TestPostgres_StaleHolderCannotDrawAfterMove(t *testing.T) {
	pool := requirePostgres(t)
	seedTenant(t, pool, "t-move")
	store := mandate.NewPostgresStore(pool)
	ctx := context.Background()
	if err := store.Issue(ctx, countCard("t-move", "card-1", "agent://a", 10), "owner"); err != nil {
		t.Fatal(err)
	}

	a := mandate.NewAuthorizer(store, nil, at(base))
	var once sync.Once
	mandate.SetAfterDiscover(a, func() {
		// The gap: agent://a has found card-1 and holds no lock yet.
		once.Do(func() {
			if _, err := store.Move(ctx, "t-move", "card-1", "agent://b", "owner", base); err != nil {
				t.Errorf("move: %v", err)
			}
		})
	})

	d, err := a.Authorize(ctx, "t-move", "p-stale", refundBy("agent://a"))
	if err != nil {
		t.Fatalf("authorize: %v", err)
	}
	if d.Outcome == mandate.OutcomeWithinLimit {
		t.Fatalf("the old holder drew after the move: %+v", d)
	}
	if n := drawCount(t, store, "t-move", "card-1"); n != 0 {
		t.Fatalf("card-1 has %d draws after a stale request", n)
	}
	b := mandate.NewAuthorizer(store, nil, at(base))
	if d, err := b.Authorize(ctx, "t-move", "p-new", refundBy("agent://b")); err != nil || d.Outcome != mandate.OutcomeWithinLimit {
		t.Fatalf("new holder: %+v %v", d, err)
	}
}

// A lock held on a separate session could be lost after
// the decision and before the write, and the write still committed. With
// the lock on the ledger transaction, losing that connection loses the
// write: the interrupted request fails closed and the Card stays within
// its limit, even after a move and the new holder's spend in between.
func TestPostgres_LosingTheConnectionBeforeTheWriteFailsClosed(t *testing.T) {
	pool := requirePostgres(t)
	seedTenant(t, pool, "t-loss")
	store := mandate.NewPostgresStore(pool)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := store.Issue(ctx, countCard("t-loss", "card-1", "agent://a", 1), "owner"); err != nil {
		t.Fatal(err)
	}

	lockID := mandate.CardLockID("t-loss", "card-1")
	a := mandate.NewAuthorizer(store, nil, at(base))
	var once sync.Once
	mandate.SetBeforeWrite(a, func() {
		once.Do(func() {
			// agent://a has evaluated the last unit under the Card lock
			// and not yet written it. Kill the session holding the lock.
			var pid int32
			if err := pool.QueryRow(ctx, `
				SELECT pid FROM pg_locks
				 WHERE locktype = 'advisory' AND granted
				   AND classid = $1::bigint::oid AND objid = $2::bigint::oid AND objsubid = 1`,
				int64(uint32(uint64(lockID)>>32)), int64(uint32(uint64(lockID)))).Scan(&pid); err != nil {
				t.Fatalf("find the lock holder: %v", err)
			}
			var ok bool
			if err := pool.QueryRow(ctx, `SELECT pg_terminate_backend($1)`, pid).Scan(&ok); err != nil || !ok {
				t.Fatalf("terminate: %v %v", ok, err)
			}
			// The lock is gone with the session: the move and the new
			// holder's spend go through.
			if _, err := store.Move(ctx, "t-loss", "card-1", "agent://b", "owner", base); err != nil {
				t.Fatalf("move: %v", err)
			}
			d, err := mandate.NewAuthorizer(store, nil, at(base)).Authorize(ctx, "t-loss", "p-b", refundBy("agent://b"))
			if err != nil || d.Outcome != mandate.OutcomeWithinLimit {
				t.Fatalf("new holder: %+v %v", d, err)
			}
		})
	})

	d, err := a.Authorize(ctx, "t-loss", "p-a", refundBy("agent://a"))
	if err == nil && d.Outcome == mandate.OutcomeWithinLimit {
		t.Fatalf("old holder authorized after losing its lock: %+v", d)
	}
	if err != nil && !errors.Is(err, mandate.ErrUsageUnavailable) {
		t.Fatalf("want a fail-closed ErrUsageUnavailable, got %v", err)
	}
	if n := drawCount(t, store, "t-loss", "card-1"); n != 1 {
		t.Fatalf("card-1 used %d, want exactly the new holder's 1", n)
	}
}

// Both holders race for the remaining budget while the Card moves
// between them: the total never passes the limit, and once the move has
// committed the old holder draws nothing.
func TestPostgres_MoveRacingDrawsNeverOverspends(t *testing.T) {
	pool := requirePostgres(t)
	seedTenant(t, pool, "t-race")
	store := mandate.NewPostgresStore(pool)
	ctx := context.Background()
	const limit = 10
	if err := store.Issue(ctx, countCard("t-race", "card-1", "agent://a", limit), "owner"); err != nil {
		t.Fatal(err)
	}
	a := mandate.NewAuthorizer(store, nil, at(base))

	var wg sync.WaitGroup
	var allowed atomic.Int64
	start := make(chan struct{})
	for i := 0; i < 30; i++ {
		holder := "agent://a"
		if i%2 == 1 {
			holder = "agent://b"
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			d, err := a.Authorize(ctx, "t-race", fmt.Sprintf("p-%d", i), refundBy(holder))
			if err != nil {
				t.Errorf("authorize: %v", err)
				return
			}
			if d.Outcome == mandate.OutcomeWithinLimit {
				allowed.Add(1)
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		time.Sleep(5 * time.Millisecond)
		if _, err := store.Move(ctx, "t-race", "card-1", "agent://b", "owner", base); err != nil {
			t.Errorf("move: %v", err)
		}
	}()
	close(start)
	wg.Wait()

	if n := drawCount(t, store, "t-race", "card-1"); n > limit || n != allowed.Load() {
		t.Fatalf("drawn %d, allowed %d, limit %d", n, allowed.Load(), limit)
	}
	after, err := a.Authorize(ctx, "t-race", "p-after", refundBy("agent://a"))
	if err != nil || after.Outcome == mandate.OutcomeWithinLimit {
		t.Fatalf("old holder drew after the move: %+v %v", after, err)
	}
}

// Several Cards per request on a small pool: all of a request's Card
// locks are on its one ledger transaction, so concurrent requests queue
// for connections instead of exhausting the pool while holding locks.
func TestPostgres_ManyCardsSmallPool(t *testing.T) {
	requirePostgres(t)
	pool := poolWith(t, 2)
	seedTenant(t, pool, "t-pool")
	store := mandate.NewPostgresStore(pool)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	for i := 0; i < 5; i++ {
		if err := store.Issue(ctx, countCard("t-pool", fmt.Sprintf("card-%d", i), "agent://a", 100), "owner"); err != nil {
			t.Fatal(err)
		}
	}
	a := mandate.NewAuthorizer(store, nil, at(base))
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d, err := a.Authorize(ctx, "t-pool", fmt.Sprintf("p-%d", i), refundBy("agent://a"))
			if err != nil || d.Outcome != mandate.OutcomeWithinLimit {
				t.Errorf("authorize: %+v %v", d, err)
			}
		}()
	}
	wg.Wait()
	for i := 0; i < 5; i++ {
		if n := drawCount(t, store, "t-pool", fmt.Sprintf("card-%d", i)); n != 4 {
			t.Errorf("card-%d drawn %d, want 4 (every covering card draws)", i, n)
		}
	}
}

// AcquireAll (the in-process fallback's locker) takes overlapping sets
// in opposite orders without deadlocking.
func TestPostgres_AcquireAllOppositeOrdersDoNotDeadlock(t *testing.T) {
	requirePostgres(t)
	locker := idempotency.NewAdvisoryLocker(poolWith(t, 4))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	for _, keys := range [][]string{{"k-a", "k-b", "k-c"}, {"k-c", "k-b", "k-a"}} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				release, err := locker.AcquireAll(ctx, keys)
				if err != nil {
					t.Errorf("acquire: %v", err)
					return
				}
				release()
			}
		}()
	}
	wg.Wait()
}

// Move keeps the Card and records the holder; it refuses a revoked Card
// and the current holder.
func TestPostgres_MoveRecordsTheHolderAndRefuses(t *testing.T) {
	pool := requirePostgres(t)
	seedTenant(t, pool, "t-rev")
	store := mandate.NewPostgresStore(pool)
	ctx := context.Background()
	if err := store.Issue(ctx, countCard("t-rev", "card-1", "agent://a", 10), "owner@x"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Move(ctx, "t-rev", "card-1", "agent://a", "owner@x", base); !errors.Is(err, mandate.ErrMoveSameHolder) {
		t.Fatalf("same holder: %v", err)
	}
	if _, err := store.Move(ctx, "t-rev", "nope", "agent://b", "owner@x", base); !errors.Is(err, mandate.ErrNotFound) {
		t.Fatalf("unknown card: %v", err)
	}
	m, err := store.Move(ctx, "t-rev", "card-1", "agent://b", "owner@x", base)
	if err != nil || m.Subject != "agent://b" || m.Aggregates[0].Limit != 10 {
		t.Fatalf("move: %+v %v", m, err)
	}
	revs, err := store.Revisions(ctx, "t-rev", "card-1")
	if err != nil || len(revs) != 2 || revs[0].Subject != "agent://a" || revs[1].Subject != "agent://b" || revs[1].By != "owner@x" {
		t.Fatalf("revisions = %+v, err %v", revs, err)
	}
	if err := store.Revoke(ctx, "t-rev", "card-1", base.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Move(ctx, "t-rev", "card-1", "agent://c", "owner@x", base); !errors.Is(err, mandate.ErrMoveInactive) {
		t.Fatalf("revoked card: %v", err)
	}
}
