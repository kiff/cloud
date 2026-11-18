package packs_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kiff/cloud/api/internal/packs"
	"github.com/kiff/cloud/api/internal/testdb"
)

type store interface {
	packs.Ledger
	packs.Requests
}

// stores runs f on the memory store and, with a database, on Postgres.
func stores(t *testing.T, f func(t *testing.T, s store, tenant string)) {
	t.Run("memory", func(t *testing.T) { f(t, packs.NewMemory(), "t-mem") })
	t.Run("postgres", func(t *testing.T) {
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
		if err := packs.ApplySchema(ctx, pool); err != nil {
			t.Fatal(err)
		}
		tenant := fmt.Sprintf("t-%s-%d", t.Name()[:4], time.Now().UnixNano())
		if _, err := pool.Exec(ctx, `INSERT INTO cloud_meta.tenants VALUES ($1)`, tenant); err != nil {
			t.Fatal(err)
		}
		f(t, packs.NewPostgres(pool), tenant)
	})
}

var now = time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)

// A pack is recorded once per store transaction; tasks are drawn oldest
// pack first, once per task; bought tasks last 12 months and bonus ones
// 90 days; a refunded pack's tasks are taken back.
func TestLedger(t *testing.T) {
	stores(t, func(t *testing.T, s store, tenant string) {
		ctx := context.Background()
		txn := func(name string) string { return tenant + "-" + name }
		older, created, err := s.Record(ctx, packs.Grant{TenantID: tenant, Pack: "pico", Tasks: 2, Source: packs.SourceApple, SourceTxn: txn("1"), At: now.AddDate(0, -3, 0)})
		if err != nil || !created || older.TasksLeft != 2 || !older.ExpiresAt.Equal(now.AddDate(0, 9, 0)) {
			t.Fatalf("record: %+v %v %v", older, created, err)
		}
		again, created, err := s.Record(ctx, packs.Grant{TenantID: tenant, Pack: "maxi", Tasks: 450, Source: packs.SourceApple, SourceTxn: txn("1"), At: now})
		if err != nil || created || again.ID != older.ID || again.Tasks != 2 {
			t.Fatalf("a repeated transaction recorded again: %+v %v %v", again, created, err)
		}
		bonus, _, err := s.Record(ctx, packs.Grant{TenantID: tenant, Pack: "bonus", Tasks: 5, Kind: packs.KindBonus, Source: packs.SourceKIFF, SourceTxn: txn("b"), At: now.AddDate(0, 0, -1)})
		if err != nil || !bonus.ExpiresAt.Equal(now.AddDate(0, 0, 89)) {
			t.Fatalf("bonus: %+v %v", bonus, err)
		}
		// Expired a day ago.
		if _, _, err := s.Record(ctx, packs.Grant{TenantID: tenant, Pack: "vonk", Tasks: 30, Source: packs.SourceStripe, SourceTxn: txn("old"), At: now.AddDate(-1, 0, -1)}); err != nil {
			t.Fatal(err)
		}
		from := now.AddDate(0, 0, -10)
		if drawn, left, err := s.Allowance(ctx, tenant, from, now); err != nil || drawn != 0 || left != 7 {
			t.Fatalf("allowance: %d %d %v", drawn, left, err)
		}
		// Three draws: the older pack's two, then the bonus.
		for i, key := range []string{"a", "b", "c", "c"} {
			if err := s.Draw(ctx, tenant, key, now.Add(-time.Duration(i)*time.Minute)); err != nil {
				t.Fatalf("draw %s: %v", key, err)
			}
		}
		valid, err := s.Valid(ctx, tenant, now)
		if err != nil || len(valid) != 1 || valid[0].ID != bonus.ID || valid[0].TasksLeft != 4 {
			t.Fatalf("valid after draws: %+v %v", valid, err)
		}
		if drawn, left, _ := s.Allowance(ctx, tenant, from, now); drawn != 3 || left != 4 {
			t.Fatalf("allowance after draws: %d %d", drawn, left)
		}
		// A task done before any pack was bought draws nothing.
		if err := s.Draw(ctx, tenant, "early", now.AddDate(-2, 0, 0)); !errors.Is(err, packs.ErrNoTasks) {
			t.Fatalf("a draw before the packs: %v", err)
		}
		// Refunded: its tasks are taken back.
		if e, found, err := s.Revoke(ctx, packs.SourceKIFF, txn("b"), now); err != nil || !found || e.TasksLeft != 0 || e.RevokedAt == nil {
			t.Fatalf("revoke: %+v %v %v", e, found, err)
		}
		if _, found, _ := s.Revoke(ctx, packs.SourceKIFF, txn("nothing"), now); found {
			t.Fatal("revoked a pack that doesn't exist")
		}
		if err := s.Draw(ctx, tenant, "d", now); !errors.Is(err, packs.ErrNoTasks) {
			t.Fatalf("drew from a refunded pack: %v", err)
		}
		// Past the bought pack's 12 months, its tasks no longer count.
		if _, left, _ := s.Allowance(ctx, tenant, from, now.AddDate(1, 0, 0)); left != 0 {
			t.Fatalf("an expired pack still counts: %d", left)
		}
	})
}

