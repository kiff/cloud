package mandate_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/mandate"
)

// A Card can be shared. Each test runs
// against the memory store and, when KIFF_TEST_POSTGRES_URL is set,
// against Postgres.

type sharedRig struct {
	store  mandate.Store
	tenant string
}

func bothStores(t *testing.T, fn func(t *testing.T, r sharedRig)) {
	t.Run("memory", func(t *testing.T) {
		s := mandate.NewMemoryStore()
		s.SetClock(at(base))
		fn(t, sharedRig{store: s, tenant: "t-shared"})
	})
	t.Run("postgres", func(t *testing.T) {
		pool := requirePostgres(t)
		tenant := fmt.Sprintf("t-shared-%d", time.Now().UnixNano())
		seedTenant(t, pool, tenant)
		fn(t, sharedRig{store: mandate.NewPostgresStore(pool), tenant: tenant})
	})
}

// sharedCard allows `limit` refunds a day to every holder together, up to
// 500 per refund, and holds what falls outside it for the owner.
func sharedCard(tenant, id string, limit int64, holders ...string) mandate.Mandate {
	m := pgMandate(tenant, id)
	m.Subject, m.Holders = holders[0], holders
	m.OnExceed = mandate.OnExceedOwner
	m.Grants[0].PerAction = []mandate.PerActionLimit{{Quantity: mandate.Quantity{Kind: mandate.KindSum, Parameter: "amount_cents"}, Max: 500}}
	m.Aggregates = []mandate.Aggregate{{Quantity: mandate.Quantity{Kind: mandate.KindCount}, Limit: limit, Window: mandate.WindowCalendarDay}}
	return m
}

func refundOf(agent string, amount int64) mandate.Request {
	r := refundBy(agent)
	r.Parameters = map[string]int64{"amount_cents": amount}
	return r
}

func (r sharedRig) issue(t *testing.T, m mandate.Mandate) {
	t.Helper()
	if err := r.store.Issue(context.Background(), m, "owner"); err != nil {
		t.Fatal(err)
	}
}

func (r sharedRig) decide(t *testing.T, proposal string, req mandate.Request) mandate.Decision {
	t.Helper()
	d, err := mandate.NewAuthorizer(r.store, nil, at(base)).Authorize(context.Background(), r.tenant, proposal, req)
	if err != nil {
		t.Fatalf("authorize %s: %v", proposal, err)
	}
	return d
}

