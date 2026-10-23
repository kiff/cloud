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

// The work comes home (exceptions), against Postgres.

func pgHomeCard(tenantID, id string, perAction, total int64) mandate.Mandate {
	m := pgMandate(tenantID, id)
	m.OnExceed = mandate.OnExceedOwner
	m.Grants[0].PerAction = []mandate.PerActionLimit{{Quantity: mandate.Quantity{Kind: mandate.KindSum, Parameter: "amount_cents"}, Max: perAction}}
	m.Aggregates[0].Limit = total
	return m
}

func pgRefund(amount int64) mandate.Request {
	return mandate.Request{Subject: "agent://a", Domain: "billing", Environment: "prod",
		Action: "REFUND_INVOICE", EntityState: "PAID", EntityID: "inv-1",
		Parameters: map[string]int64{"amount_cents": amount}}
}

func outsideTotal(t *testing.T, s *mandate.PostgresStore, tenantID, card string) int64 {
	t.Helper()
	es, err := s.ExceptionEntries(context.Background(), tenantID, card, base.Add(-48*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	var n int64
	for _, e := range es {
		n += e.Amount
	}
	return n
}

// Concurrent retries of one held proposal meet on one exception.
func TestPostgres_ConcurrentRetriesHoldOnce(t *testing.T) {
	pool := requirePostgres(t)
	seedTenant(t, pool, "t-hold")
	store := mandate.NewPostgresStore(pool)
	ctx := context.Background()
	if err := store.Issue(ctx, pgHomeCard("t-hold", "card-1", 50000, 500000), "owner"); err != nil {
		t.Fatal(err)
	}
	a := mandate.NewAuthorizer(store, nil, at(base))
	var wg sync.WaitGroup
	ids := make(chan string, 10)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d, err := a.Authorize(ctx, "t-hold", "p-1", pgRefund(150000))
			if err != nil || d.Outcome != mandate.OutcomeOwnerRequired {
				t.Errorf("retry: %+v %v", d, err)
				return
			}
			ids <- d.ExceptionID
		}()
	}
	wg.Wait()
	close(ids)
	first := ""
	for id := range ids {
		if first == "" {
			first = id
		}
		if id != first {
			t.Fatalf("retries got different handles: %s, %s", first, id)
		}
	}
	es, err := store.Exceptions(ctx, "t-hold", "")
	if err != nil || len(es) != 1 {
		t.Fatalf("exceptions = %d, %v", len(es), err)
	}
}

// Concurrent re-decides after an approval redeem it once: one entry, one
// consumption, and every caller gets the same authorization.
func TestPostgres_ConcurrentRedemptionConsumesOnce(t *testing.T) {
	pool := requirePostgres(t)
	seedTenant(t, pool, "t-redeem")
	store := mandate.NewPostgresStore(pool)
	ctx := context.Background()
	if err := store.Issue(ctx, pgHomeCard("t-redeem", "card-1", 50000, 500000), "owner"); err != nil {
		t.Fatal(err)
	}
	a := mandate.NewAuthorizer(store, nil, at(base))
	d, err := a.Authorize(ctx, "t-redeem", "p-1", pgRefund(150000))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApproveOnce(ctx, "t-redeem", d.ExceptionID, "owner", base); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := a.Authorize(ctx, "t-redeem", "p-1", pgRefund(150000))
			if err != nil || got.Outcome != mandate.OutcomeWithinLimit {
				t.Errorf("redeem: %+v %v", got, err)
			}
		}()
	}
	wg.Wait()
	if n := outsideTotal(t, store, "t-redeem", "card-1"); n != 150000 {
		t.Fatalf("recorded outside the card %d, want exactly one 150000", n)
	}
	draws, err := store.ProposalDraws(ctx, "t-redeem", "p-1")
	if err != nil || len(draws) != 1 || draws[0].Amount != 150000 {
		t.Fatalf("approved retries must draw the full amount exactly once: %+v %v", draws, err)
	}
	e, _ := store.Exception(ctx, "t-redeem", d.ExceptionID)
	if e.Status != mandate.ExceptionConsumed {
		t.Fatalf("status = %s", e.Status)
	}
}