// A take-back that comes before its pack is recorded is kept: the pack is
// recorded taken back, with no tasks, answering no request, and the
// take-back is used once.
func TestTakeBackBeforeRecord(t *testing.T) {
	stores(t, func(t *testing.T, s store, tenant string) {
		ctx := context.Background()
		txn := tenant + "-early"
		if _, found, err := s.Revoke(ctx, packs.SourceStripe, txn, now.Add(-time.Hour)); err != nil || found {
			t.Fatalf("take back first: %v %v", found, err)
		}
		req, err := s.Ask(ctx, packs.Request{TenantID: tenant, AskedBy: "member", Pack: "rayo", CreatedAt: now.AddDate(0, 0, -1), ExpiresAt: now.AddDate(0, 0, 9)})
		if err != nil {
			t.Fatal(err)
		}
		e, created, err := s.Record(ctx, packs.Grant{TenantID: tenant, Pack: "rayo", Tasks: 70, Source: packs.SourceStripe, SourceTxn: txn, RequestID: req.ID, At: now.Add(-2 * time.Hour)})
		if err != nil || !created || e.TasksLeft != 0 || e.RevokedAt == nil || !e.RevokedAt.Equal(now.Add(-time.Hour)) || e.RequestID != "" {
			t.Fatalf("recorded after its take-back: %+v %v %v", e, created, err)
		}
		if r, _ := s.Get(ctx, tenant, req.ID, now); r.Status != packs.RequestOpen {
			t.Fatalf("a pack taken back answered the request: %+v", r)
		}
		if _, left, _ := s.Allowance(ctx, tenant, now.AddDate(0, 0, -10), now); left != 0 {
			t.Fatalf("a pack taken back counts: %d", left)
		}
		// A take-back past KeepTakeBack is pruned by the next one: a payment
		// that never was a pack's (a plan's invoice) doesn't stay.
		if _, _, err := s.Revoke(ctx, packs.SourceStripe, tenant+"-invoice", now); err != nil {
			t.Fatal(err)
		}
		if _, _, err := s.Revoke(ctx, packs.SourceStripe, tenant+"-later", now.Add(packs.KeepTakeBack+time.Hour)); err != nil {
			t.Fatal(err)
		}
		if e, _, err := s.Record(ctx, packs.Grant{TenantID: tenant, Pack: "pico", Tasks: 12, Source: packs.SourceStripe, SourceTxn: tenant + "-invoice", At: now}); err != nil || e.TasksLeft != 12 || e.RevokedAt != nil {
			t.Fatalf("a pruned take-back still applied: %+v %v", e, err)
		}
		// The same transaction from the other store is another pack.
		if e, _, err := s.Record(ctx, packs.Grant{TenantID: tenant, Pack: "rayo", Tasks: 70, Source: packs.SourceApple, SourceTxn: txn, At: now}); err != nil || e.TasksLeft != 70 {
			t.Fatalf("another store's: %+v %v", e, err)
		}
	})
}

