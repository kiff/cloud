package mandate_test

import (
	"context"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/mandate"
)

// A trial's queue against Postgres: each write counts the trial's earlier
// allowed ones, another trial run doesn't see them, and nothing real is
// drawn or held.
func TestPostgres_TrialCountsItsOwnWrites(t *testing.T) {
	pool := requirePostgres(t)
	seedTenant(t, pool, "t-trial")
	store := mandate.NewPostgresStore(pool)
	ctx := context.Background()
	if err := store.Issue(ctx, pgHomeCard("t-trial", "card-1", 50000, 100000), "owner"); err != nil {
		t.Fatal(err)
	}
	a := mandate.NewAuthorizer(store, nil, at(base))
	try := func(run string, amount int64) mandate.Outcome {
		t.Helper()
		d, err := a.Trial(ctx, "t-trial", run, pgRefund(amount))
		if err != nil {
			t.Fatal(err)
		}
		return d.Outcome
	}
	for i, want := range []mandate.Outcome{mandate.OutcomeWithinLimit, mandate.OutcomeWithinLimit, mandate.OutcomeOwnerRequired} {
		if got := try("run-1", 40000); got != want {
			t.Fatalf("refund %d = %s, want %s", i+1, got, want)
		}
	}
	if got := try("run-2", 40000); got != mandate.OutcomeWithinLimit {
		t.Fatalf("another trial run = %s", got)
	}
	if n, err := store.Used(ctx, "t-trial", "card-1", "sum(amount_cents)", base.Add(-48*time.Hour), ""); err != nil || n != 0 {
		t.Fatalf("real usage after trials = %d, %v", n, err)
	}
	if es, _ := store.Exceptions(ctx, "t-trial", ""); len(es) != 0 {
		t.Fatalf("a trial held an exception: %+v", es)
	}
}
