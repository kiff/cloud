// Package idempotency provides the cross-request serialization the
// proposal gate uses to make its dedup atomic.
//
// A first version shipped read-then-evaluate-then-write dedup with no
// atomicity: two concurrent retries of the same idempotency key
// both observe "no prior" and both evaluate + write, producing
// duplicate decision/audit rows under exactly the concurrent retry
// storm the feature targets. AdvisoryLocker closes that by
// serializing the dedup+evaluate+write critical section on a
// per-(tenant, key) Postgres advisory lock: the second caller
// blocks until the first finishes, then observes the first's
// recorded decision and dedupes instead of re-evaluating.
package idempotency

import (
	"context"
	"fmt"
	"hash/fnv"
	"sort"

	"github.com/jackc/pgx/v5/pgxpool"
)

// AdvisoryLocker takes a session-scoped Postgres advisory lock for
// the duration of a proposal's critical section. It holds one
// pooled connection per held lock; the lock and unlock must run on
// the same session, so the connection is parked until release.
//
// The pool MUST be dedicated to locking — never the pool the
// limiter and stores draw from. The lock is held across the
// dedup+limit+evaluate critical section, and the limiter needs its
// own connection during that window; sharing one pool deadlocks
// under a same-key retry storm (waiters park connections blocked
// in pg_advisory_lock while the leader can't get a connection for
// the limiter). With a dedicated pool the lock connections and the
// work connections can never starve each other; excess waiters
// simply queue in Go for a lock-pool connection.
//
// Concurrency cost: a held lock parks a (lock-pool) connection for
// the length of one evaluate(), which is short. Contention on a
// single key — the retry storm — serializes by design.
type AdvisoryLocker struct {
	pool *pgxpool.Pool
}

// NewAdvisoryLocker wraps the dedicated lock pool. Returns nil when
// pool is nil so callers can treat "no locker" as "no atomicity"
// (the sequential dedup still applies).
func NewAdvisoryLocker(pool *pgxpool.Pool) *AdvisoryLocker {
	if pool == nil {
		return nil
	}
	return &AdvisoryLocker{pool: pool}
}

// Acquire takes the advisory lock for key and returns a release
// func that unlocks and returns the connection to the pool. The
// returned release is always safe to call exactly once; it is
// nil only when err is non-nil.
//
// The key is hashed to the single-bigint advisory-lock space.
// Collisions only cause two unrelated keys to serialize against
// each other — a correctness-neutral, low-probability slowdown,
// never a missed lock.
func (l *AdvisoryLocker) Acquire(ctx context.Context, key string) (func(), error) {
	conn, err := l.pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("idempotency: acquire conn: %w", err)
	}
	lockID := lockKey(key)
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", lockID); err != nil {
		conn.Release()
		return nil, fmt.Errorf("idempotency: advisory lock: %w", err)
	}
	released := false
	return func() {
		if released {
			return
		}
		released = true
		// Best-effort unlock on the same session, then release.
		// Use a background context so the unlock still runs if the
		// request context was cancelled mid-flight; the lock must
		// not outlive the request.
		_, _ = conn.Exec(context.Background(), "SELECT pg_advisory_unlock($1)", lockID)
		conn.Release()
	}, nil
}

// AcquireAll takes the advisory locks for every key on ONE pooled
// connection, in sorted order, and returns a release that unlocks them
// all and returns the connection.
//
// One connection, not one per key: Acquire pins a connection for each
// lock, so taking a lock per Card that way can exhaust the pool while
// earlier locks are still held. Sorted,
// deduplicated keys give every caller the same acquisition order, so
// two requests over overlapping sets cannot deadlock. On any error the
// locks already taken are released before returning.
func (l *AdvisoryLocker) AcquireAll(ctx context.Context, keys []string) (func(), error) {
	ids := sortedLockIDs(keys)
	conn, err := l.pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("idempotency: acquire conn: %w", err)
	}
	unlock := func(n int) {
		for i := n - 1; i >= 0; i-- {
			_, _ = conn.Exec(context.Background(), "SELECT pg_advisory_unlock($1)", ids[i])
		}
		conn.Release()
	}
	for i, id := range ids {
		if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", id); err != nil {
			unlock(i)
			return nil, fmt.Errorf("idempotency: advisory lock: %w", err)
		}
	}
	released := false
	return func() {
		if released {
			return
		}
		released = true
		unlock(len(ids))
	}, nil
}

// sortedLockIDs hashes keys to lock ids, drops duplicates and sorts
// them, so every caller locks a given set in the same order.
func sortedLockIDs(keys []string) []int64 {
	seen := make(map[int64]bool, len(keys))
	ids := make([]int64, 0, len(keys))
	for _, k := range keys {
		id := lockKey(k)
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

// lockKey hashes a string into the int64 advisory-lock space.
func lockKey(s string) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(s))
	return int64(h.Sum64())
}