// Two draws of one task at once take one task.
func TestLedgerDrawsOnce(t *testing.T) {
	stores(t, func(t *testing.T, s store, tenant string) {
		ctx := context.Background()
		if _, _, err := s.Record(ctx, packs.Grant{TenantID: tenant, Pack: "vonk", Tasks: 30, Source: packs.SourceStripe, SourceTxn: tenant + "-1", At: now.AddDate(0, 0, -1)}); err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if err := s.Draw(ctx, tenant, "same", now); err != nil {
					t.Error(err)
				}
			}()
		}
		wg.Wait()
		if drawn, left, _ := s.Allowance(ctx, tenant, now.AddDate(0, 0, -1), now); drawn != 1 || left != 29 {
			t.Fatalf("drawn %d, left %d; want 1 and 29", drawn, left)
		}
	})
}

// A member asks once at a time; the first answer counts: a pack bought
// with the request's id approves it, and a decline or withdrawal after
// that changes nothing. An open request past its end reads as expired,
// and frees the member to ask again.
func TestRequests(t *testing.T) {
	stores(t, func(t *testing.T, s store, tenant string) {
		ctx := context.Background()
		end := now.AddDate(0, 0, 5)
		r, err := s.Ask(ctx, packs.Request{TenantID: tenant, AskedBy: "member", Pack: "rayo", Reason: "  the refunds taf ran out ", CreatedAt: now, ExpiresAt: end})
		if err != nil || r.Status != packs.RequestOpen || r.Reason != "the refunds taf ran out" {
			t.Fatalf("ask: %+v %v", r, err)
		}
		if _, err := s.Ask(ctx, packs.Request{TenantID: tenant, AskedBy: "member", Pack: "pico", CreatedAt: now, ExpiresAt: end}); !errors.Is(err, packs.ErrAlreadyAsked) {
			t.Fatalf("a second open request: %v", err)
		}
		if _, err := s.Withdraw(ctx, tenant, r.ID, "someone-else", now); !errors.Is(err, packs.ErrRequestNotFound) {
			t.Fatalf("withdrawn by someone else: %v", err)
		}
		if open, _ := s.List(ctx, tenant, "", true, now.AddDate(0, -1, 0), now); len(open) != 1 || open[0].ID != r.ID {
			t.Fatalf("open: %+v", open)
		}
		e, _, err := s.Record(ctx, packs.Grant{TenantID: tenant, Pack: "rayo", Tasks: 70, Source: packs.SourceApple, SourceTxn: tenant + "-r", RequestID: r.ID, AnsweredBy: "admin", At: now.Add(time.Minute)})
		if err != nil {
			t.Fatal(err)
		}
		got, err := s.Get(ctx, tenant, r.ID, now.Add(time.Minute))
		if err != nil || got.Status != packs.RequestApproved || got.PackID != e.ID || got.AnsweredBy != "admin" {
			t.Fatalf("approved: %+v %v", got, err)
		}
		if _, err := s.Decline(ctx, tenant, r.ID, "admin2", "no", now.Add(2*time.Minute)); !errors.Is(err, packs.ErrAnswered) {
			t.Fatalf("declined after approval: %v", err)
		}

		// Declined, with a note; then the member asks again.
		r2, err := s.Ask(ctx, packs.Request{TenantID: tenant, AskedBy: "member", Pack: "pico", CreatedAt: now.Add(time.Hour), ExpiresAt: end})
		if err != nil {
			t.Fatal(err)
		}
		if d, err := s.Decline(ctx, tenant, r2.ID, "admin", " next month ", now.Add(2*time.Hour)); err != nil || d.Status != packs.RequestDeclined || d.Note != "next month" {
			t.Fatalf("decline: %+v %v", d, err)
		}
		// Withdrawn by the member.
		r3, err := s.Ask(ctx, packs.Request{TenantID: tenant, AskedBy: "member", Pack: "pico", CreatedAt: now.Add(3 * time.Hour), ExpiresAt: end})
		if err != nil {
			t.Fatal(err)
		}
		if w, err := s.Withdraw(ctx, tenant, r3.ID, "member", now.Add(4*time.Hour)); err != nil || w.Status != packs.RequestWithdrawn {
			t.Fatalf("withdraw: %+v %v", w, err)
		}
		// Expired at the period's end.
		r4, err := s.Ask(ctx, packs.Request{TenantID: tenant, AskedBy: "member", Pack: "pico", CreatedAt: now.Add(5 * time.Hour), ExpiresAt: end})
		if err != nil {
			t.Fatal(err)
		}
		if g, _ := s.Get(ctx, tenant, r4.ID, end); g.Status != packs.RequestExpired {
			t.Fatalf("past its end: %+v", g)
		}
		if _, err := s.Decline(ctx, tenant, r4.ID, "admin", "", end); !errors.Is(err, packs.ErrAnswered) {
			t.Fatalf("declined an expired request: %v", err)
		}
		if _, err := s.Ask(ctx, packs.Request{TenantID: tenant, AskedBy: "member", Pack: "pico", CreatedAt: end, ExpiresAt: end.AddDate(0, 1, 0)}); err != nil {
			t.Fatalf("asking after one expired: %v", err)
		}
		mine, _ := s.List(ctx, tenant, "member", false, now.AddDate(0, -1, 0), end)
		if len(mine) != 5 {
			t.Fatalf("the member's requests: %d", len(mine))
		}
	})
}

