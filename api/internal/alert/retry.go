package alert

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log/slog"
	"sort"
	"strconv"
	"sync"
	"time"
)

// Retries: a delivery that failed is sent again on a backoff, up
// to about a day, from a durable queue, so a receiver that was briefly
// down still hears about a held call. A target whose deliveries keep
// failing for that long is switched off, with the reason, until its
// owner switches it back on. A delivery the SSRF guard refused is never
// retried: the address won't become safe by waiting.

// RetryBackoff is how long to wait before each retry, after the first
// attempt failed: seven retries over about 22 hours.
var RetryBackoff = []time.Duration{time.Minute, 5 * time.Minute, 15 * time.Minute, time.Hour, 3 * time.Hour, 6 * time.Hour, 12 * time.Hour}

// retryLease is how long a claimed retry is hidden from other claims
// while it is being sent. A retry is claimed just before its one send,
// which times out well within it.
const retryLease = 2 * time.Minute

// retryBatch bounds how many retries one run sends, one after another.
const retryBatch = 50

// ErrLeaseLost is returned when a retry is updated by a worker whose claim
// lapsed and was taken by another: that worker's update is refused, so a
// stale send can't overwrite a newer attempt or finish it.
var ErrLeaseLost = errors.New("alert: the retry was claimed by another worker")

// Retry is one failed delivery waiting to be sent again: the exact body
// (signed afresh on each send), its target, and how often it has failed.
type Retry struct {
	ID         string
	TenantID   string
	TargetID   string
	EventID    string
	ProposalID string
	Action     string
	Outcome    string
	Body       []byte
	Attempts   int // failed sends so far, the first included; the next wait is RetryBackoff[Attempts-1]
	NextAt     time.Time
	LastError  string
	CreatedAt  time.Time
	// Claim names the claim that holds the retry: RescheduleRetry and
	// FinishRetry act only while it is still the current one.
	Claim string
}

// RetryStore is the durable retry queue.
type RetryStore interface {
	// ScheduleRetry queues r.
	ScheduleRetry(ctx context.Context, r Retry) error
	// ClaimDueRetry claims the retry due soonest at now, if any: it is
	// hidden from other claims until now+lease, under a new Claim.
	ClaimDueRetry(ctx context.Context, now time.Time, lease time.Duration) (Retry, bool, error)
	// RescheduleRetry records r's attempts, next attempt and last error,
	// if r's claim still holds it; ErrLeaseLost otherwise.
	RescheduleRetry(ctx context.Context, r Retry) error
	// FinishRetry removes r from the queue (sent, given up, or moot), if
	// r's claim still holds it; ErrLeaseLost otherwise.
	FinishRetry(ctx context.Context, r Retry) error
	// GiveUp switches r's target off with reason and removes r from the
	// queue, as one step and only while r's claim holds it: a worker
	// whose claim lapsed (another may have delivered it since) gets
	// ErrLeaseLost and changes nothing. On any error nothing changes, and
	// the retry is claimed again once its lease ends.
	GiveUp(ctx context.Context, r Retry, reason string, at time.Time) error
}

func newClaim() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// MemoryRetryStore is the in-process RetryStore.
type MemoryRetryStore struct {
	mu      sync.Mutex
	retries map[string]Retry // tenant|id
	targets Store            // GiveUp switches targets off here
}

// NewMemoryRetryStore returns an empty queue whose GiveUp switches
// targets off in targets.
func NewMemoryRetryStore(targets Store) *MemoryRetryStore {
	return &MemoryRetryStore{retries: map[string]Retry{}, targets: targets}
}

func (m *MemoryRetryStore) ScheduleRetry(_ context.Context, r Retry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.retries[r.TenantID+"|"+r.ID] = r
	return nil
}

func (m *MemoryRetryStore) ClaimDueRetry(_ context.Context, now time.Time, lease time.Duration) (Retry, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var due []Retry
	for _, r := range m.retries {
		if !r.NextAt.After(now) {
			due = append(due, r)
		}
	}
	if len(due) == 0 {
		return Retry{}, false, nil
	}
	sort.Slice(due, func(i, j int) bool { return due[i].NextAt.Before(due[j].NextAt) })
	r := due[0]
	r.Claim = newClaim()
	held := r
	held.NextAt = now.Add(lease)
	m.retries[r.TenantID+"|"+r.ID] = held
	return r, true, nil
}

// held reports whether r's claim still holds it; m.mu must be held.
func (m *MemoryRetryStore) held(r Retry) bool {
	cur, ok := m.retries[r.TenantID+"|"+r.ID]
	return ok && cur.Claim == r.Claim
}

func (m *MemoryRetryStore) RescheduleRetry(_ context.Context, r Retry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.held(r) {
		return ErrLeaseLost
	}
	m.retries[r.TenantID+"|"+r.ID] = r
	return nil
}

func (m *MemoryRetryStore) FinishRetry(_ context.Context, r Retry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.held(r) {
		return ErrLeaseLost
	}
	delete(m.retries, r.TenantID+"|"+r.ID)
	return nil
}