// Every holder draws on the one total, and each draw names the agent.
func TestSharedCard_OneTotal(t *testing.T) {
	bothStores(t, func(t *testing.T, r sharedRig) {
		r.issue(t, sharedCard(r.tenant, "team", 3, "agent://a", "agent://b"))
		for i, agent := range []string{"agent://a", "agent://a", "agent://b"} {
			if d := r.decide(t, fmt.Sprintf("p-%d", i), refundOf(agent, 100)); d.Outcome != mandate.OutcomeWithinLimit {
				t.Fatalf("draw %d by %s: %s", i, agent, d.Outcome)
			}
		}
		if d := r.decide(t, "p-3", refundOf("agent://b", 100)); d.Outcome == mandate.OutcomeWithinLimit {
			t.Fatal("the fourth refund fit a total of three shared by two agents")
		}
		if d := r.decide(t, "p-4", refundOf("agent://c", 100)); d.Outcome != mandate.OutcomeNotCovered {
			t.Fatalf("an agent that does not hold the Card: %s", d.Outcome)
		}
		draws, err := r.store.Draws(context.Background(), r.tenant, "team", base.Add(-time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		by := map[string]int{}
		for _, d := range draws {
			by[d.Subject]++
		}
		if by["agent://a"] != 2 || by["agent://b"] != 1 || len(draws) != 3 {
			t.Fatalf("draws by agent = %v", by)
		}
	})
}

// An exception approved for one holder's action is that agent's: another
// holder cannot use it, and its own retry redeems it.
func TestSharedCard_ApprovalIsTheAgents(t *testing.T) {
	bothStores(t, func(t *testing.T, r sharedRig) {
		ctx := context.Background()
		r.issue(t, sharedCard(r.tenant, "team", 10, "agent://a", "agent://b"))
		held := r.decide(t, "p-big", refundOf("agent://a", 4700))
		if held.Outcome != mandate.OutcomeOwnerRequired {
			t.Fatalf("hold: %s", held.Outcome)
		}
		if _, err := r.store.ApproveOnce(ctx, r.tenant, held.ExceptionID, "owner", base); err != nil {
			t.Fatal(err)
		}
		if d := r.decide(t, "p-big", refundOf("agent://b", 4700)); d.Outcome == mandate.OutcomeWithinLimit {
			t.Fatal("another holder used an approval given to agent://a")
		}
		if d := r.decide(t, "p-big", refundOf("agent://a", 4700)); d.Outcome != mandate.OutcomeWithinLimit {
			t.Fatalf("agent://a's retry: %s", d.Outcome)
		}
	})
}

// Removing a holder, through SetHolders or a reissue, invalidates the
// exceptions held or approved for that agent and leaves the others'.
func TestSharedCard_RemovingAHolderCancelsOnlyTheirs(t *testing.T) {
	for _, how := range []string{"set holders", "reissue"} {
		t.Run(how, func(t *testing.T) {
			bothStores(t, func(t *testing.T, r sharedRig) {
				ctx := context.Background()
				card := sharedCard(r.tenant, "team", 10, "agent://a", "agent://b")
				r.issue(t, card)
				heldA := r.decide(t, "p-a", refundOf("agent://a", 4700))
				heldB := r.decide(t, "p-b", refundOf("agent://b", 4700))
				if heldA.Outcome != mandate.OutcomeOwnerRequired || heldB.Outcome != mandate.OutcomeOwnerRequired {
					t.Fatalf("holds: %s %s", heldA.Outcome, heldB.Outcome)
				}
				if _, err := r.store.ApproveOnce(ctx, r.tenant, heldA.ExceptionID, "owner", base); err != nil {
					t.Fatal(err)
				}
				if _, err := r.store.ApproveOnce(ctx, r.tenant, heldB.ExceptionID, "owner", base); err != nil {
					t.Fatal(err)
				}
				if how == "set holders" {
					if _, err := r.store.SetHolders(ctx, r.tenant, "team", []string{"agent://b"}, []string{"agent://a", "agent://b"}, "owner", base); err != nil {
						t.Fatal(err)
					}
				} else {
					card.Subject, card.Holders = "agent://b", []string{"agent://b"}
					r.issue(t, card)
				}
				a, _ := r.store.Exception(ctx, r.tenant, heldA.ExceptionID)
				b, _ := r.store.Exception(ctx, r.tenant, heldB.ExceptionID)
				if a.Status != mandate.ExceptionInvalidated || a.Reason != "holder_changed" {
					t.Errorf("agent://a's approval: %s %q", a.Status, a.Reason)
				}
				if b.Status != mandate.ExceptionApproved {
					t.Errorf("agent://b's approval: %s", b.Status)
				}
				if d := r.decide(t, "p-b", refundOf("agent://b", 4700)); d.Outcome != mandate.OutcomeWithinLimit {
					t.Errorf("agent://b redeems: %s", d.Outcome)
				}
				if d := r.decide(t, "p-a2", refundOf("agent://a", 100)); d.Outcome != mandate.OutcomeNotCovered {
					t.Errorf("agent://a after removal: %s", d.Outcome)
				}
			})
		})
	}
}

// An agent holding a shared Card and its own is bound by both: the
// tighter one decides, and its share of the team total needs nothing new.
func TestSharedCard_OwnCardAndSharedOneTighterBinds(t *testing.T) {
	bothStores(t, func(t *testing.T, r sharedRig) {
		r.issue(t, sharedCard(r.tenant, "team", 10, "agent://a", "agent://b"))
		own := sharedCard(r.tenant, "a-own", 1, "agent://a")
		own.OnExceed = mandate.OnExceedRefuse
		r.issue(t, own)
		if d := r.decide(t, "p-1", refundOf("agent://a", 100)); d.Outcome != mandate.OutcomeWithinLimit {
			t.Fatalf("first: %s", d.Outcome)
		}
		if d := r.decide(t, "p-2", refundOf("agent://a", 100)); d.Outcome == mandate.OutcomeWithinLimit {
			t.Fatal("agent://a went past its own Card because the shared one had room")
		}
		if d := r.decide(t, "p-3", refundOf("agent://b", 100)); d.Outcome != mandate.OutcomeWithinLimit {
			t.Fatalf("agent://b: %s", d.Outcome)
		}
	})
}

// Holders are recorded on every revision; SetHolders refuses a list that
// does not change, a bad list, and an inactive Card.
func TestSharedCard_SetHoldersRules(t *testing.T) {
	bothStores(t, func(t *testing.T, r sharedRig) {
		ctx := context.Background()
		r.issue(t, sharedCard(r.tenant, "team", 10, "agent://a"))
		m, err := r.store.SetHolders(ctx, r.tenant, "team", []string{"agent://a", "agent://b"}, []string{"agent://a"}, "owner", base)
		if err != nil || m.Subject != "agent://a" || len(m.HolderList()) != 2 {
			t.Fatalf("share: %+v %v", m, err)
		}
		if _, err := r.store.SetHolders(ctx, r.tenant, "team", []string{"agent://a", "agent://b"}, []string{"agent://a", "agent://b"}, "owner", base); !errors.Is(err, mandate.ErrMoveSameHolder) {
			t.Errorf("unchanged list: %v", err)
		}
		for _, bad := range [][]string{nil, {"agent://a", "agent://a"}, {""}, tooMany()} {
			if _, err := r.store.SetHolders(ctx, r.tenant, "team", bad, []string{"agent://a", "agent://b"}, "owner", base); !errors.Is(err, mandate.ErrInvalidMandate) {
				t.Errorf("%d holders %v: %v", len(bad), bad, err)
			}
		}
		if _, err := r.store.SetHolders(ctx, r.tenant, "nope", []string{"agent://b"}, nil, "owner", base); !errors.Is(err, mandate.ErrNotFound) {
			t.Errorf("missing card: %v", err)
		}
		revs, err := r.store.Revisions(ctx, r.tenant, "team")
		if err != nil || len(revs) != 2 || strings.Join(revs[1].Holders, ",") != "agent://a,agent://b" || strings.Join(revs[0].Holders, ",") != "agent://a" {
			t.Fatalf("revisions: %+v %v", revs, err)
		}
		cards, err := r.store.ForSubject(ctx, r.tenant, "agent://b")
		if err != nil || len(cards) != 1 || cards[0].ID != "team" {
			t.Fatalf("agent://b's Cards: %+v %v", cards, err)
		}
		if err := r.store.Revoke(ctx, r.tenant, "team", base.Add(-time.Minute)); err != nil {
			t.Fatal(err)
		}
		if _, err := r.store.SetHolders(ctx, r.tenant, "team", []string{"agent://c"}, nil, "owner", base); !errors.Is(err, mandate.ErrMoveInactive) {
			t.Errorf("revoked: %v", err)
		}
	})
}

func tooMany() []string {
	out := make([]string, mandate.MaxHolders+1)
	for i := range out {
		out[i] = fmt.Sprintf("agent://%d", i)
	}
	return out
}

func TestValidateHolders(t *testing.T) {
	m := sharedCard("t", "c", 1, "agent://a", "agent://b")
	if err := mandate.Validate(m, nil); err != nil && strings.Contains(err.Error(), "holder") {
		t.Fatalf("two holders: %v", err)
	}
	for name, hs := range map[string][]string{"duplicate": {"agent://a", "agent://a"}, "too many": tooMany(), "empty": {"agent://a", " "}} {
		m := sharedCard("t", "c", 1, hs...)
		if err := mandate.Validate(m, nil); !errors.Is(err, mandate.ErrInvalidMandate) {
			t.Errorf("%s: %v", name, err)
		}
	}
	m.Subject = "agent://b"
	if err := mandate.Validate(m, nil); err == nil || !strings.Contains(err.Error(), "first holder") {
		t.Errorf("subject not first: %v", err)
	}
}

// Postgres: two holders spending the last of a shared total at once
// serialise on the one Card lock, and exactly one draws.
func TestPostgres_SharedCardLastUnitOnce(t *testing.T) {
	pool := requirePostgres(t)
	seedTenant(t, pool, "t-last")
	store := mandate.NewPostgresStore(pool)
	card := sharedCard("t-last", "team", 1, "agent://a", "agent://b")
	card.OnExceed = mandate.OnExceedRefuse
	if err := store.Issue(context.Background(), card, "owner"); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	allowed := make(chan string, 20)
	for i := 0; i < 20; i++ {
		agent := []string{"agent://a", "agent://b"}[i%2]
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			d, err := mandate.NewAuthorizer(store, nil, at(base)).Authorize(context.Background(), "t-last", fmt.Sprintf("p-%d", i), refundOf(agent, 100))
			if err == nil && d.Outcome == mandate.OutcomeWithinLimit {
				allowed <- agent
			}
		}(i)
	}
	wg.Wait()
	close(allowed)
	if n := len(allowed); n != 1 {
		t.Fatalf("%d agents drew the last unit", n)
	}
	if n := drawCount(t, store, "t-last", "team"); n != 1 {
		t.Fatalf("draws = %d", n)
	}
}

// Postgres: a request that found the Card, then waited while its agent
// was removed from the holders, cannot draw when it resumes.
func TestPostgres_RemovedHolderCannotDrawAfterDiscovery(t *testing.T) {
	pool := requirePostgres(t)
	seedTenant(t, pool, "t-removed")
	store := mandate.NewPostgresStore(pool)
	ctx := context.Background()
	if err := store.Issue(ctx, sharedCard("t-removed", "team", 10, "agent://a", "agent://b"), "owner"); err != nil {
		t.Fatal(err)
	}
	a := mandate.NewAuthorizer(store, nil, at(base))
	var once sync.Once
	mandate.SetAfterDiscover(a, func() {
		once.Do(func() {
			if _, err := store.SetHolders(ctx, "t-removed", "team", []string{"agent://b"}, []string{"agent://a", "agent://b"}, "owner", base); err != nil {
				t.Errorf("remove: %v", err)
			}
		})
	})
	d, err := a.Authorize(ctx, "t-removed", "p-stale", refundOf("agent://a", 100))
	if err != nil {
		t.Fatalf("authorize: %v", err)
	}
	if d.Outcome == mandate.OutcomeWithinLimit {
		t.Fatalf("a removed holder drew: %+v", d)
	}
	if n := drawCount(t, store, "t-removed", "team"); n != 0 {
		t.Fatalf("draws = %d", n)
	}
}

// Postgres: Cards and draws from before Cards could be shared get their
// one holder when the schema is applied, and nothing is written twice.
func TestPostgres_SharedCardBackfill(t *testing.T) {
	pool := requirePostgres(t)
	seedTenant(t, pool, "t-old")
	store := mandate.NewPostgresStore(pool)
	ctx := context.Background()
	if err := store.Issue(ctx, sharedCard("t-old", "old", 10, "agent://old"), "owner"); err != nil {
		t.Fatal(err)
	}
	if d, err := mandate.NewAuthorizer(store, nil, at(base)).Authorize(ctx, "t-old", "p-old", refundOf("agent://old", 100)); err != nil || d.Outcome != mandate.OutcomeWithinLimit {
		t.Fatalf("draw: %+v %v", d, err)
	}
	// As it was before: no holder rows, and a draw that names no agent.
	if _, err := pool.Exec(ctx, `DELETE FROM cloud_meta.mandate_holders WHERE tenant_id = 't-old'`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE cloud_meta.mandate_draws SET subject = '' WHERE tenant_id = 't-old'`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := mandate.ApplySchema(ctx, pool); err != nil {
			t.Fatal(err)
		}
	}
	cards, err := store.ForSubject(ctx, "t-old", "agent://old")
	if err != nil || len(cards) != 1 || strings.Join(cards[0].HolderList(), ",") != "agent://old" || len(cards[0].Holders) != 1 {
		t.Fatalf("after backfill: %+v %v", cards, err)
	}
	draws, err := store.Draws(ctx, "t-old", "old", base.Add(-time.Hour))
	if err != nil || len(draws) != 1 || draws[0].Subject != "agent://old" {
		t.Fatalf("draw after backfill: %+v %v", draws, err)
	}
}

// A reissue that names only the subject (every form that changes a
// Card's limits) keeps a shared Card shared; naming another subject
// gives the Card to that agent alone, as a reissue always did.
func TestSharedCard_ReissueKeepsHolders(t *testing.T) {
	bothStores(t, func(t *testing.T, r sharedRig) {
		ctx := context.Background()
		card := sharedCard(r.tenant, "team", 10, "agent://a", "agent://b")
		r.issue(t, card)
		card.Holders = nil
		card.Aggregates[0].Limit = 20
		r.issue(t, card)
		cards, err := r.store.ForSubject(ctx, r.tenant, "agent://b")
		if err != nil || len(cards) != 1 || strings.Join(cards[0].HolderList(), ",") != "agent://a,agent://b" || cards[0].Aggregates[0].Limit != 20 {
			t.Fatalf("after a subject-only reissue: %+v %v", cards, err)
		}
		card.Subject = "agent://c"
		r.issue(t, card)
		if cards, _ := r.store.ForSubject(ctx, r.tenant, "agent://b"); len(cards) != 0 {
			t.Fatalf("agent://b still holds the Card after it was given to agent://c alone")
		}
	})
}

// Two owners change the holders from the same list at
// once (one stops sharing with agent://b, one shares with agent://c).
// Exactly one change lands; the other is refused as stale instead of
// silently undoing it. A stale list is refused on its own too.
func TestSharedCard_ConcurrentHolderChangesDoNotUndoEachOther(t *testing.T) {
	bothStores(t, func(t *testing.T, r sharedRig) {
		ctx := context.Background()
		r.issue(t, sharedCard(r.tenant, "team", 10, "agent://a", "agent://b"))
		read := []string{"agent://a", "agent://b"}
		changes := [][]string{{"agent://a"}, {"agent://a", "agent://b", "agent://c"}}
		errs := make(chan error, len(changes))
		var wg sync.WaitGroup
		for _, next := range changes {
			wg.Add(1)
			go func(next []string) {
				defer wg.Done()
				_, err := r.store.SetHolders(ctx, r.tenant, "team", next, read, "owner", base)
				errs <- err
			}(next)
		}
		wg.Wait()
		close(errs)
		var won, stale int
		for err := range errs {
			switch {
			case err == nil:
				won++
			case errors.Is(err, mandate.ErrHoldersChanged):
				stale++
			default:
				t.Fatalf("unexpected: %v", err)
			}
		}
		if won != 1 || stale != 1 {
			t.Fatalf("won %d, refused as stale %d; want one each", won, stale)
		}
		cards, _ := r.store.ForSubject(ctx, r.tenant, "agent://a")
		got := strings.Join(cards[0].HolderList(), ",")
		if got != "agent://a" && got != "agent://a,agent://b,agent://c" {
			t.Fatalf("holders = %s: a mix of both changes", got)
		}
		if _, err := r.store.SetHolders(ctx, r.tenant, "team", []string{"agent://d"}, read, "owner", base); !errors.Is(err, mandate.ErrHoldersChanged) {
			t.Fatalf("a change from a stale list: %v", err)
		}
	})
}