// A redemption racing a revocation or a move of the Card: either the
// redemption committed first (consumed, one entry) or the mutation did
// (invalidated, no entry). Never an entry recorded after the Card was
// taken away.
func TestPostgres_RedemptionRacingRevokeOrMove(t *testing.T) {
	for _, mutation := range []string{"revoke", "move"} {
		t.Run(mutation, func(t *testing.T) {
			pool := requirePostgres(t)
			tid := "t-race-" + mutation
			seedTenant(t, pool, tid)
			store := mandate.NewPostgresStore(pool)
			ctx := context.Background()
			for round := 0; round < 10; round++ {
				card := fmt.Sprintf("card-%d", round)
				proposal := fmt.Sprintf("p-%d", round)
				if err := store.Issue(ctx, pgHomeCard(tid, card, 50000, 500000), "owner"); err != nil {
					t.Fatal(err)
				}
				a := mandate.NewAuthorizer(store, nil, at(base))
				d, err := a.Authorize(ctx, tid, proposal, pgRefund(150000))
				if err != nil || d.Outcome != mandate.OutcomeOwnerRequired {
					t.Fatalf("hold: %+v %v", d, err)
				}
				if _, err := store.ApproveOnce(ctx, tid, d.ExceptionID, "owner", base); err != nil {
					t.Fatal(err)
				}
				var wg sync.WaitGroup
				wg.Add(2)
				go func() {
					defer wg.Done()
					_, _ = a.Authorize(ctx, tid, proposal, pgRefund(150000))
				}()
				go func() {
					defer wg.Done()
					var err error
					if mutation == "revoke" {
						err = store.Revoke(ctx, tid, card, base)
					} else {
						_, err = store.Move(ctx, tid, card, "agent://other", "owner", base)
					}
					if err != nil {
						t.Errorf("%s: %v", mutation, err)
					}
				}()
				wg.Wait()
				e, _ := store.Exception(ctx, tid, d.ExceptionID)
				n := outsideTotal(t, store, tid, card)
				switch e.Status {
				case mandate.ExceptionConsumed:
					if n != 150000 {
						t.Fatalf("round %d: consumed with %d recorded", round, n)
					}
				case mandate.ExceptionInvalidated:
					if n != 0 {
						t.Fatalf("round %d: invalidated but %d recorded", round, n)
					}
				default:
					t.Fatalf("round %d: status %s", round, e.Status)
				}
			}
		})
	}
}

// Another tenant cannot read or answer an exception.
func TestPostgres_ExceptionsAreTenantScoped(t *testing.T) {
	pool := requirePostgres(t)
	seedTenant(t, pool, "t-own")
	seedTenant(t, pool, "t-other")
	store := mandate.NewPostgresStore(pool)
	ctx := context.Background()
	if err := store.Issue(ctx, pgHomeCard("t-own", "card-1", 50000, 500000), "owner"); err != nil {
		t.Fatal(err)
	}
	d, err := mandate.NewAuthorizer(store, nil, at(base)).Authorize(ctx, "t-own", "p-1", pgRefund(150000))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Exception(ctx, "t-other", d.ExceptionID); !errors.Is(err, mandate.ErrExceptionNotFound) {
		t.Fatalf("read across tenants: %v", err)
	}
	for _, answer := range []func() (mandate.Exception, error){
		func() (mandate.Exception, error) { return store.ApproveOnce(ctx, "t-other", d.ExceptionID, "x", base) },
		func() (mandate.Exception, error) {
			return store.Reject(ctx, "t-other", d.ExceptionID, "x", base, mandate.Refusal{})
		},
		func() (mandate.Exception, error) { return store.ChangeCard(ctx, "t-other", d.ExceptionID, "x", base) },
	} {
		if _, err := answer(); !errors.Is(err, mandate.ErrExceptionNotFound) {
			t.Fatalf("answer across tenants: %v", err)
		}
	}
	if es, _ := store.Exceptions(ctx, "t-other", ""); len(es) != 0 {
		t.Fatalf("listed across tenants: %+v", es)
	}
}

