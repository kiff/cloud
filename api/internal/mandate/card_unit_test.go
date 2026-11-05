package mandate_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

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
	IssueAll(ctx context.Context, ms []mandate.Mandate, by string, expect map[string]int64, retire []string, at time.Time) error
	IssueAllCovering(ctx context.Context, ms []mandate.Mandate, by string, expect map[string]int64, retire []string, at time.Time, covers []mandate.UnitCover) error
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
// changed (another action's allowed call doesn't); changing the Card closes it too; dismissing it lets the next
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
		// An allowed call on another action closes nothing: it says nothing
		// about this one. A call in its unit on its own action closes it, as
		// changed, naming the action; the other action's stays.
		if err := s.CloseCardUnit(ctx, ask("tc-7a", "answer_dispute_usd", "usd"), base.Add(4*time.Hour)); err != nil {
			t.Fatal(err)
		}
		if got, _ := s.Exception(ctx, tenant, first.ID); got.Status != mandate.ExceptionHeld {
			t.Fatalf("another action's allowed call closed it: %s", got.Status)
		}
		if err := s.CloseCardUnit(ctx, ask("tc-7", "answer_dispute", "usd"), base.Add(4*time.Hour)); err != nil {
			t.Fatal(err)
		}
		if got, _ := s.Exception(ctx, tenant, first.ID); got.Status != mandate.ExceptionChanged || got.ResolvedAt == nil ||
			got.Reason != "a call in USD was allowed on answer_dispute: its Cards count USD now" {
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

// A signed Card that gives the agent an action in the ask's unit closes the
// agent's asks in that unit as changed, in the same transaction, naming the
// action and who signed. Another agent's, or another unit's, stays; Cards
// issued with no covers close nothing.
func TestASignedCardInTheUnitClosesTheAgentsAsks(t *testing.T) {
	eachCardUnitStore(t, func(t *testing.T, s cardUnitStore, tenant string) {
		ctx := context.Background()
		ask := func(proposal, subject, unit string) mandate.Exception {
			e, opened, err := s.AskCardUnit(ctx, mandate.CardUnitAsk{TenantID: tenant, ProposalID: proposal, Subject: subject,
				Action: "refund_payment", EntityID: "pi_1", Unit: unit, CardUnit: "EUR"}, base)
			if err != nil || !opened {
				t.Fatalf("ask %s: %v %v", proposal, opened, err)
			}
			return e
		}
		usd, gbp, theirs := ask("tc-cover-1", "chargebacks-cover", "USD"), ask("tc-cover-2", "chargebacks-cover", "GBP"), ask("tc-cover-3", "refunds-cover", "USD")
		card := mandate.Mandate{ID: "card-cover-usd", TenantID: tenant, Subject: "chargebacks-cover",
			Grants:     []mandate.Grant{{Action: "refund_payment_usd"}},
			Aggregates: []mandate.Aggregate{{Quantity: mandate.Quantity{Kind: mandate.KindSum, Parameter: "amount"}, Limit: 1000, Window: mandate.WindowCalendarDay}}}

		// Issued with no covers (a trial, a setup read): nothing closes.
		if err := s.IssueAll(ctx, []mandate.Mandate{card}, "owner@example.com", nil, nil, base.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
		if got, _ := s.Exception(ctx, tenant, usd.ID); got.Status != mandate.ExceptionHeld {
			t.Fatalf("closed without covers: %s", got.Status)
		}

		covers := []mandate.UnitCover{{Subject: "chargebacks-cover", Action: "refund_payment_usd", Unit: "usd"}}
		if err := s.IssueAllCovering(ctx, []mandate.Mandate{card}, "owner@example.com", nil, nil, base.Add(2*time.Hour), covers); err != nil {
			t.Fatal(err)
		}
		got, _ := s.Exception(ctx, tenant, usd.ID)
		if got.Status != mandate.ExceptionChanged || got.ResolvedBy != "owner@example.com" || got.ResolvedAt == nil ||
			!got.ResolvedAt.Equal(base.Add(2*time.Hour)) || got.Reason != "a signed Card gives it refund_payment_usd in USD now" {
			t.Fatalf("the USD ask: %+v", got)
		}
		for _, e := range []mandate.Exception{gbp, theirs} {
			if got, _ := s.Exception(ctx, tenant, e.ID); got.Status != mandate.ExceptionHeld {
				t.Fatalf("%s (%s) closed: %s", e.ID, e.Subject, got.Status)
			}
		}
	})
}

// One missing limit is one ask, however a run alternates: a refused USD
// call on one action and an allowed USD call on another, five times, open
// a single ask counted five times, which alerts the owner once.
func TestAnAllowedCallOnAnotherActionKeepsTheAsk(t *testing.T) {
	eachCardUnitStore(t, func(t *testing.T, s cardUnitStore, tenant string) {
		ctx := context.Background()
		opened := 0
		for i := range 5 {
			at := base.Add(time.Duration(i) * time.Minute)
			_, o, err := s.AskCardUnit(ctx, mandate.CardUnitAsk{TenantID: tenant, ProposalID: fmt.Sprintf("tc-alt-r-%d", i), Subject: "chargebacks-alt",
				Action: "refund", EntityID: "pi_1", Unit: "USD", CardUnit: "EUR"}, at)
			if err != nil {
				t.Fatal(err)
			}
			if o {
				opened++
			}
			if err := s.CloseCardUnit(ctx, mandate.CardUnitAsk{TenantID: tenant, ProposalID: fmt.Sprintf("tc-alt-u-%d", i), Subject: "chargebacks-alt",
				Action: "update_dispute", EntityID: "dp_1", Unit: "USD"}, at.Add(time.Second)); err != nil {
				t.Fatal(err)
			}
		}
		if opened != 1 {
			t.Fatalf("5 refused USD refunds, each followed by an allowed USD update_dispute: %d asks opened, want 1", opened)
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

// Postgres holds one held card_unit exception per agent, action and unit
// itself: a second can't be written, and one written before the index
// existed is folded into the earliest when the schema is applied.
func TestPostgresHoldsOneCardUnitAsk(t *testing.T) {
	pool := requirePostgres(t)
	seedTenant(t, pool, "t-unit-one")
	ctx := context.Background()
	s := mandate.NewPostgresStore(pool)
	first, _, err := s.AskCardUnit(ctx, mandate.CardUnitAsk{TenantID: "t-unit-one", ProposalID: "tc-1", Subject: "chargebacks",
		Action: "answer_dispute", EntityID: "dp_1", Unit: "USD", CardUnit: "EUR"}, base)
	if err != nil {
		t.Fatal(err)
	}
	// copyAs writes a copy of first, as another held exception made later.
	const copyAs = `INSERT INTO cloud_meta.mandate_exceptions
		SELECT (jsonb_populate_record(NULL::cloud_meta.mandate_exceptions,
			to_jsonb(e) || jsonb_build_object('id', $3::text, 'proposal_id', $3::text, 'ask_count', 2, 'created_at', e.created_at + interval '1 hour'))).*
		  FROM cloud_meta.mandate_exceptions e WHERE e.tenant_id = $1 AND e.id = $2`
	var pgErr *pgconn.PgError
	if _, err := pool.Exec(ctx, copyAs, "t-unit-one", first.ID, "exc-second"); !errors.As(err, &pgErr) || pgErr.Code != "23505" {
		t.Fatalf("a second held card_unit exception: %v, want a unique violation", err)
	}

	// Before the index: a duplicate, folded when the schema is applied. In a
	// transaction, rolled back, so other tests never see the index missing.
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `DROP INDEX cloud_meta.mandate_exceptions_card_unit_held_one`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, copyAs, "t-unit-one", first.ID, "exc-second"); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, mandate.SchemaSQL()); err != nil {
		t.Fatal(err)
	}
	var keptStatus, secondStatus, secondReason string
	var keptCount int
	if err := tx.QueryRow(ctx, `SELECT status, ask_count FROM cloud_meta.mandate_exceptions WHERE tenant_id = $1 AND id = $2`,
		"t-unit-one", first.ID).Scan(&keptStatus, &keptCount); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRow(ctx, `SELECT status, reason FROM cloud_meta.mandate_exceptions WHERE tenant_id = $1 AND id = 'exc-second'`,
		"t-unit-one").Scan(&secondStatus, &secondReason); err != nil {
		t.Fatal(err)
	}
	if keptStatus != "held" || keptCount != 3 || secondStatus != "invalidated" || secondReason == "" {
		t.Fatalf("folded: kept %s %d, second %s %q", keptStatus, keptCount, secondStatus, secondReason)
	}
}

// A held card_unit ask names no Card, so it reads as held: a list of held
// exceptions shows it, and no Card check withdraws it (kiff/tafs#464
// item 11, from the v25 phone check).
func TestACardUnitAskReadsAsHeld(t *testing.T) {
	eachCardUnitStore(t, func(t *testing.T, s cardUnitStore, tenant string) {
		ctx := context.Background()
		e, _, err := s.AskCardUnit(ctx, mandate.CardUnitAsk{TenantID: tenant, ProposalID: fmt.Sprintf("tc-read-%d", time.Now().UnixNano()),
			Subject: "chargebacks", Action: fmt.Sprintf("answer_read_%d", time.Now().UnixNano()), EntityID: "dp_1", Unit: "USD", CardUnit: "EUR"}, base)
		if err != nil {
			t.Fatal(err)
		}
		p, err := mandate.ReadException(ctx, s.(mandate.Store), e, base.Add(time.Hour))
		if err != nil || p.Status != mandate.ExceptionHeld || p.Exception.Status != mandate.ExceptionHeld {
			t.Fatalf("read: %s / %s (%q) %v", p.Status, p.Exception.Status, p.Exception.Reason, err)
		}
	})
}

// The same proposal reaching the authorizer while its
// card_unit ask is held (the decide path refuses a retry in the same unit
// before the authorizer; one that gets past that check is this case) is
// refused as a reused proposal, whatever it asks, and the ask stays held
// as it was: never invalidated, never replaced by another kind of hold.
func TestTheAuthorizerLeavesAHeldCardUnitAskStanding(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		name   string
		amount int64
	}{{"within the Card", 2500}, {"over the Card", 20000}} {
		t.Run(c.name, func(t *testing.T) {
			s := seeded(t, spendMandate(10000))
			ask, opened, err := s.AskCardUnit(ctx, mandate.CardUnitAsk{TenantID: "t1", ProposalID: "tc-1", Subject: "agent://refund-agent",
				Action: "REFUND_INVOICE", EntityID: "in_1", Unit: "usd", CardUnit: "EUR"}, base)
			if err != nil || !opened {
				t.Fatalf("ask: %v %v", opened, err)
			}
			a := mandate.NewAuthorizer(s, nil, at(base.Add(time.Minute)))
			d, err := a.Authorize(ctx, "t1", "tc-1", req("agent://refund-agent", "REFUND_INVOICE", "PAID", map[string]int64{"amount_cents": c.amount}))
			if err != nil {
				t.Fatal(err)
			}
			got, err := s.Exception(ctx, "t1", ask.ID)
			if err != nil || d.Allowed() || d.Outcome != mandate.OutcomeProposalReused || got.KindOf() != mandate.KindCardUnit ||
				got.Status != mandate.ExceptionHeld || got.Count != 1 || got.Reason != "" {
				t.Fatalf("decision %s; ask %s %s count %d (%s) %v", d.Outcome, got.KindOf(), got.Status, got.Count, got.Reason, err)
			}
			draws, err := s.ProposalDraws(ctx, "t1", "tc-1")
			if err != nil || len(draws) != 0 {
				t.Fatalf("draws: %v %v", draws, err)
			}
		})
	}
}
