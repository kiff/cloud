package mandate_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/mandate"
)

// Per-action limits live on grants, stored as JSONB: they round-trip,
// and a Card issued before they existed still loads with none.
func TestPostgres_PerActionLimitsRoundTrip(t *testing.T) {
	pool := requirePostgres(t)
	seedTenant(t, pool, "t-pa")
	store := mandate.NewPostgresStore(pool)
	ctx := context.Background()

	m := pgMandate("t-pa", "card-pa")
	m.Grants[0].PerAction = []mandate.PerActionLimit{{Quantity: mandate.Quantity{Kind: mandate.KindSum, Parameter: "amount_cents"}, Max: 50000}}
	if err := store.Issue(ctx, m, "owner"); err != nil {
		t.Fatal(err)
	}
	if err := store.Issue(ctx, pgMandate("t-pa", "card-old"), "owner"); err != nil {
		t.Fatal(err)
	}
	ms, err := store.ForSubject(ctx, "t-pa", "agent://a")
	if err != nil || len(ms) != 2 {
		t.Fatalf("load: %d %v", len(ms), err)
	}
	for _, got := range ms {
		switch got.ID {
		case "card-pa":
			if len(got.Grants[0].PerAction) != 1 || got.Grants[0].PerAction[0].Max != 50000 ||
				got.Grants[0].PerAction[0].Quantity.Parameter != "amount_cents" {
				t.Fatalf("per-action limit did not round-trip: %+v", got.Grants[0])
			}
		case "card-old":
			if len(got.Grants[0].PerAction) != 0 {
				t.Fatalf("a Card without per-action limits grew one: %+v", got.Grants[0])
			}
		}
	}

	a := mandate.NewAuthorizer(store, nil, at(base))
	r := mandate.Request{Subject: "agent://a", Domain: "billing", Environment: "prod",
		Action: "REFUND_INVOICE", EntityState: "PAID", Parameters: map[string]int64{"amount_cents": 150000}}
	d, err := a.Authorize(ctx, "t-pa", "p-big", r)
	if err != nil || d.Outcome != mandate.OutcomePerActionExceeded {
		t.Fatalf("authorize: %+v %v", d, err)
	}
	if n, _ := store.Used(ctx, "t-pa", "card-pa", "sum(amount_cents)", base.Add(-48*time.Hour), ""); n != 0 {
		t.Fatalf("a refused action drew %d", n)
	}
}

// Overlapping Card sets locked through the production
// transaction path in opposite orders do not deadlock (sorted
// pg_advisory_xact_lock ids).
func TestPostgres_WithCardsOppositeOrdersDoNotDeadlock(t *testing.T) {
	requirePostgres(t)
	pool := poolWith(t, 2)
	seedTenant(t, pool, "t-order")
	store := mandate.NewPostgresStore(pool)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	for _, ids := range [][]string{{"c-a", "c-b", "c-c"}, {"c-c", "c-b", "c-a"}} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				if err := store.WithCards(ctx, "t-order", ids, func(mandate.CardTx) error { return nil }); err != nil {
					t.Errorf("with cards: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()
}
