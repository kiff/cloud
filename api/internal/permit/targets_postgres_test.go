package permit

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kiff/cloud/api/internal/testdb"
)

// Targets round-trip through Postgres with their governed arguments, and
// one stored before the resource argument existed reads with none.
func TestPostgres_TargetsKeepTheResourceArgument(t *testing.T) {
	url := testdb.URL(t)
	if url == "" {
		t.Skip("set " + testdb.EnvVar + " to run Postgres tests")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	// Applying twice is the boot path on a database that already has it.
	for i := 0; i < 2; i++ {
		if err := ApplySchema(ctx, pool); err != nil {
			t.Fatal(err)
		}
	}
	s := PostgresTargets{Pool: pool}
	at := time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)
	scoped := Target{Domain: "gateway", Action: "issue_refund", Tool: "refund", Audience: "a", AmountArg: "amount_eur", ResourceArg: "order"}
	plain := Target{Domain: "gateway", Action: "lookup", Tool: "lookup", Audience: "a"}
	if err := s.Replace(ctx, "t-rs", []Target{scoped, plain}, "owner", at); err != nil {
		t.Fatal(err)
	}
	if got, ok, err := s.Get(ctx, "t-rs", "gateway", "issue_refund"); err != nil || !ok || got != scoped {
		t.Fatalf("get: %+v %v %v", got, ok, err)
	}
	ts, err := s.List(ctx, "t-rs")
	if err != nil || len(ts) != 2 || ts[0] != scoped || ts[1] != plain {
		t.Fatalf("list: %+v %v", ts, err)
	}
}
