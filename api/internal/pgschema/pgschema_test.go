package pgschema_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kiff/cloud/api/internal/pgschema"
	"github.com/kiff/cloud/api/internal/testdb"
)

const v1 = `CREATE TABLE IF NOT EXISTS pgschema_t (id INT);
ALTER TABLE pgschema_t ADD COLUMN IF NOT EXISTS a INT;`

const v2 = v1 + `
ALTER TABLE pgschema_t ADD COLUMN IF NOT EXISTS b INT;`

func pool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := testdb.URL(t)
	if url == "" {
		t.Skip(testdb.EnvVar + " not set")
	}
	p, err := pgxpool.New(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	ctx := context.Background()
	if _, err := p.Exec(ctx, `DROP TABLE IF EXISTS pgschema_t`); err != nil {
		t.Fatal(err)
	}
	if err := pgschema.Forget(ctx, p, "test/t"); err != nil {
		t.Fatal(err)
	}
	if err := pgschema.Apply(ctx, p, "test/t", v1); err != nil {
		t.Fatal(err)
	}
	return p
}

// holding is a transaction that reads pgschema_t and stays open, as a live
// query on the old instance does during a deploy: it holds the table's
// ACCESS SHARE lock, which an ALTER TABLE waits for.
func holding(t *testing.T, p *pgxpool.Pool) (release func()) {
	t.Helper()
	ctx := context.Background()
	tx, err := p.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `LOCK TABLE pgschema_t IN ACCESS SHARE MODE`); err != nil {
		t.Fatal(err)
	}
	done := false
	release = func() {
		if !done {
			done = true
			_ = tx.Rollback(ctx)
		}
	}
	t.Cleanup(release)
	return release
}

func columns(t *testing.T, p *pgxpool.Pool) map[string]bool {
	t.Helper()
	rows, err := p.Query(context.Background(), `SELECT column_name FROM information_schema.columns WHERE table_name = 'pgschema_t'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			t.Fatal(err)
		}
		out[c] = true
	}
	return out
}

func recorded(t *testing.T, p *pgxpool.Pool) string {
	t.Helper()
	var sum string
	if err := p.QueryRow(context.Background(), `SELECT sha256 FROM cloud_meta.schemas_applied WHERE name = 'test/t'`).Scan(&sum); err != nil {
		t.Fatal(err)
	}
	return sum
}

// An unchanged schema runs no DDL, so it takes no ACCESS EXCLUSIVE lock: it
// returns at once while another transaction reads the table, where its
// ALTER TABLE ... IF NOT EXISTS, run again, would wait.
func TestAnUnchangedSchemaTakesNoLock(t *testing.T) {
	p := pool(t)
	ctx := context.Background()
	holding(t, p)

	// The ALTER on its own does wait for the reader, column there or not.
	tx, err := p.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = tx.Exec(ctx, `SET LOCAL lock_timeout = '100ms'`)
	_, err = tx.Exec(ctx, `ALTER TABLE pgschema_t ADD COLUMN IF NOT EXISTS a INT`)
	_ = tx.Rollback(ctx)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "55P03" {
		t.Fatalf("the ALTER alone didn't wait for the lock: %v", err)
	}

	start := time.Now()
	if err := pgschema.Apply(ctx, p, "test/t", v1); err != nil {
		t.Fatal(err)
	}
	if took := time.Since(start); took > 200*time.Millisecond {
		t.Fatalf("an unchanged schema waited %v: it took a lock", took)
	}
}

// A changed schema gives way to the reader and is tried again, then applies
// once the reader is done, and its text is recorded.
func TestAChangedSchemaGivesWayThenApplies(t *testing.T) {
	p := pool(t)
	release := holding(t, p)
	go func() { time.Sleep(700 * time.Millisecond); release() }()
	if err := pgschema.Apply(context.Background(), p, "test/t", v2); err != nil {
		t.Fatal(err)
	}
	if !columns(t, p)["b"] || recorded(t, p) != pgschema.Sum(v2) {
		t.Fatalf("not applied: %v %s", columns(t, p), recorded(t, p))
	}
}

// A changed schema whose lock never frees stops after its attempts with
// the lock's error, having applied and recorded nothing.
func TestAChangedSchemaStopsAfterItsAttempts(t *testing.T) {
	p := pool(t)
	holding(t, p)
	attempts, timeout := pgschema.Attempts, pgschema.LockTimeout
	pgschema.Attempts, pgschema.LockTimeout = 2, 50*time.Millisecond
	t.Cleanup(func() { pgschema.Attempts, pgschema.LockTimeout = attempts, timeout })
	err := pgschema.Apply(context.Background(), p, "test/t", v2)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "55P03" {
		t.Fatalf("want the lock's error, got %v", err)
	}
	if columns(t, p)["b"] || recorded(t, p) != pgschema.Sum(v1) {
		t.Fatalf("partly applied: %v %s", columns(t, p), recorded(t, p))
	}
}

// Forget makes the next Apply run the schema again: tables dropped since
// come back.
func TestForgetRunsTheSchemaAgain(t *testing.T) {
	p := pool(t)
	ctx := context.Background()
	if _, err := p.Exec(ctx, `DROP TABLE pgschema_t`); err != nil {
		t.Fatal(err)
	}
	if err := pgschema.Apply(ctx, p, "test/t", v1); err != nil {
		t.Fatal(err)
	}
	if len(columns(t, p)) != 0 {
		t.Fatal("an unchanged schema ran again")
	}
	if err := pgschema.Forget(ctx, p, "test/t"); err != nil {
		t.Fatal(err)
	}
	if err := pgschema.Apply(ctx, p, "test/t", v1); err != nil {
		t.Fatal(err)
	}
	if !columns(t, p)["a"] {
		t.Fatal("not applied again after Forget")
	}
}
