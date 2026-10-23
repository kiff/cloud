package mandate_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/mandate"
)

// IssueAll (kiff/tafs#107) revises an existing Card and adds a new one in
// one transaction: the revised Card keeps its id and what it used, and
// gets a revision; Cards of two tenants are never issued together.
func TestPostgres_IssueAllRevisesAndAdds(t *testing.T) {
	pool := requirePostgres(t)
	seedTenant(t, pool, "t-issue-all")
	store := mandate.NewPostgresStore(pool)
	ctx := context.Background()
	if err := store.Issue(ctx, pgHomeCard("t-issue-all", "card-1", 50000, 100000), "owner"); err != nil {
		t.Fatal(err)
	}
	a := mandate.NewAuthorizer(store, nil, at(base))
	if d, err := a.Authorize(ctx, "t-issue-all", "p-1", pgRefund(40000)); err != nil || !d.Allowed() {
		t.Fatalf("draw: %+v %v", d, err)
	}
	revised := pgHomeCard("t-issue-all", "card-1", 90000, 100000)
	added := pgHomeCard("t-issue-all", "card-2", 1000, 5000)
	if err := store.IssueAll(ctx, []mandate.Mandate{revised, added}, "owner", nil, nil, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if revs, _ := store.Revisions(ctx, "t-issue-all", "card-1"); len(revs) != 2 {
		t.Fatalf("card-1 revisions = %d", len(revs))
	}
	if revs, _ := store.Revisions(ctx, "t-issue-all", "card-2"); len(revs) != 1 {
		t.Fatalf("card-2 revisions = %d", len(revs))
	}
	if n, _ := store.Used(ctx, "t-issue-all", "card-1", "sum(amount_cents)", base.AddDate(0, 0, -2), ""); n != 40000 {
		t.Fatalf("card-1 used after its revision = %d", n)
	}
	// Only over the revision it was made from: card-1 is at 2 now, so a
	// change made from revision 1 is refused, with nothing issued.
	stale := pgHomeCard("t-issue-all", "card-1", 1, 100000)
	if err := store.IssueAll(ctx, []mandate.Mandate{stale, pgHomeCard("t-issue-all", "card-5", 1, 1)}, "owner", map[string]int64{"card-1": 1}, nil, time.Time{}); !errors.Is(err, mandate.ErrCardRevised) {
		t.Fatalf("a change over a moved Card: %v", err)
	}
	if revs, _ := store.Revisions(ctx, "t-issue-all", "card-5"); len(revs) != 0 {
		t.Fatal("a refused IssueAll issued a Card")
	}
	if err := store.IssueAll(ctx, []mandate.Mandate{stale}, "owner", map[string]int64{"card-1": 2}, nil, time.Time{}); err != nil {
		t.Fatalf("a change over the revision it was made from: %v", err)
	}
	other := pgHomeCard("t-other", "card-3", 1, 1)
	if err := store.IssueAll(ctx, []mandate.Mandate{pgHomeCard("t-issue-all", "card-4", 1, 1), other}, "owner", nil, nil, time.Time{}); err == nil {
		t.Fatal("Cards of two tenants issued together")
	}
	if revs, _ := store.Revisions(ctx, "t-issue-all", "card-4"); len(revs) != 0 {
		t.Fatal("a refused IssueAll issued a Card")
	}
}

// IssueAll retires Cards in the same transaction (kiff/tafs#107): revoked
// from at with the issued ones, or, for one that doesn't exist, nothing.
func TestPostgres_IssueAllRetires(t *testing.T) {
	pool := requirePostgres(t)
	seedTenant(t, pool, "t-issue-retire")
	store := mandate.NewPostgresStore(pool)
	ctx := context.Background()
	for _, id := range []string{"card-1", "card-2"} {
		if err := store.Issue(ctx, pgHomeCard("t-issue-retire", id, 50000, 100000), "owner"); err != nil {
			t.Fatal(err)
		}
	}
	at := base.Add(time.Hour)
	if err := store.IssueAll(ctx, []mandate.Mandate{pgHomeCard("t-issue-retire", "card-1", 40000, 100000)}, "owner", nil, []string{"card-2", "card-x"}, at); !errors.Is(err, mandate.ErrNotFound) {
		t.Fatalf("retiring a Card that doesn't exist: %v", err)
	}
	if revs, _ := store.Revisions(ctx, "t-issue-retire", "card-1"); len(revs) != 1 {
		t.Fatal("a refused IssueAll issued a Card")
	}
	if err := store.IssueAll(ctx, []mandate.Mandate{pgHomeCard("t-issue-retire", "card-1", 40000, 100000)}, "owner", map[string]int64{"card-2": 1}, []string{"card-2"}, at); err != nil {
		t.Fatal(err)
	}
	ms, err := store.List(ctx, "t-issue-retire")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, m := range ms {
		if m.ID == "card-2" {
			found = true
			if m.RevokedAt == nil || !m.RevokedAt.Equal(at) {
				t.Fatalf("card-2 after being retired: revoked %v", m.RevokedAt)
			}
		}
	}
	if revs, _ := store.Revisions(ctx, "t-issue-retire", "card-1"); !found || len(revs) != 2 {
		t.Fatalf("card-2 listed %v, card-1 revisions %d", found, len(revs))
	}
}
