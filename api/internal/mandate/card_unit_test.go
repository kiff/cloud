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

// cardUnitStore is what the ask needs of a store, both of them.
type cardUnitStore interface {
	AskCardUnit(ctx context.Context, a mandate.CardUnitAsk, now time.Time) (mandate.Exception, bool, error)
	CloseCardUnit(ctx context.Context, a mandate.CardUnitAsk, now time.Time) error
	Exception(ctx context.Context, tenantID, id string) (mandate.Exception, error)
	ApproveOnce(ctx context.Context, tenantID, id, by string, now time.Time) (mandate.Exception, error)
	ChangeCard(ctx context.Context, tenantID, id, by string, now time.Time) (mandate.Exception, error)
	Reject(ctx context.Context, tenantID, id, by string, now time.Time, r mandate.Refusal) (mandate.Exception, error)
}

func eachCardUnitStore(t *testing.T, fn func(t *testing.T, s cardUnitStore, tenant string)) {
	t.Run("memory", func(t *testing.T) { fn(t, mandate.NewMemoryStore(), "t-unit") })
	t.Run("postgres", func(t *testing.T) {
		pool := requirePostgres(t)
		seedTenant(t, pool, "t-unit")
		fn(t, mandate.NewPostgresStore(pool), "t-unit")
	})
}

// A call refused because its Card has no limit in its unit opens one ask
// per agent, action and unit: each refused repeat counts on it, another
// unit or action is its own, and it can't be approved once (an older app
// that reads it as a hold gets 409). A call in its unit closes it as
// changed; changing the Card closes it too; dismissing it lets the next
// refusal open a new one (kiff/tafs#464 item 11).
func TestACardUnitAskIsOneAskCountedUntilTheCardCountsTheUnit(t *testing.T) {
	eachCardUnitStore(t, func(t *testing.T, s cardUnitStore, tenant string) {
		ctx := context.Background()
		ask := func(proposal, action, unit string) mandate.CardUnitAsk {
			return mandate.CardUnitAsk{TenantID: tenant, ProposalID: proposal, Subject: "chargebacks", Action: action,
				EntityID: "dp_1", Unit: unit, CardUnit: "EUR"}
		}
		first, opened, err := s.AskCardUnit(ctx, ask("tc-1", "answer_dispute", "usd"), base)
		if err != nil || !opened {
			t.Fatalf("first: %v %v", opened, err)
		}
		if first.KindOf() != mandate.KindCardUnit || first.Status != mandate.ExceptionHeld || first.Count != 1 ||
			len(first.Breaches) != 1 || first.Breaches[0].Unit != "USD" || first.Breaches[0].CardUnit != "EUR" || first.CanApproveOnce() {
			t.Fatalf("first: %+v", first)
		}
		for i, p := range []string{"tc-2", "tc-3", "tc-4"} {
			e, opened, err := s.AskCardUnit(ctx, ask(p, "answer_dispute", "USD"), base.Add(time.Duration(i+1)*time.Hour))
			if err != nil || opened || e.ID != first.ID {
				t.Fatalf("repeat %s: %v %v %s", p, opened, err, e.ID)
			}
		}
		got, err := s.Exception(ctx, tenant, first.ID)
		if err != nil || got.Count != 4 || got.LastAt == nil || !got.LastAt.Equal(base.Add(3*time.Hour)) || got.ProposalID != "tc-1" {
			t.Fatalf("counted: %+v %v", got, err)
		}
		// Another unit, or another action, is its own ask.
		if _, opened, _ := s.AskCardUnit(ctx, ask("tc-5", "answer_dispute", "GBP"), base); !opened {
			t.Fatal("another unit was counted on the USD ask")
		}
		other, opened, _ := s.AskCardUnit(ctx, ask("tc-6", "accept_dispute", "USD"), base)
		if !opened {
			t.Fatal("another action was counted on answer_dispute's ask")
		}

		// It can't be approved once.
		if _, err := s.ApproveOnce(ctx, tenant, first.ID, "owner", base); !errors.Is(err, mandate.ErrCumulativeApproval) {
			t.Fatalf("approve once: %v", err)
		}
		// A call in its unit closes it, as changed; the other action's stays.
		if err := s.CloseCardUnit(ctx, ask("tc-7", "answer_dispute", "usd"), base.Add(4*time.Hour)); err != nil {
			t.Fatal(err)
		}
		if got, _ := s.Exception(ctx, tenant, first.ID); got.Status != mandate.ExceptionChanged || got.Reason != mandate.ReasonUnitAllowed || got.ResolvedAt == nil {
			t.Fatalf("closed: %+v", got)
		}
		if got, _ := s.Exception(ctx, tenant, other.ID); got.Status != mandate.ExceptionHeld {
			t.Fatalf("the other action's: %s", got.Status)
		}
		// The next refusal after it closed opens a new ask.
		again, opened, err := s.AskCardUnit(ctx, ask("tc-8", "answer_dispute", "USD"), base.Add(5*time.Hour))
		if err != nil || !opened || again.ID == first.ID || again.Count != 1 {
			t.Fatalf("after closing: %v %v %+v", opened, err, again)
		}
		// Changing the Card answers it, as changed.
		if got, err := s.ChangeCard(ctx, tenant, again.ID, "owner", base.Add(6*time.Hour)); err != nil || got.Status != mandate.ExceptionChanged {
			t.Fatalf("change card: %+v %v", got, err)
		}
		// Dismissed, the next refusal opens a new one.
		if got, err := s.Reject(ctx, tenant, other.ID, "owner", base.Add(6*time.Hour), mandate.Refusal{}); err != nil || got.Status != mandate.ExceptionRejected {
			t.Fatalf("reject: %+v %v", got, err)
		}
		if _, opened, _ := s.AskCardUnit(ctx, ask("tc-9", "accept_dispute", "USD"), base.Add(7*time.Hour)); !opened {
			t.Fatal("a refusal after dismissing was counted on the dismissed ask")
		}
	})
}

