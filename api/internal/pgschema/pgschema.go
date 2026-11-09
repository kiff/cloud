// Package pgschema applies a store's schema at boot only when its text
// changed.
//
// A schema is idempotent DDL (CREATE … IF NOT EXISTS, ALTER TABLE … ADD
// COLUMN IF NOT EXISTS), so applying it on every boot looked free. It
// isn't: ALTER TABLE takes an ACCESS EXCLUSIVE lock even when the column
// exists, and the whole schema runs in one transaction. During a deploy the
// new instance's boot held those locks while the old instance served,
// stalling its queries and deadlocking with its Card-warnings worker.
//
// Apply records each schema's SHA-256 in cloud_meta.schemas_applied and
// runs nothing when it matches. A changed schema runs in one transaction
// with a lock_timeout below Postgres' deadlock_timeout, so it gives way to
// live traffic and is tried again, never deadlocking and rarely failing a
// boot.
package pgschema

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const guardSQL = `CREATE SCHEMA IF NOT EXISTS cloud_meta;
CREATE TABLE IF NOT EXISTS cloud_meta.schemas_applied (
    name       TEXT        PRIMARY KEY, -- the store's schema: "<package>/<file>"
    sha256     TEXT        NOT NULL,    -- of the text last applied in full
    applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
);`

// LockTimeout bounds each wait for a lock while a changed schema applies.
// It is below Postgres' default deadlock_timeout (1 s), so the schema gives
// way before a deadlock would be detected, and its transaction, never a
// live one, is the one undone.
var LockTimeout = 500 * time.Millisecond

// Attempts is how many times a changed schema is tried when it gave way.
var Attempts = 8

// Apply runs sql, the schema called name, unless the database last applied
// exactly this text. Another error than giving way to a lock is returned at
// once, with nothing applied.
func Apply(ctx context.Context, pool *pgxpool.Pool, name, sql string) error {
	sum := Sum(sql)
	if got, err := applied(ctx, pool, name); err != nil {
		return err
	} else if got == sum {
		return nil
	}
	for attempt := 1; ; attempt++ {
		err := apply(ctx, pool, name, sql, sum)
		if err == nil || !gaveWay(err) || attempt >= Attempts {
			if err != nil {
				return fmt.Errorf("schema %s: %w", name, err)
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("schema %s: %w", name, ctx.Err())
		case <-time.After(time.Duration(attempt) * 200 * time.Millisecond):
		}
	}
}

// Forget makes the next Apply of name run its schema again, whatever was
// recorded: for a test that dropped the tables, or a database repaired by
// hand.
func Forget(ctx context.Context, pool *pgxpool.Pool, name string) error {
	_, err := pool.Exec(ctx, `DELETE FROM cloud_meta.schemas_applied WHERE name = $1`, name)
	if missingTable(err) {
		return nil
	}
	return err
}

// Sum is the recorded hash of a schema's text.
func Sum(sql string) string {
	h := sha256.Sum256([]byte(sql))
	return hex.EncodeToString(h[:])
}

func applied(ctx context.Context, pool *pgxpool.Pool, name string) (string, error) {
	var sum string
	err := pool.QueryRow(ctx, `SELECT sha256 FROM cloud_meta.schemas_applied WHERE name = $1`, name).Scan(&sum)
	switch {
	case errors.Is(err, pgx.ErrNoRows), missingTable(err):
		return "", nil
	case err != nil:
		return "", fmt.Errorf("schema %s: read: %w", name, err)
	}
	return sum, nil
}

func apply(ctx context.Context, pool *pgxpool.Pool, name, sql, sum string) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if _, err = tx.Exec(ctx, fmt.Sprintf("SET LOCAL lock_timeout = '%dms'", LockTimeout.Milliseconds())); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, guardSQL); err != nil {
		return err
	}
	// Two instances booting at once apply it one after the other; the
	// second finds it recorded.
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, "schema:"+name); err != nil {
		return err
	}
	var got string
	err = tx.QueryRow(ctx, `SELECT sha256 FROM cloud_meta.schemas_applied WHERE name = $1`, name).Scan(&got)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if got != sum {
		if _, err = tx.Exec(ctx, sql); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO cloud_meta.schemas_applied (name, sha256) VALUES ($1, $2)
			ON CONFLICT (name) DO UPDATE SET sha256 = EXCLUDED.sha256, applied_at = now()`, name, sum); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// gaveWay is a lock wait the schema gave up (lock_timeout), or a deadlock
// it was chosen to undo: the same text succeeds once the lock is free.
func gaveWay(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && (pgErr.Code == "55P03" || pgErr.Code == "40P01")
}

func missingTable(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && (pgErr.Code == "42P01" || pgErr.Code == "3F000")
}