// GiveUp switches the target off under the queue's lock, so no claim,
// reschedule or finish of r can come between its check and its effect.
func (m *MemoryRetryStore) GiveUp(ctx context.Context, r Retry, reason string, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.held(r) {
		return ErrLeaseLost
	}
	if m.targets != nil {
		if _, err := m.targets.SetEnabled(ctx, r.TenantID, r.TargetID, false, reason, at); err != nil {
			return err
		}
	}
	delete(m.retries, r.TenantID+"|"+r.ID)
	return nil
}

// Pending is the queue's size, for tests.
func (m *MemoryRetryStore) Pending() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.retries)
}

// Retrier sends queued retries. Run it once per process.
type Retrier struct {
	Targets    Store
	Retries    RetryStore
	Deliveries DeliveryStore // optional: each retry is recorded like a first attempt
	Sink       Sink
	Logger     *slog.Logger
	Now        func() time.Time
	// Every is the polling interval; 30 seconds when zero.
	Every time.Duration
}

// Run polls until ctx is done.
func (r *Retrier) Run(ctx context.Context) {
	every := r.Every
	if every <= 0 {
		every = 30 * time.Second
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := r.RunOnce(ctx); err != nil && r.Logger != nil {
				r.Logger.WarnContext(ctx, "alert retries", slog.String("err", err.Error()))
			}
		}
	}
}

// RunOnce sends the retries due now, up to retryBatch, claiming each just
// before sending it: a slow send holds only its own retry, and the rest
// stay free for other workers.
func (r *Retrier) RunOnce(ctx context.Context) error {
	for range retryBatch {
		if ctx.Err() != nil {
			return nil
		}
		q, ok, err := r.Retries.ClaimDueRetry(ctx, r.now(), retryLease)
		if err != nil || !ok {
			return err
		}
		r.send(ctx, q)
	}
	return nil
}

func (r *Retrier) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now().UTC()
}

func (r *Retrier) send(ctx context.Context, q Retry) {
	targets, err := r.Targets.ListTargets(ctx, q.TenantID)
	if err != nil {
		r.warn(ctx, q, "list targets", err)
		return // the lease expires and it is claimed again
	}
	var target *Target
	for i := range targets {
		if targets[i].ID == q.TargetID {
			target = &targets[i]
		}
	}
	if target == nil || !target.Enabled {
		// Removed or switched off since: never send to it.
		r.finish(ctx, q)
		return
	}
	sendCtx, cancel := context.WithTimeout(ctx, defaultDeliveryTimeout)
	err = r.Sink.Send(sendCtx, *target, q.Body)
	cancel()
	now := r.now()
	switch {
	case err == nil:
		r.record(ctx, q, DeliveryDelivered, "retry "+strconv.Itoa(q.Attempts), now)
		r.finish(ctx, q)
	case errors.Is(err, ErrBlockedAddress):
		r.record(ctx, q, DeliveryBlocked, err.Error(), now)
		r.finish(ctx, q)
	case q.Attempts >= len(RetryBackoff):
		// That was the last retry. The target is switched off and the
		// retry leaves the queue in one step, only while this worker's
		// claim holds it. If that fails, the retry stays, is claimed
		// again once its lease ends, and is sent once more.
		r.record(ctx, q, DeliveryFailed, "gave up: "+err.Error(), now)
		reason := "deliveries kept failing for about a day: " + err.Error()
		if gerr := r.Retries.GiveUp(ctx, q, reason, now); gerr != nil {
			r.warn(ctx, q, "give up", gerr)
			return
		}
		if r.Logger != nil {
			r.Logger.WarnContext(ctx, "alert target switched off", slog.String("tenant", q.TenantID), slog.String("target", q.TargetID))
		}
	default:
		r.record(ctx, q, DeliveryFailed, err.Error(), now)
		q.Attempts++
		q.NextAt = now.Add(RetryBackoff[q.Attempts-1])
		q.LastError = err.Error()
		r.reschedule(ctx, q)
	}
}

// finish takes q off the queue; reschedule saves its next attempt.
func (r *Retrier) finish(ctx context.Context, q Retry) {
	if err := r.Retries.FinishRetry(ctx, q); err != nil {
		r.warn(ctx, q, "finish", err)
	}
}

func (r *Retrier) reschedule(ctx context.Context, q Retry) {
	if err := r.Retries.RescheduleRetry(ctx, q); err != nil {
		r.warn(ctx, q, "reschedule", err)
	}
}

func (r *Retrier) warn(ctx context.Context, q Retry, what string, err error) {
	if r.Logger != nil {
		r.Logger.WarnContext(ctx, "alert retry: "+what+" failed", slog.String("tenant", q.TenantID), slog.String("retry", q.ID), slog.String("err", err.Error()))
	}
}

func (r *Retrier) record(ctx context.Context, q Retry, status, detail string, at time.Time) {
	if r.Deliveries == nil {
		return
	}
	_ = r.Deliveries.RecordDelivery(ctx, Delivery{ID: newDeliveryID(), TenantID: q.TenantID, ProposalID: q.ProposalID,
		TargetID: q.TargetID, TargetType: "webhook", Action: q.Action, Outcome: q.Outcome, Status: status, Detail: detail, AttemptedAt: at})
}
