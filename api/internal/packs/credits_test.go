package packs_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kiff/cloud/api/internal/meter"
	"github.com/kiff/cloud/api/internal/packs"
	"github.com/kiff/cloud/api/internal/testdb"
)

// creditStores runs f on the memory store and, with a database, on
// Postgres.
func creditStores(t *testing.T, f func(t *testing.T, s packs.CreditLedger, tenant string)) {
	t.Run("memory", func(t *testing.T) { f(t, packs.NewMemory(), "t-mem") })
	t.Run("postgres", func(t *testing.T) {
		pool, tenant := creditDB(t)
		f(t, packs.NewPostgres(pool), tenant)
	})
}

// creditDB is the package's database with the run ledger and the packs,
// and a fresh account on it.
func creditDB(t *testing.T) (*pgxpool.Pool, string) {
	t.Helper()
	url := testdb.URL(t)
	if url == "" {
		t.Skip("KIFF_TEST_POSTGRES_URL not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(ctx, `CREATE SCHEMA IF NOT EXISTS cloud_meta; CREATE TABLE IF NOT EXISTS cloud_meta.tenants (id text PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	if err := meter.ApplySchema(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if err := packs.ApplySchema(ctx, pool); err != nil {
		t.Fatal(err)
	}
	tenant := fmt.Sprintf("t-cr-%d", time.Now().UnixNano())
	if _, err := pool.Exec(ctx, `INSERT INTO cloud_meta.tenants VALUES ($1)`, tenant); err != nil {
		t.Fatal(err)
	}
	return pool, tenant
}

// A pack of credits is recorded once per transaction, never expires, and a
// refunded one's credits are taken back, even when the refund came first.
func TestCreditPacks(t *testing.T) {
	creditStores(t, func(t *testing.T, s packs.CreditLedger, tenant string) {
		ctx := context.Background()
		txn := func(n string) string { return tenant + "-" + n }
		old, created, err := s.RecordCredits(ctx, packs.CreditGrant{TenantID: tenant, Pack: "c500", Credits: 500, Source: packs.SourceApple, SourceTxn: txn("1"), At: now.AddDate(-2, 0, 0)})
		if err != nil || !created || old.CreditsLeft != 500 {
			t.Fatalf("record: %+v %v %v", old, created, err)
		}
		again, created, err := s.RecordCredits(ctx, packs.CreditGrant{TenantID: tenant, Pack: "c500", Credits: 500, Source: packs.SourceApple, SourceTxn: txn("1"), At: now})
		if err != nil || created || again.ID != old.ID {
			t.Fatalf("a repeat: %+v %v %v", again, created, err)
		}
		if _, _, err := s.RecordCredits(ctx, packs.CreditGrant{TenantID: tenant, Pack: "bonus", Credits: 40, Kind: packs.KindBonus, Source: packs.SourceKIFF, SourceTxn: txn("b"), At: now}); err != nil {
			t.Fatal(err)
		}
		// Two years on, the first pack still counts: credits never expire.
		if _, left, err := s.Credits(ctx, tenant, now.AddDate(0, -1, 0), now); err != nil || left != 540 {
			t.Fatalf("left: %d %v", left, err)
		}
		if list, _ := s.CreditPacks(ctx, tenant); len(list) != 2 || list[0].ID != old.ID {
			t.Fatalf("oldest first: %+v", list)
		}
		if p, found, err := s.RevokeCredits(ctx, packs.SourceApple, txn("1"), now); err != nil || !found || p.CreditsLeft != 0 || p.RevokedAt == nil || p.TakenBack != 500 {
			t.Fatalf("revoke: %+v %v %v", p, found, err)
		}
		// Taken back again: nothing left to take.
		if p, found, err := s.RevokeCredits(ctx, packs.SourceApple, txn("1"), now.Add(time.Hour)); err != nil || !found || p.TakenBack != 0 || !p.RevokedAt.Equal(now) {
			t.Fatalf("revoke again: %+v %v %v", p, found, err)
		}
		// Refunded before it was recorded: recorded already taken back.
		if _, found, err := s.RevokeCredits(ctx, packs.SourceGoogle, txn("2"), now); err != nil || found {
			t.Fatalf("an early refund: %v %v", found, err)
		}
		if p, created, err := s.RecordCredits(ctx, packs.CreditGrant{TenantID: tenant, Pack: "c1500", Credits: 1500, Source: packs.SourceGoogle, SourceTxn: txn("2"), At: now}); err != nil || !created || p.CreditsLeft != 0 || p.RevokedAt == nil {
			t.Fatalf("recorded after its refund: %+v %v %v", p, created, err)
		}
		if _, left, _ := s.Credits(ctx, tenant, now.AddDate(0, -1, 0), now); left != 40 {
			t.Fatalf("left after the refunds: %d", left)
		}
	})
}

// settleRig keeps runs on the ledger and settles them.
type settleRig struct {
	t      *testing.T
	pool   *pgxpool.Pool
	ledger *packs.Postgres
	tenant string
	from   time.Time
	to     time.Time
}

func newSettleRig(t *testing.T) *settleRig {
	pool, tenant := creditDB(t)
	return &settleRig{t: t, pool: pool, ledger: packs.NewPostgres(pool), tenant: tenant,
		from: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), to: time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)}
}