// Cards issued without a policy read as refuse; the policy round-trips;
// Change Card writes a revision in Postgres.
func TestPostgres_PolicyAndChangeCard(t *testing.T) {
	pool := requirePostgres(t)
	seedTenant(t, pool, "t-pol")
	store := mandate.NewPostgresStore(pool)
	ctx := context.Background()
	legacy := pgMandate("t-pol", "card-legacy")
	legacy.Subject = "agent://legacy"
	if err := store.Issue(ctx, legacy, "owner"); err != nil {
		t.Fatal(err)
	}
	if err := store.Issue(ctx, pgHomeCard("t-pol", "card-home", 50000, 500000), "owner"); err != nil {
		t.Fatal(err)
	}
	ms, err := store.List(ctx, "t-pol")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range ms {
		want := mandate.OnExceedRefuse
		if m.ID == "card-home" {
			want = mandate.OnExceedOwner
		}
		if m.OnExceed != want {
			t.Fatalf("%s policy = %q, want %q", m.ID, m.OnExceed, want)
		}
	}
	a := mandate.NewAuthorizer(store, nil, at(base))
	d, err := a.Authorize(ctx, "t-pol", "p-1", pgRefund(150000))
	if err != nil || d.Outcome != mandate.OutcomeOwnerRequired {
		t.Fatalf("hold: %+v %v", d, err)
	}
	if _, err := store.ChangeCard(ctx, "t-pol", d.ExceptionID, "owner@x", base); err != nil {
		t.Fatal(err)
	}
	if got, err := a.Authorize(ctx, "t-pol", "p-1", pgRefund(150000)); err != nil || got.Outcome != mandate.OutcomeWithinLimit {
		t.Fatalf("after change: %+v %v", got, err)
	}
	revs, _ := store.Revisions(ctx, "t-pol", "card-home")
	if len(revs) != 2 || revs[1].By != "owner@x" || revs[1].OnExceed != mandate.OnExceedOwner {
		t.Fatalf("revisions = %+v", revs)
	}
}

// A reissue that does not name a policy keeps the Card's
// current one, read under the Card's lock. An explicit change to refuse
// that holds the lock while the reissue queues behind it must survive:
// the reissue reads the policy only once it has the lock.
func TestPostgres_OmittedPolicyResolvesUnderTheLock(t *testing.T) {
	pool := requirePostgres(t)
	seedTenant(t, pool, "t-keep")
	store := mandate.NewPostgresStore(pool)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := store.Issue(ctx, pgHomeCard("t-keep", "card-1", 50000, 500000), "owner"); err != nil {
		t.Fatal(err)
	}

	locked, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	mandate.SetAfterIssueLock(store, func() {
		once.Do(func() {
			close(locked)
			<-release
		})
	})
	explicit := pgHomeCard("t-keep", "card-1", 50000, 500000)
	explicit.OnExceed = mandate.OnExceedRefuse
	done := make(chan error, 2)
	go func() { done <- store.Issue(ctx, explicit, "owner") }() // takes the lock, then waits
	<-locked

	omitted := pgHomeCard("t-keep", "card-1", 60000, 500000) // an older client, no policy
	omitted.OnExceed = mandate.OnExceedKeep
	go func() { done <- store.Issue(ctx, omitted, "old-client") }()

	// Wait until the reissue is queued on the Card lock, then let the
	// explicit change commit.
	for {
		var waiting int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity WHERE wait_event_type = 'Lock' AND datname = current_database()`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	close(release)
	for i := 0; i < 2; i++ {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}

	ms, err := store.List(ctx, "t-keep")
	if err != nil || len(ms) != 1 {
		t.Fatalf("list: %v %d", err, len(ms))
	}
	if ms[0].OnExceed != mandate.OnExceedRefuse {
		t.Fatalf("the older reissue overwrote the explicit change: policy %q", ms[0].OnExceed)
	}
	if ms[0].Grants[0].PerAction[0].Max != 60000 {
		t.Fatalf("the reissue's own change was lost: %+v", ms[0].Grants[0])
	}
	revs, _ := store.Revisions(ctx, "t-keep", "card-1")
	if len(revs) != 3 || revs[2].OnExceed != mandate.OnExceedRefuse {
		t.Fatalf("revisions: %+v", revs)
	}
}
