package handlers

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/agents"
	"github.com/kiff/cloud/api/internal/appstore"
	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/membership"
	"github.com/kiff/cloud/api/internal/packs"
	"github.com/kiff/cloud/api/internal/tenant"
)

const firstMonth = "credits-firstmonth"

// failingBought is a read of what an account bought that fails.
type failingBought struct{}

func (failingBought) BoughtPacks(context.Context, string) (map[string]bool, error) {
	return nil, errors.New("db down")
}

// firstMonthStore is an account's usage read on the production pack table,
// with Google Play billing set up and bought as its read of what the
// account bought.
func firstMonthStore(t *testing.T, meta map[string]any, bought packs.BoughtRead) (*usageRig, *packs.Memory) {
	t.Helper()
	r := newUsageRig(t, meta)
	catalog, err := packs.LoadCatalog()
	if err != nil {
		t.Fatal(err)
	}
	r.catalog = catalog
	ledger := packs.NewMemory()
	if bought == nil {
		bought = ledger
	}
	r.mux = http.NewServeMux()
	NewMeterUsageHandler(r.tenants, r.meter, slog.Default()).WithPacks(ledger, catalog).WithRequests(ledger).WithBought(bought).
		WithApple(appstore.NewMemoryAccounts()).WithGoogle().
		WithClock(func() time.Time { return r.now }).Register(r.mux)
	return r, ledger
}

// storePacks is the store's packs in a usage read, by id.
func storePacks(t *testing.T, out map[string]any) map[string]map[string]any {
	t.Helper()
	by := map[string]map[string]any{}
	for _, p := range out["store"].(map[string]any)["packs"].([]any) {
		m := p.(map[string]any)
		by[m["id"].(string)] = m
	}
	return by
}

// The store lists the First month pack, marked once, with its Google
// product and no Apple product key, until the account has bought it. A
// second receipt for it is still recorded with its credits, and the pack
// stays off the list; the other packs are listed throughout, unmarked.
func TestTheFirstMonthPackIsListedUntilBought(t *testing.T) {
	ctx := context.Background()
	for _, plan := range []map[string]any{
		{}, // no plan yet: listed beside Solo
		{tenant.MetadataKeyPlan: "solo", tenant.MetadataKeyBillingSource: "google"},
	} {
		r, ledger := firstMonthStore(t, plan, nil)
		list := storePacks(t, r.get("/v1/me/usage"))
		p, ok := list[firstMonth]
		if !ok || len(list) != 4 {
			t.Fatalf("plan %v: the store's packs: %v", plan, list)
		}
		if p["once"] != true || num(p["credits"]) != 7000 || p["google_product"] != "bot.tafs.credits.firstmonth" ||
			p["line"] != "First month pack · 7,000 credits" || p["name"] != "First month pack" {
			t.Fatalf("the First month pack: %v", p)
		}
		if _, has := p["apple_product"]; has {
			t.Fatalf("the pack carries an Apple product key the App Store doesn't sell: %v", p)
		}
		for id, other := range list {
			if id == firstMonth {
				continue
			}
			if _, marked := other["once"]; marked || other["apple_product"] == "" || other["apple_product"] == nil {
				t.Fatalf("%s: %v", id, other)
			}
		}

		buy := func(txn string) {
			t.Helper()
			if _, created, err := ledger.RecordCredits(ctx, packs.CreditGrant{TenantID: r.id, Pack: firstMonth, Credits: 7000,
				Source: packs.SourceGoogle, SourceTxn: txn, At: r.now}); err != nil || !created {
				t.Fatalf("receipt %s: %v %v", txn, created, err)
			}
		}
		buy("gpa-1")
		if list := storePacks(t, r.get("/v1/me/usage")); len(list) != 3 || list[firstMonth] != nil {
			t.Fatalf("plan %v: offered again once bought: %v", plan, list)
		}
		// A second receipt: the customer paid, so its credits are recorded.
		buy("gpa-2")
		if _, left, err := ledger.Credits(ctx, r.id, r.now.AddDate(0, -1, 0), r.now); err != nil || left != 14000 {
			t.Fatalf("both receipts' credits: %d %v", left, err)
		}
		if list := storePacks(t, r.get("/v1/me/usage")); len(list) != 3 || list[firstMonth] != nil {
			t.Fatalf("plan %v: offered after a second receipt: %v", plan, list)
		}
		// Refunded: still not offered again.
		if _, _, err := ledger.RevokeCredits(ctx, packs.SourceGoogle, "gpa-1", r.now); err != nil {
			t.Fatal(err)
		}
		if _, _, err := ledger.RevokeCredits(ctx, packs.SourceGoogle, "gpa-2", r.now); err != nil {
			t.Fatal(err)
		}
		if list := storePacks(t, r.get("/v1/me/usage")); list[firstMonth] != nil {
			t.Fatalf("plan %v: offered again after a refund: %v", plan, list)
		}
	}
}