// run keeps a counted run that used credits and ended at ended.
func (r *settleRig) run(id string, credits int64, ended time.Time) {
	r.t.Helper()
	if _, err := r.pool.Exec(context.Background(), `
		INSERT INTO cloud_meta.billable_runs (tenant_id, run_id, agent_id, started_at, ended_at, cost_micro_usd, counted, credits)
		VALUES ($1, $2, 'agt', $3, $3, 1, $4, $5)`, r.tenant, id, ended, credits > 0, credits); err != nil {
		r.t.Fatal(err)
	}
}

func (r *settleRig) pack(txn string, credits int64, at time.Time) {
	r.t.Helper()
	if _, _, err := r.ledger.RecordCredits(context.Background(), packs.CreditGrant{TenantID: r.tenant, Pack: "c", Credits: credits,
		Source: packs.SourceApple, SourceTxn: r.tenant + "-" + txn, At: at}); err != nil {
		r.t.Fatal(err)
	}
}

func (r *settleRig) settle(id string, included int64) packs.Settled {
	r.t.Helper()
	s, err := r.ledger.SettleRun(context.Background(), r.tenant, id, r.from, r.to, included)
	if err != nil {
		r.t.Fatal(err)
	}
	return s
}

// A Solo run takes the period's included credits first, then the packs
// bought by the time it ended, oldest first, and what's past both is the
// free overshoot on the run: a pack bought later never pays for it.
func TestSettlingRuns(t *testing.T) {
	r := newSettleRig(t)
	ctx := context.Background()
	day := func(d int) time.Time { return r.from.AddDate(0, 0, d) }
	r.pack("old", 30, day(-40))
	r.pack("new", 50, day(1))
	r.run("a", 80, day(2))
	r.run("b", 40, day(3))
	r.run("c", 60, day(4))
	r.run("free", 0, day(4))
	if s := r.settle("a", 100); s != (packs.Settled{Included: 80}) {
		t.Fatalf("a in the included credits: %+v", s)
	}
	// 20 included left, then the oldest pack's 30.
	if s := r.settle("b", 100); s != (packs.Settled{Included: 20, Drawn: 20}) {
		t.Fatalf("b across the included credits: %+v", s)
	}
	// The old pack's last 10, then the new one's 50.
	if s := r.settle("c", 100); s != (packs.Settled{Drawn: 60}) {
		t.Fatalf("c across two packs: %+v", s)
	}
	if drawn, left, _ := r.ledger.Credits(ctx, r.tenant, r.from, r.to); drawn != 80 || left != 0 {
		t.Fatalf("after c: drawn %d left %d", drawn, left)
	}
	// Past everything: the overshoot, kept on the run.
	r.run("d", 25, day(5))
	if s := r.settle("d", 100); s != (packs.Settled{Over: 25}) {
		t.Fatalf("d past the balance: %+v", s)
	}
	// A pack bought after a run ended doesn't pay for it.
	r.run("e", 10, day(6))
	r.pack("later", 500, day(7))
	if s := r.settle("e", 100); s != (packs.Settled{Over: 10}) {
		t.Fatalf("e before the pack was bought: %+v", s)
	}
	// Settled once: again changes nothing, and a free run takes nothing.
	if s := r.settle("d", 100); !s.Before {
		t.Fatalf("d again: %+v", s)
	}
	if s := r.settle("free", 100); s != (packs.Settled{}) {
		t.Fatalf("a free run: %+v", s)
	}
	var over int64
	if err := r.pool.QueryRow(ctx, `SELECT SUM(over_credits) FROM cloud_meta.billable_runs WHERE tenant_id = $1 AND settled_at IS NOT NULL`, r.tenant).Scan(&over); err != nil || over != 35 {
		t.Fatalf("the overshoot kept: %d %v", over, err)
	}
	if _, left, _ := r.ledger.Credits(ctx, r.tenant, r.from, r.to); left != 500 {
		t.Fatalf("the later pack is whole: %d", left)
	}
	if _, err := r.ledger.SettleRun(ctx, r.tenant, "nope", r.from, r.to, 100); err != packs.ErrNoRun {
		t.Fatalf("an unknown run: %v", err)
	}
}