// The pack table: five packs with their tasks and store ids, each shown
// with its line; a broken table is refused.
func TestCatalog(t *testing.T) {
	c, err := packs.LoadCatalog()
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int{"pico": 12, "vonk": 30, "rayo": 70, "forte": 200, "maxi": 450}
	// Solo's packs of credits, sold on Google Play (and on the App Store
	// once its products exist), each under the same product id in both.
	credits := map[string]struct {
		credits int64
		product string
		line    string
	}{
		"credits-500":  {500, "bot.tafs.credits.500", "500 credits"},
		"credits-1500": {1500, "bot.tafs.credits.1500", "1,500 credits"},
		"credits-5000": {5000, "bot.tafs.credits.5000", "5,000 credits"},
	}
	// And the First month pack: 7,000 credits, sold once per account, on
	// Google Play only until the App Store has its product.
	const first = "credits-firstmonth"
	if len(c.Packs) != len(want)+len(credits)+1 || len(c.CreditPacks()) != len(credits)+1 || c.Solo.AppleProduct == "" {
		t.Fatalf("catalog: %+v", c)
	}
	fm, ok := c.Pack(first)
	if !ok || fm.Credits != 7000 || !fm.Once || fm.GoogleProduct != "bot.tafs.credits.firstmonth" || fm.AppleProduct != "" ||
		fm.Line() != "First month pack · 7,000 credits" {
		t.Fatalf("the First month pack: %+v %q", fm, fm.Line())
	}
	if q, ok := c.ByGoogleProduct("bot.tafs.credits.firstmonth"); !ok || q.ID != first {
		t.Fatalf("the First month pack by Google's product: %+v", q)
	}
	// A pack with no Apple product is never found by an empty one.
	if q, ok := c.ByAppleProduct(""); ok {
		t.Fatalf("a pack found by no Apple product: %+v", q)
	}
	if once := c.OncePacks(); len(once) != 1 || once[0].ID != first {
		t.Fatalf("the packs sold once: %+v", once)
	}
	// Offered: every pack of credits, less the once-only one once bought.
	if all := c.Offered(func(string) bool { return false }); len(all) != len(credits)+1 || all[len(all)-1].ID != first {
		t.Fatalf("offered before it's bought: %+v", all)
	}
	if rest := c.Offered(func(id string) bool { return true }); len(rest) != len(credits) {
		t.Fatalf("offered once it's bought: %+v", rest)
	}
	for id, w := range credits {
		p, ok := c.Pack(id)
		if !ok || p.Once || p.Credits != w.credits || p.Tasks != 0 || p.AppleProduct != w.product || p.GoogleProduct != w.product || p.Line() != w.line {
			t.Fatalf("%s: %+v %q", id, p, p.Line())
		}
		if q, ok := c.ByGoogleProduct(w.product); !ok || q.ID != id {
			t.Fatalf("%s by Google's product: %+v", id, q)
		}
	}
	for id, tasks := range want {
		p, ok := c.Pack(id)
		if !ok || p.Tasks != tasks {
			t.Fatalf("%s: %+v", id, p)
		}
		if q, ok := c.ByAppleProduct(p.AppleProduct); !ok || q.ID != id {
			t.Fatalf("%s by its product: %+v", id, q)
		}
	}
	if p, _ := c.Pack("rayo"); p.Line() != "Rayo · 70 tasks" {
		t.Fatalf("line: %q", p.Line())
	}
	// No pack is sold on kiff.dev: none has a Stripe price. Packs of tasks
	// stay for the stores' late receipts and refunds of them.
	for _, p := range c.Packs {
		if p.StripePrice != "" || p.GoogleProduct == "" {
			t.Fatalf("%s: %+v", p.ID, p)
		}
	}
	for name, raw := range map[string]string{
		"a pack of tasks sold once":             `{"packs": [{"id": "x", "name": "X", "tasks": 5, "once": true, "apple_product": "a.x"}]}`,
		"a pack of credits in no store":         `{"packs": [{"id": "x", "name": "X", "credits": 5}]}`,
		"a pack of tasks with no Apple product": `{"packs": [{"id": "x", "name": "X", "tasks": 5, "google_product": "g.x"}]}`,
		"no packs":                              `{"packs": []}`,
		"no tasks":                              `{"packs": [{"id": "a", "name": "A", "stripe_price": "s", "apple_product": "p"}]}`,
		"a repeated id":                         `{"packs": [{"id": "a", "name": "A", "tasks": 1, "stripe_price": "s", "apple_product": "p"}, {"id": "b", "name": "B", "tasks": 1, "stripe_price": "s", "apple_product": "q"}]}`,
		"not json":                              `{`,
		"no apple id":                           `{"packs": [{"id": "a", "name": "A", "tasks": 1, "stripe_price": "s"}]}`,
		"tasks and credits":                     `{"packs": [{"id": "a", "name": "A", "tasks": 1, "credits": 5, "stripe_price": "s", "apple_product": "p"}]}`,
		// Packs of credits are sold in the app stores only.
		"credits on stripe": `{"packs": [{"id": "a", "name": "A", "credits": 500, "stripe_price": "s", "apple_product": "p"}]}`,
		// A product sold as a pack of tasks never becomes credits.
		"apple's tasks as credits":  `{"packs": [{"id": "a", "name": "A", "credits": 500, "apple_product": "bot.tafs.pack.pico"}]}`,
		"google's tasks as credits": `{"packs": [{"id": "a", "name": "A", "credits": 500, "apple_product": "p", "google_product": "bot.tafs.pack.maxi"}]}`,
	} {
		if _, err := packs.ParseCatalog([]byte(raw)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// Packs of credits: no Stripe price, so two of them don't share one.
	c, err = packs.ParseCatalog([]byte(`{"packs": [
		{"id": "pico", "name": "Pico", "tasks": 12, "stripe_price": "kiff_pack_pico", "apple_product": "bot.tafs.pack.pico"},
		{"id": "c1", "name": "Uno", "credits": 500, "apple_product": "bot.tafs.credits.uno", "google_product": "bot.tafs.credits.uno"},
		{"id": "c2", "name": "Dos", "credits": 1500, "apple_product": "bot.tafs.credits.dos"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := c.CreditPacks(); len(got) != 2 || got[1].Line() != "1,500 credits" {
		t.Fatalf("packs of credits: %+v", got)
	}
	if p, ok := c.ByGoogleProduct("bot.tafs.credits.uno"); !ok || p.Credits != 500 {
		t.Fatalf("by Google's product: %+v", p)
	}
}