// When what the account bought can't be read, the usage read fails: the
// pack is never offered on a guess.
func TestTheStoreFailsWhenBoughtCantBeRead(t *testing.T) {
	r, _ := firstMonthStore(t, map[string]any{tenant.MetadataKeyPlan: "solo"}, failingBought{})
	admin := auth.Identity{Subject: "alice", Tenant: r.id, MemberRole: membership.RoleAdmin, Roles: membership.IdentityRoles(membership.RoleAdmin)}
	if rr := serve(r.mux, admin, http.MethodGet, "/v1/me/usage", "", nil); rr.Code == http.StatusOK {
		t.Fatalf("the usage read answered with what was bought unread: %s", rr.Body.String())
	}
}

// firstMonthBalance is the credits read of an account with meta, on the
// production pack table.
func firstMonthBalance(t *testing.T, meta map[string]any, bought func(*packs.Memory) packs.BoughtRead) *creditsRig {
	t.Helper()
	r := &creditsRig{t: t, ledger: &fakeLedger{tafs: []string{"agt-refunds"}}, tenants: tenant.NewMemoryStore(), packs: packs.NewMemory(),
		alerts: &alertsSeen{}, mux: http.NewServeMux(), id: tenant.NewID(), now: time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)}
	if err := r.tenants.Create(context.Background(), tenant.Tenant{ID: r.id, Slug: "acme", Status: tenant.StatusActive, Metadata: meta}); err != nil {
		t.Fatal(err)
	}
	catalog, err := packs.LoadCatalog()
	if err != nil {
		t.Fatal(err)
	}
	h := NewCreditsHandler(r.tenants, r.ledger, slog.Default()).WithClock(func() time.Time { return r.now }).WithAgentNames(agents.NewMemoryStore()).
		WithCreditPacks(r.packs).WithCatalog(catalog).WithRequests(r.packs, r.alerts)
	if bought != nil {
		h.WithBought(bought(r.packs))
	}
	h.Register(r.mux)
	return r
}

// The balance says whether the First month pack is still on offer
// (can_buy.once): to someone who manages billing, beside Solo on an account
// with no plan and while Solo runs, until the account has bought it. Never
// to a member, never on Company, and never when what the account bought
// isn't read.
func TestTheBalanceOffersTheFirstMonthPackOnce(t *testing.T) {
	ctx := context.Background()
	mine := func(m *packs.Memory) packs.BoughtRead { return m }
	const path = "/v1/me/usage/credits"

	// No plan: Solo is offered, and the pack beside it. The pack is bought
	// once Solo runs, so packs stays false: the app shows its row with no
	// button.
	r := firstMonthBalance(t, map[string]any{}, mine)
	if v := r.balance(r.admin(), path); !v.CanBuy.Solo || v.CanBuy.Packs || !v.CanBuy.Once {
		t.Fatalf("no plan: %+v", v.CanBuy)
	}
	if v := r.balance(r.member(), path); v.CanBuy.Solo || v.CanBuy.Once {
		t.Fatalf("a member with no plan: %+v", v.CanBuy)
	}

	// Solo runs: packs are offered, the First month pack among them.
	r = firstMonthBalance(t, map[string]any{tenant.MetadataKeyPlan: "solo", tenant.MetadataKeyBillingSource: "google"}, mine)
	if v := r.balance(r.admin(), path); v.CanBuy.Solo || !v.CanBuy.Packs || !v.CanBuy.Once {
		t.Fatalf("Solo: %+v", v.CanBuy)
	}
	// Another pack bought changes nothing.
	if _, _, err := r.packs.RecordCredits(ctx, packs.CreditGrant{TenantID: r.id, Pack: "credits-500", Credits: 500, Source: packs.SourceGoogle, SourceTxn: "g-0", At: r.now}); err != nil {
		t.Fatal(err)
	}
	if v := r.balance(r.admin(), path); !v.CanBuy.Once {
		t.Fatalf("after another pack: %+v", v.CanBuy)
	}
	// Bought: no longer offered, the other packs still are.
	if _, _, err := r.packs.RecordCredits(ctx, packs.CreditGrant{TenantID: r.id, Pack: firstMonth, Credits: 7000, Source: packs.SourceGoogle, SourceTxn: "g-1", At: r.now}); err != nil {
		t.Fatal(err)
	}
	if v := r.balance(r.admin(), path); !v.CanBuy.Packs || v.CanBuy.Once {
		t.Fatalf("once bought: %+v", v.CanBuy)
	}

	// Company buys no packs.
	r = firstMonthBalance(t, map[string]any{tenant.MetadataKeyPlan: "team"}, mine)
	if v := r.balance(r.admin(), path); v.CanBuy.Once || v.CanBuy.Packs {
		t.Fatalf("Company: %+v", v.CanBuy)
	}

	// With no read of what the account bought, it isn't offered; with one
	// that fails, the read fails.
	r = firstMonthBalance(t, map[string]any{}, nil)
	if v := r.balance(r.admin(), path); v.CanBuy.Once {
		t.Fatalf("offered with what was bought unread: %+v", v.CanBuy)
	}
	r = firstMonthBalance(t, map[string]any{}, func(*packs.Memory) packs.BoughtRead { return failingBought{} })
	if rr := serve(r.mux, r.admin(), http.MethodGet, path, "", nil); rr.Code != http.StatusInternalServerError {
		t.Fatalf("a failed read of what was bought: %d %s", rr.Code, rr.Body.String())
	}
}
