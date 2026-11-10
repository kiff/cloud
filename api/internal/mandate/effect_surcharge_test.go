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

// Room the owner's "No" gave back is taken again, once, when the refund is
// found after all: the Card counts the call as it did before the "No". A
// release from any other source is never taken back, and a call with no
// release of that source changes nothing.
func testReinstates(t *testing.T, s mandate.Store, tenant string) {
	t.Helper()
	ctx := context.Background()
	m := homeCard("reinstated", 50000, 500000)
	m.TenantID = tenant
	if err := s.Issue(ctx, m, "owner"); err != nil {
		t.Fatal(err)
	}
	a := mandate.NewAuthorizer(s, nil, at(base))
	for _, op := range []string{"said-no", "no-effect"} {
		if d, err := a.Authorize(ctx, tenant, op, refund(20000)); err != nil || !d.Allowed() {
			t.Fatal(d, err)
		}
	}
	rs := s.(mandate.EffectReleaseStore)
	r := refund(20000)
	if _, err := rs.ReleaseEffects(ctx, mandate.NoEffectEvidence{TenantID: tenant, ProposalID: "said-no", Subject: r.Subject, Action: r.Action,
		At: base.Add(time.Second), Source: "owner:did_this_happen", EvidenceID: "happened-said-no", Reason: "the owner said it didn't happen"}); err != nil {
		t.Fatal(err)
	}
	if _, err := rs.ReleaseEffects(ctx, effectEvidence(tenant, "no-effect", base.Add(time.Second))); err != nil {
		t.Fatal(err)
	}
	agg := m.Aggregates[0]
	if b, _ := mandate.ReadLimitBalance(ctx, s, tenant, m, agg, base.Add(2*time.Second), ""); b.Net != 0 {
		t.Fatalf("after both releases: %+v", b)
	}
	rr := s.(mandate.EffectReinstateStore)
	ev := func(prop string) mandate.ReinstateEvidence {
		return mandate.ReinstateEvidence{TenantID: tenant, ProposalID: prop, Undoes: "owner:did_this_happen",
			Source: "thune:refund_found", EvidenceID: "re_1", At: base.Add(3 * time.Second)}
	}
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := rr.ReinstateEffects(ctx, ev("said-no")); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if out, err := rr.ReinstateEffects(ctx, ev("no-effect")); err != nil || len(out) != 0 {
		t.Fatalf("a release of another source was taken back: %+v %v", out, err)
	}
	if b, err := mandate.ReadLimitBalance(ctx, s, tenant, m, agg, base.Add(4*time.Second), ""); err != nil || b.Net != 20000 {
		t.Fatalf("after the refund was found: %+v %v, want the said-no call counted once (20000)", b, err)
	}
	if _, err := rr.ReinstateEffects(ctx, mandate.ReinstateEvidence{TenantID: tenant, ProposalID: "said-no"}); err == nil {
		t.Fatal("incomplete evidence was accepted")
	}
}

func TestReinstateTakesBackTheOwnersNoOnce(t *testing.T) {
	testReinstates(t, mandate.NewMemoryStore(), tenantID)
}

func TestPostgres_ReinstateTakesBackTheOwnersNoOnce(t *testing.T) {
	p := requirePostgres(t)
	seedTenant(t, p, "effects-reinstated")
	testReinstates(t, mandate.NewPostgresStore(p), "effects-reinstated")
}
