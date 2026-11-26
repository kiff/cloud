package packs_test

import (
	"context"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/packs"
)

// Reset store billing sets aside an account's App Store and Google Play packs
// with credits left: they're kept, with nothing left, and no longer count or
// get drawn. A bonus and a Stripe pack stay, another account's packs stay, a
// store's later word on a set-aside pack doesn't bring it back, and a second
// reset sets nothing more aside.
func TestStorePacksSetAside(t *testing.T) {
	run := func(t *testing.T, s packs.CreditLedger, tenant string, setAside func(tenant string, at time.Time) (packs.StorePacksSetAside, error)) {
		ctx := context.Background()
		record := func(who, pack string, credits int64, kind packs.Kind, src packs.Source, txn string) {
			t.Helper()
			if _, _, err := s.RecordCredits(ctx, packs.CreditGrant{TenantID: who, Pack: pack, Credits: credits, Kind: kind,
				Source: src, SourceTxn: tenant + "-" + txn, At: now.Add(-time.Hour)}); err != nil {
				t.Fatal(err)
			}
		}
		record(tenant, "credits-500", 500, packs.KindBought, packs.SourceApple, "apple-500")
		record(tenant, "credits-5000", 5000, packs.KindBought, packs.SourceApple, "apple-5000")
		record(tenant, "credits-1500", 1500, packs.KindBought, packs.SourceGoogle, "play-1500")
		record(tenant, "bonus", 40, packs.KindBonus, packs.SourceKIFF, "bonus")
		record(tenant, "c500", 500, packs.KindBought, packs.SourceStripe, "stripe-500")
		// One refunded already: nothing left to set aside.
		record(tenant, "credits-500", 500, packs.KindBought, packs.SourceApple, "apple-refunded")
		if _, _, err := s.RevokeCredits(ctx, packs.SourceApple, tenant+"-apple-refunded", now.Add(-time.Minute)); err != nil {
			t.Fatal(err)
		}
		other := tenant + "-other"
		record(other, "credits-500", 500, packs.KindBought, packs.SourceApple, "other-500")

		aside, err := setAside(tenant, now)
		if err != nil || aside != (packs.StorePacksSetAside{Packs: 3, Credits: 7000}) {
			t.Fatalf("set aside: %+v %v", aside, err)
		}
		if _, left, err := s.Credits(ctx, tenant, now.AddDate(0, -1, 0), now); err != nil || left != 540 {
			t.Fatalf("left after the reset: %d %v (want the bonus and Stripe's pack, 540)", left, err)
		}
		left, err := s.CreditPacks(ctx, tenant)
		if err != nil || len(left) != 2 {
			t.Fatalf("packs drawn from after the reset: %+v %v", left, err)
		}
		for _, p := range left {
			if p.Source != packs.SourceKIFF && p.Source != packs.SourceStripe {
				t.Fatalf("a store pack is still drawn from: %+v", p)
			}
		}
		if _, left, _ := s.Credits(ctx, other, now.AddDate(0, -1, 0), now); left != 500 {
			t.Fatalf("another account's pack: %d left", left)
		}
		// The app sending a set-aside pack's transaction again: still set aside.
		again, created, err := s.RecordCredits(ctx, packs.CreditGrant{TenantID: tenant, Pack: "credits-5000", Credits: 5000,
			Source: packs.SourceApple, SourceTxn: tenant + "-apple-5000", At: now})
		if err != nil || created || again.CreditsLeft != 0 || again.RevokedAt == nil {
			t.Fatalf("a set-aside pack sent again: %+v %v %v", again, created, err)
		}
		// The store refunding one: nothing more to take back.
		if back, found, err := s.RevokeCredits(ctx, packs.SourceGoogle, tenant+"-play-1500", now); err != nil || !found || back.TakenBack != 0 {
			t.Fatalf("a set-aside pack refunded: %+v %v %v", back, found, err)
		}
		if aside, err := setAside(tenant, now.Add(time.Hour)); err != nil || aside != (packs.StorePacksSetAside{}) {
			t.Fatalf("a second reset: %+v %v", aside, err)
		}
	}
	t.Run("memory", func(t *testing.T) {
		m := packs.NewMemory()
		run(t, m, "t-mem", func(tenant string, at time.Time) (packs.StorePacksSetAside, error) {
			want := m.StorePacks(tenant)
			if got := m.SetAsideStorePacks(tenant, at); got != want {
				t.Fatalf("StorePacks %+v, then SetAsideStorePacks %+v", want, got)
			}
			return want, nil
		})
	})
	t.Run("postgres", func(t *testing.T) {
		pool, tenant := creditDB(t)
		ctx := context.Background()
		if _, err := pool.Exec(ctx, `INSERT INTO cloud_meta.tenants VALUES ($1)`, tenant+"-other"); err != nil {
			t.Fatal(err)
		}
		run(t, packs.NewPostgres(pool), tenant, func(tenant string, at time.Time) (packs.StorePacksSetAside, error) {
			return packs.SetAsideStorePacksQ(ctx, pool, tenant, at)
		})
	})
}