// Runs settled at once take exactly what there is: the included credits
// once, each pack's credits once, and the rest as overshoot.
func TestSettlingRunsAtOnce(t *testing.T) {
	r := newSettleRig(t)
	ctx := context.Background()
	r.pack("p1", 70, r.from.AddDate(0, 0, -1))
	r.pack("p2", 30, r.from.AddDate(0, 0, -1))
	const n = 20
	for i := range n {
		r.run(fmt.Sprintf("r%02d", i), 15, r.from.Add(time.Duration(i)*time.Minute))
	}
	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		total packs.Settled
	)
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			s, err := r.ledger.SettleRun(ctx, r.tenant, fmt.Sprintf("r%02d", i), r.from, r.to, 100)
			if err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			total.Included, total.Drawn, total.Over = total.Included+s.Included, total.Drawn+s.Drawn, total.Over+s.Over
			mu.Unlock()
		}(i)
	}
	wg.Wait()
	// 300 credits: 100 included, 100 from the packs, 100 past the balance.
	if total != (packs.Settled{Included: 100, Drawn: 100, Over: 100}) {
		t.Fatalf("at once: %+v", total)
	}
	if drawn, left, _ := r.ledger.Credits(ctx, r.tenant, r.from, r.to); drawn != 100 || left != 0 {
		t.Fatalf("the packs: drawn %d left %d", drawn, left)
	}
}

// What an account has bought, for a pack sold once per account: a bought
// pack counts, a refunded one still does, a bonus and another account's
// don't. And a second receipt for the same pack is recorded with its
// credits: the customer paid.
func TestBoughtPacks(t *testing.T) {
	creditStores(t, func(t *testing.T, s packs.CreditLedger, tenant string) {
		ctx := context.Background()
		b, ok := s.(packs.BoughtRead)
		if !ok {
			t.Fatalf("%T doesn't say what an account bought", s)
		}
		txn := func(n string) string { return tenant + "-bought-" + n }
		bought := func() map[string]bool {
			t.Helper()
			got, err := b.BoughtPacks(ctx, tenant)
			if err != nil {
				t.Fatal(err)
			}
			return got
		}
		if got := bought(); len(got) != 0 {
			t.Fatalf("nothing bought yet: %v", got)
		}
		if _, _, err := s.RecordCredits(ctx, packs.CreditGrant{TenantID: tenant, Pack: "bonus", Credits: 40, Kind: packs.KindBonus, Source: packs.SourceKIFF, SourceTxn: txn("b"), At: now}); err != nil {
			t.Fatal(err)
		}
		if got := bought(); len(got) != 0 {
			t.Fatalf("a bonus isn't bought: %v", got)
		}
		first := packs.CreditGrant{TenantID: tenant, Pack: "credits-firstmonth", Credits: 7000, Source: packs.SourceGoogle, SourceTxn: txn("1"), At: now}
		if _, created, err := s.RecordCredits(ctx, first); err != nil || !created {
			t.Fatalf("the first: %v %v", created, err)
		}
		if got := bought(); len(got) != 1 || !got["credits-firstmonth"] {
			t.Fatalf("bought: %v", got)
		}
		// A second receipt, another transaction: recorded, its credits added.
		second := first
		second.SourceTxn = txn("2")
		if p, created, err := s.RecordCredits(ctx, second); err != nil || !created || p.CreditsLeft != 7000 {
			t.Fatalf("a second receipt: %+v %v %v", p, created, err)
		}
		if _, left, err := s.Credits(ctx, tenant, now.AddDate(0, -1, 0), now); err != nil || left != 14040 {
			t.Fatalf("both packs' credits, and the bonus: %d %v", left, err)
		}
		// Refunded, both: still bought, so it isn't offered again.
		for _, n := range []string{"1", "2"} {
			if _, found, err := s.RevokeCredits(ctx, packs.SourceGoogle, txn(n), now); err != nil || !found {
				t.Fatalf("refund %s: %v %v", n, found, err)
			}
		}
		if got := bought(); !got["credits-firstmonth"] {
			t.Fatalf("a refunded pack is still bought: %v", got)
		}
		if got, err := b.BoughtPacks(ctx, tenant+"-other"); err != nil || len(got) != 0 {
			t.Fatalf("another account: %v %v", got, err)
		}
	})
}
