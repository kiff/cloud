package proofs

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kiff/cloud/api/internal/testdb"
)

func postgresStore(t *testing.T) (*PostgresStore, *pgxpool.Pool) {
	t.Helper()
	u := testdb.URL(t)
	if u == "" {
		t.Skip(testdb.EnvVar + " not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, u)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := ApplySchema(ctx, pool); err != nil {
		t.Fatalf("ApplySchema: %v", err)
	}
	if err := ApplySchema(ctx, pool); err != nil {
		t.Fatalf("ApplySchema twice: %v", err)
	}
	if _, err := pool.Exec(ctx, `TRUNCATE cloud_meta.proof_runs, cloud_meta.proof_overrides`); err != nil {
		t.Fatal(err)
	}
	return NewPostgresStore(pool), pool
}

func TestPostgresRunsRoundTripAndStayAppendOnly(t *testing.T) {
	store, _ := postgresStore(t)
	ctx := context.Background()
	r := goodRun()
	r.Counted, r.ReceivedAt = true, t0
	r.GitHubRunID, r.GitHubRunAttempt = "42146041790", "2"
	r.Checks[2].Why = "seen at the vendor"
	r.Rules = json.RawMessage(`{"judge_limits": "sha256:cd34",  "coach_words":"sha256:ab12"}`) // kept exactly, spacing and order too
	if err := store.Append(ctx, r); err != nil {
		t.Fatal(err)
	}
	again := r
	again.Outcome = OutcomeFail
	if err := store.Append(ctx, again); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("second Append = %v, want ErrDuplicate", err)
	}
	got, err := store.Run(ctx, r.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Outcome != OutcomePass || !got.Counted || len(got.Checks) != 3 || got.Checks[2].Why != "seen at the vendor" ||
		!got.StartedAt.Equal(r.StartedAt) || got.EventsKey != r.EventsKey || got.RunnerVersion != r.RunnerVersion ||
		got.Entry != r.Entry || string(got.Rules) != string(r.Rules) || got.GitHubRunID != r.GitHubRunID || got.GitHubRunAttempt != r.GitHubRunAttempt {
		t.Fatalf("round trip: %+v", got)
	}
	if _, err := store.Run(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing run: %v", err)
	}
	runs, err := store.RunsSince(ctx, t0.Add(-time.Hour))
	if err != nil || len(runs) != 1 {
		t.Fatalf("RunsSince = %d, %v", len(runs), err)
	}
}

func TestPostgresOverridesAndSweep(t *testing.T) {
	store, pool := postgresStore(t)
	ctx := context.Background()
	until := t0.Add(time.Hour)
	saved, err := store.AppendOverride(ctx, Override{App: "stripe", Action: OverrideRelease, Operator: "ops-1",
		Reason: "false alarm", OwnerApproval: "owner's yes", Until: &until})
	if err != nil || saved.ID == "" || saved.CreatedAt.IsZero() {
		t.Fatalf("AppendOverride = %+v, %v", saved, err)
	}
	list, err := store.Overrides(ctx)
	if err != nil || len(list) != 1 || list[0].OwnerApproval != "owner's yes" || !list[0].Until.Equal(until) {
		t.Fatalf("Overrides = %+v, %v", list, err)
	}

	old := goodRun()
	old.RunID = "01JPROOFRUNOLD000000000001"
	if err := store.Append(ctx, old); err != nil {
		t.Fatal(err)
	}
	fresh := goodRun()
	if err := store.Append(ctx, fresh); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE cloud_meta.proof_runs SET received_at = NOW() - INTERVAL '61 days' WHERE run_id = $1`, old.RunID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE cloud_meta.proof_overrides SET created_at = NOW() - INTERVAL '61 days'`); err != nil {
		t.Fatal(err)
	}
	n, err := store.Sweep(ctx, time.Now().Add(-Retention))
	if err != nil || n != 2 {
		t.Fatalf("Sweep = %d, %v; want 2", n, err)
	}
	if _, err := store.Run(ctx, old.RunID); !errors.Is(err, ErrNotFound) {
		t.Fatal("the old run survived the sweep")
	}
	if _, err := store.Run(ctx, fresh.RunID); err != nil {
		t.Fatal("the sweep took a fresh run")
	}
}
