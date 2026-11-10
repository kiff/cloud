package thune

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
)

// Reader reads a charge's facts from the processor, as an agent through
// its own Card: the case reads the profile lists. It never writes.
type Reader interface {
	ReadCharge(ctx context.Context, tenant, account, charge string) (Facts, error)
}

// Calls lets thune settle the gateway's record of a call whose answer
// was lost, once its refund is found by its marker. Never a resend.
type Calls interface {
	Found(ctx context.Context, key string) error
}

// The watch follows sent and unknown refunds until their money ends, and
// sends nothing, ever: unknown means no blind resend.
//
// It holds no authority of its own (thune design v3, step one): the
// processor's events, brought by the connection service, are its main
// source (Settle), and it reads a charge only as an agent, when one of
// that account's agents makes a governed call (RunAccount). So a refund
// whose event never arrives, on an account whose agents never call again,
// is seen late; "Did this happen?" still covers an unknown call.
//
// The room a failed refund drew comes back, and the owner is told, from
// apps/api reading thune's rows itself, never from this process's word.
type Watcher struct {
	Store  *Store
	Calls  Calls
	Logger *slog.Logger
	// Give up after this long without an end. Zero: 7 days.
	MaxWatch time.Duration
	// Batch is how many rows one run takes. Zero: 5: an agent's call
	// waits on these reads.
	Batch int
}

func (w *Watcher) maxWatch() time.Duration {
	if w.MaxWatch > 0 {
		return w.MaxWatch
	}
	return 7 * 24 * time.Hour
}

// RunAccount takes due watch rows on one processor account, reads each
// charge through r, and returns how many it processed. Rows another
// watcher holds are skipped.
func (w *Watcher) RunAccount(ctx context.Context, tenant, account string, r Reader) (int, error) {
	n := w.Batch
	if n <= 0 {
		n = 5
	}
	done := 0
	for i := 0; i < n; i++ {
		ok, err := w.one(ctx, tenant, account, r)
		if err != nil {
			return done, err
		}
		if !ok {
			break
		}
		done++
	}
	return done, nil
}

// one processes one due row; false when none is due.
func (w *Watcher) one(ctx context.Context, tenant, account string, r Reader) (bool, error) {
	s := w.Store
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	var key string
	var attempts int
	err = tx.QueryRow(ctx, `SELECT w.key, w.attempts FROM gateway.thune_watch_work w JOIN gateway.thune_adjustments a ON a.key = w.key
		WHERE w.completed_at IS NULL AND w.available_at <= $1 AND a.tenant_id = $2 AND a.account = $3
		ORDER BY w.available_at, w.key LIMIT 1 FOR UPDATE OF w SKIP LOCKED`, s.now(), tenant, account).Scan(&key, &attempts)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	finished, werr := w.watch(ctx, key, r)
	if werr != nil {
		// Back off: 5 s per attempt, at most 5 minutes.
		backoff := time.Duration(min(300, 5*(attempts+1))) * time.Second
		if _, err := tx.Exec(ctx, `UPDATE gateway.thune_watch_work SET attempts=attempts+1, last_error=$2, available_at=$3 WHERE key=$1`,
			key, werr.Error(), s.now().Add(backoff)); err != nil {
			return false, err
		}
		if w.Logger != nil {
			w.Logger.WarnContext(ctx, "thune: watch failed", slog.String("invocation", key), slog.String("err", werr.Error()))
		}
		return true, tx.Commit(ctx)
	}
	if finished {
		_, err = tx.Exec(ctx, `UPDATE gateway.thune_watch_work SET completed_at=$2 WHERE key=$1`, key, s.now())
	} else {
		// Read again: soon at first, then less often, at most hourly.
		next := time.Duration(min(3600, 30*(attempts+1))) * time.Second
		_, err = tx.Exec(ctx, `UPDATE gateway.thune_watch_work SET attempts=attempts+1, last_error='', available_at=$2 WHERE key=$1`,
			key, s.now().Add(next))
	}
	if err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}

// watch reads key's charge once and records it. It reports whether the
// watch is over.
func (w *Watcher) watch(ctx context.Context, key string, r Reader) (bool, error) {
	a, err := w.Store.Get(ctx, key)
	if err != nil {
		return false, err
	}
	if !a.Watchable || ended(a.Financial) || w.Store.now().Sub(a.CreatedAt) > w.maxWatch() {
		return true, nil
	}
	f, err := r.ReadCharge(ctx, a.Identity.TenantID, a.Identity.Account, a.Identity.Object)
	if err != nil {
		return false, err
	}
	if err := w.Settle(ctx, a.Identity.TenantID, f); err != nil {
		return false, err
	}
	a, err = w.Store.Get(ctx, key)
	if err != nil {
		return false, err
	}
	return ended(a.Financial), nil
}

func ended(fin string) bool {
	return fin == FinSucceeded || fin == FinFailed || fin == FinCancelled
}

// Settle records what the processor said about a charge (a full read, or
// one refund from an event) and acts on it, once however often it's said:
// a lost call found by its marker is settled as sent, and a refund that
// failed or was cancelled releases its request's claim, since the money is
// known not to have moved.
func (w *Watcher) Settle(ctx context.Context, tenant string, f Facts) error {
	obs, err := w.Store.Observe(ctx, tenant, f)
	if err != nil {
		return err
	}
	for _, c := range obs.Changes {
		if c.Found && w.Calls != nil {
			if err := w.Calls.Found(ctx, c.Key); err != nil {
				return err
			}
		}
		if c.ToFin == FinFailed || c.ToFin == FinCancelled {
			a, err := w.Store.Get(ctx, c.Key)
			if err != nil {
				return err
			}
			if a.Identity.Request != "" {
				if _, err := w.Store.Release(ctx, a.Identity, c.Key); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
