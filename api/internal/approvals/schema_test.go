package approvals_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/kiff/cloud/api/internal/approvals"
)

// The migration must converge, not just succeed once: it runs against
// every tenant schema and an operator will rerun it after a partial
// failure. Every statement is IF NOT EXISTS, so a second pass is a
// no-op rather than an error.
func TestApplySchema_IsIdempotent(t *testing.T) {
	t.Parallel()
	pool := newSchema(t) // already applied once
	ctx := context.Background()

	for i := 0; i < 2; i++ {
		mustTx(t, pool, func(tx pgx.Tx) error { return approvals.ApplySchema(ctx, tx) })
	}

	// One column, not several: a rerun must not have duplicated it.
	// Scoped to this test's schema — information_schema spans the whole
	// database, and the parallel tests each have their own copy.
	var n int
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM information_schema.columns
		WHERE table_schema = current_schema()
		  AND table_name = 'kiff_approvals'
		  AND column_name = 'domain_id'
	`).Scan(&n); err != nil {
		t.Fatalf("inspect columns: %v", err)
	}
	if n != 1 {
		t.Fatalf("domain_id column count = %d, want 1", n)
	}
}

// Timeouts are transaction-scoped so a pooled connection cannot carry
// them back into application queries.
func TestSetMigrationTimeouts_AreTransactionLocal(t *testing.T) {
	t.Parallel()
	pool := newSchema(t)
	ctx := context.Background()

	mustTx(t, pool, func(tx pgx.Tx) error {
		if err := approvals.SetMigrationTimeouts(ctx, tx); err != nil {
			return err
		}
		var lock string
		if err := tx.QueryRow(ctx, "SHOW lock_timeout").Scan(&lock); err != nil {
			return err
		}
		if lock == "0" {
			t.Error("lock_timeout not applied inside the transaction")
		}
		return nil
	})

	// After commit, the pooled connection must be back to the server
	// default rather than carrying the migration's short timeout.
	var after string
	if err := pool.QueryRow(ctx, "SHOW lock_timeout").Scan(&after); err != nil {
		t.Fatalf("show after commit: %v", err)
	}
	if after != "0" {
		t.Fatalf("lock_timeout leaked out of the transaction: %q", after)
	}
}

// Explicit attribution stamps every unattributed row and leaves
// already-attributed rows alone. It is only ever reached when an
// operator named the domain; nothing here infers it.
func TestAttributeLegacy(t *testing.T) {
	t.Parallel()
	pool := newSchema(t)
	ctx := context.Background()

	// Two legacy rows (no domain) and one already owned by domainB.
	if _, err := pool.Exec(ctx, `
		INSERT INTO kiff_approvals (id, entity_id, entity_type, action_name,
		                            requested_by, status, created_at, domain_id)
		VALUES ('l1','o1','Order','REFUND','x','pending',NOW(),''),
		       ('l2','o2','Order','REFUND','x','granted',NOW(),''),
		       ('b1','o3','Order','SHIP','x','pending',NOW(),$1)
	`, domainB); err != nil {
		t.Fatalf("seed: %v", err)
	}

	var n int64
	mustTx(t, pool, func(tx pgx.Tx) error {
		var err error
		n, err = approvals.AttributeLegacy(ctx, tx, domainA)
		return err
	})
	if n != 2 {
		t.Fatalf("attributed %d rows, want 2", n)
	}

	// The pre-owned row must not have been reattributed.
	var owner string
	if err := pool.QueryRow(ctx, `SELECT domain_id FROM kiff_approvals WHERE id='b1'`).Scan(&owner); err != nil {
		t.Fatalf("read b1: %v", err)
	}
	if owner != domainB {
		t.Fatalf("b1 domain = %q, want %q — explicit attribution must not touch already-attributed rows", owner, domainB)
	}

	// And the formerly-legacy rows are now visible to the scoped store.
	scoped := storeFor(t, pool, domainA)
	got, err := scoped.List(ctx, "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("domainA sees %d approvals after attribution, want 2", len(got))
	}
}

func TestAttributeLegacy_RejectsEmptyDomain(t *testing.T) {
	t.Parallel()
	pool := newSchema(t)
	ctx := context.Background()
	mustTx(t, pool, func(tx pgx.Tx) error {
		if _, err := approvals.AttributeLegacy(ctx, tx, ""); err == nil {
			t.Error("expected an error when backfilling with no domain id")
		}
		return nil
	})
}

// CountUnattributed is the pre-deploy measurement. The pending subset is
// the number that matters: those approvals stop satisfying their action
// once the scoped store ships.
func TestCountUnattributed_SplitsPending(t *testing.T) {
	t.Parallel()
	pool := newSchema(t)
	ctx := context.Background()

	if _, err := pool.Exec(ctx, `
		INSERT INTO kiff_approvals (id, entity_id, entity_type, action_name,
		                            requested_by, status, created_at, domain_id,
		                            reviewed_by, reviewed_at)
		VALUES ('u1','o1','Order','REFUND','x','pending',NOW(),'',NULL,NULL),
		       ('u2','o2','Order','REFUND','x','pending',NOW(),'',NULL,NULL),
		       ('u3','o3','Order','REFUND','x','granted',NOW(),'','r',NOW()),
		       ('a1','o4','Order','REFUND','x','pending',NOW(),$1,NULL,NULL)
	`, domainA); err != nil {
		t.Fatalf("seed: %v", err)
	}

	var total, pendingCount int64
	mustTx(t, pool, func(tx pgx.Tx) error {
		var err error
		total, pendingCount, err = approvals.CountUnattributed(ctx, tx)
		return err
	})
	if total != 3 {
		t.Errorf("unattributed total = %d, want 3 (the attributed row must not count)", total)
	}
	if pendingCount != 2 {
		t.Errorf("unattributed pending = %d, want 2", pendingCount)
	}
}

// A dry run must leave the schema exactly as it found it — including the
// DDL, which Postgres makes transactional. That is what lets the
// measurement report on a column that does not exist yet.
func TestDryRun_RollsBackTheDDL(t *testing.T) {
	t.Parallel()
	url := requireURL(t)
	ctx := context.Background()

	// A schema with the framework table but *without* the migration.
	pool, schema := frameworkOnlySchema(t, url)

	// Simulate the dry-run transaction: apply, measure, roll back.
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := approvals.SetMigrationTimeouts(ctx, tx); err != nil {
		t.Fatalf("timeouts: %v", err)
	}
	if err := approvals.ApplySchema(ctx, tx); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if _, _, err := approvals.CountUnattributed(ctx, tx); err != nil {
		t.Fatalf("count inside dry run: %v", err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatalf("rollback: %v", err)
	}

	var n int
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM information_schema.columns
		WHERE table_schema = $1 AND table_name = 'kiff_approvals' AND column_name = 'domain_id'
	`, schema).Scan(&n); err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if n != 0 {
		t.Fatal("dry run left domain_id behind; the DDL must roll back with the transaction")
	}
}
