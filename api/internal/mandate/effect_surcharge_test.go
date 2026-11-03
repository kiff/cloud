package mandate_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/mandate"
)

func verified(tenant, prop string, decided, vendor int64, at time.Time) mandate.VerifiedEvidence {
	r := refund(decided)
	return mandate.VerifiedEvidence{TenantID: tenant, ProposalID: prop, Subject: r.Subject, Action: r.Action, AmountArg: "amount_cents",
		Decided: decided, Vendor: vendor, At: at, Source: "gateway:verify_read"}
}

// A vendor that used more than its Card decided is counted at the larger
// figure, once, on the amount's own limit only; one that used less, or a
// call count, changes nothing (connection profiles: the verify read).
func testSurcharges(t *testing.T, s mandate.Store, tenant string) {
	t.Helper()
	ctx := context.Background()
	m := homeCard("surcharged", 50000, 500000)
	m.TenantID = tenant
	m.Aggregates = append(m.Aggregates, mandate.Aggregate{Quantity: mandate.Quantity{Kind: mandate.KindCount}, Limit: 10, Window: mandate.WindowCalendarDay})
	if err := s.Issue(ctx, m, "owner"); err != nil {
		t.Fatal(err)
	}
	a := mandate.NewAuthorizer(s, nil, at(base))
	if d, err := a.Authorize(ctx, tenant, "verified-op", refund(40000)); err != nil || !d.Allowed() {
		t.Fatal(d, err)
	}
	ss := s.(mandate.EffectSurchargeStore)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := ss.SurchargeEffects(ctx, verified(tenant, "verified-op", 40000, 45000, base.Add(time.Second))); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	now := base.Add(2 * time.Second)
	for _, agg := range m.Aggregates {
		b, err := mandate.ReadLimitBalance(ctx, s, tenant, m, agg, now, "")
		want := int64(45000)
		if agg.Quantity.Kind == mandate.KindCount {
			want = 1
		}
		if err != nil || b.Net != want {
			t.Fatalf("%s: %+v %v, want %d", agg.Quantity.Key(), b, err, want)
		}
	}
	// Before it was recorded, the Card counted what it decided.
	if b, _ := mandate.ReadLimitBalance(ctx, s, tenant, m, m.Aggregates[0], base.Add(time.Second/2), ""); b.Net != 40000 {
		t.Fatalf("before the read: %+v", b)
	}
	// A vendor that used less, or the same, is not a surcharge.
	if _, err := ss.SurchargeEffects(ctx, verified(tenant, "verified-op", 40000, 30000, now)); err == nil {
		t.Fatal("a smaller vendor figure was recorded")
	}
	// Evidence for another amount than the one drawn charges nothing.
	if out, err := ss.SurchargeEffects(ctx, verified(tenant, "verified-op", 39000, 60000, now)); err != nil || len(out) != 0 {
		t.Fatalf("another decided amount: %+v %v", out, err)
	}
}

func TestEffectSurchargeCountsTheLargerFigureOnce(t *testing.T) {
	testSurcharges(t, mandate.NewMemoryStore(), tenantID)
}

func TestPostgres_EffectSurchargeCountsTheLargerFigureOnce(t *testing.T) {
	p := requirePostgres(t)
	seedTenant(t, p, "effects-surcharged")
	testSurcharges(t, mandate.NewPostgresStore(p), "effects-surcharged")
}