// Refusals at once for the same agent, action and unit open one ask, and
// each counts on it. One burst meets the race only now and then, so it
// runs rounds of them, each on its own action.
func TestCardUnitRefusalsAtOnceOpenOneAsk(t *testing.T) {
	eachCardUnitStore(t, func(t *testing.T, s cardUnitStore, tenant string) {
		ctx := context.Background()
		const rounds, n = 20, 8
		type result struct {
			id     string
			opened bool
			err    error
		}
		for round := range rounds {
			action := fmt.Sprintf("answer_at_once_%d_%d", time.Now().UnixNano(), round)
			results := make(chan result, n)
			var start sync.WaitGroup
			start.Add(1)
			for i := range n {
				go func() {
					start.Wait()
					e, opened, err := s.AskCardUnit(ctx, mandate.CardUnitAsk{TenantID: tenant, ProposalID: fmt.Sprintf("tc-at-once-%d-%d", round, i),
						Subject: "chargebacks", Action: action, EntityID: "dp_1", Unit: "USD", CardUnit: "EUR"}, base)
					results <- result{e.ID, opened, err}
				}()
			}
			start.Done()
			ids, opened := map[string]bool{}, 0
			for range n {
				r := <-results
				if r.err != nil {
					t.Fatal(r.err)
				}
				ids[r.id] = true
				if r.opened {
					opened++
				}
			}
			if len(ids) != 1 || opened != 1 {
				t.Fatalf("round %d: %d asks, %d opened; want one", round, len(ids), opened)
			}
			for id := range ids {
				if got, err := s.Exception(ctx, tenant, id); err != nil || got.Count != n || got.Status != mandate.ExceptionHeld {
					t.Fatalf("round %d, the ask: %+v %v", round, got, err)
				}
			}
		}
	})
}
