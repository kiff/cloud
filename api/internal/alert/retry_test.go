package alert_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/alert"
)

// flakySink fails the first `fail` sends (or all, with fail < 0), with err.
type flakySink struct {
	mu    sync.Mutex
	fail  int
	err   error
	sends int
	last  []byte
}

func (s *flakySink) Send(_ context.Context, _ alert.Target, body []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sends++
	s.last = body
	if s.fail < 0 || s.sends <= s.fail {
		return s.err
	}
	return nil
}

type retryRig struct {
	targets    *alert.MemoryStore
	retries    *alert.MemoryRetryStore
	deliveries *alert.MemoryDeliveryStore
	sink       *flakySink
	now        time.Time
	retrier    *alert.Retrier
}

func newRetryRig(t *testing.T, fail int, err error) *retryRig {
	t.Helper()
	targets := alert.NewMemoryStore()
	r := &retryRig{targets: targets, retries: alert.NewMemoryRetryStore(targets), deliveries: alert.NewMemoryDeliveryStore(),
		sink: &flakySink{fail: fail, err: err}, now: time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)}
	_ = r.targets.CreateTarget(ctx(), alert.Target{ID: "t-phone", TenantID: "T", URL: "https://h/phone", Enabled: true})
	r.retrier = &alert.Retrier{Targets: r.targets, Retries: r.retries, Deliveries: r.deliveries, Sink: r.sink, Now: func() time.Time { return r.now }}
	return r
}

// queue puts a first failed send of body in the queue, as the dispatcher does.
func (r *retryRig) queue(t *testing.T, body string) {
	t.Helper()
	if err := r.retries.ScheduleRetry(ctx(), alert.Retry{ID: "r1", TenantID: "T", TargetID: "t-phone", EventID: "evt_1", ProposalID: "p1",
		Action: "refund", Outcome: "owner_required", Body: []byte(body), Attempts: 1, NextAt: r.now.Add(alert.RetryBackoff[0])}); err != nil {
		t.Fatal(err)
	}
}

// step moves the clock to the next due time and runs the retrier.
func (r *retryRig) step(t *testing.T, d time.Duration) {
	t.Helper()
	r.now = r.now.Add(d)
	if err := r.retrier.RunOnce(ctx()); err != nil {
		t.Fatal(err)
	}
}

// A delivery that failed is sent again on the backoff, with the
// same body, until it goes through; nothing is sent before it is due.
func TestRetrierSendsAgainUntilItGoesThrough(t *testing.T) {
	r := newRetryRig(t, 2, errors.New("webhook returned 503"))
	r.sink.sends = 1 // the dispatcher's first send failed
	r.queue(t, `{"event_id":"evt_1"}`)
	r.step(t, 30*time.Second)
	if r.sink.sends != 1 {
		t.Fatal("a retry was sent before it was due")
	}
	r.step(t, 30*time.Second) // 1m: retry 1 fails
	r.step(t, 5*time.Minute)  // 6m: retry 2 goes through
	if r.sink.sends != 3 || string(r.sink.last) != `{"event_id":"evt_1"}` || r.retries.Pending() != 0 {
		t.Fatalf("sends %d, body %s, pending %d", r.sink.sends, r.sink.last, r.retries.Pending())
	}
	ds, _ := r.deliveries.ListDeliveries(ctx(), "T", "p1")
	if len(ds) != 2 || ds[0].Status != alert.DeliveryDelivered || ds[1].Status != alert.DeliveryFailed {
		t.Fatalf("deliveries = %+v", ds)
	}
}

// After the last retry, about a day later, KIFF gives up and switches the
// target off with the reason; it is not sent to again.
func TestRetrierGivesUpAndSwitchesTheTargetOff(t *testing.T) {
	r := newRetryRig(t, -1, errors.New("webhook returned 500"))
	r.queue(t, `{}`)
	var total time.Duration
	for _, wait := range alert.RetryBackoff {
		total += wait
		r.step(t, wait)
	}
	if r.sink.sends != len(alert.RetryBackoff) || r.retries.Pending() != 0 {
		t.Fatalf("sends %d, pending %d", r.sink.sends, r.retries.Pending())
	}
	if total < 20*time.Hour || total > 26*time.Hour {
		t.Errorf("retries span %v, want about a day", total)
	}
	targets, _ := r.targets.ListTargets(ctx(), "T")
	if targets[0].Enabled || !strings.Contains(targets[0].DisabledReason, "kept failing") || targets[0].DisabledAt == nil {
		t.Fatalf("target = %+v", targets[0])
	}
	if targets[0].Matches("refund", "owner_required") {
		t.Error("a switched-off target still matches")
	}
	ds, _ := r.deliveries.ListDeliveries(ctx(), "T", "p1")
	if !strings.HasPrefix(ds[0].Detail, "gave up") {
		t.Errorf("last delivery = %+v", ds[0])
	}
}

// A refusal by the SSRF guard is final, and a target removed or switched
// off since is never sent to.
func TestRetrierStopsForBlockedOrGoneTargets(t *testing.T) {
	r := newRetryRig(t, -1, fmt.Errorf("dial: %w", alert.ErrBlockedAddress))
	r.queue(t, `{}`)
	r.step(t, time.Minute)
	if r.sink.sends != 1 || r.retries.Pending() != 0 {
		t.Fatalf("blocked: sends %d, pending %d", r.sink.sends, r.retries.Pending())
	}
	if ts, _ := r.targets.ListTargets(ctx(), "T"); !ts[0].Enabled {
		t.Error("a blocked address switched the target off")
	}

	gone := newRetryRig(t, -1, errors.New("x"))
	gone.queue(t, `{}`)
	_ = gone.targets.DeleteTarget(ctx(), "T", "t-phone")
	gone.step(t, time.Minute)
	if gone.sink.sends != 0 || gone.retries.Pending() != 0 {
		t.Fatalf("removed target: sends %d, pending %d", gone.sink.sends, gone.retries.Pending())
	}
	off := newRetryRig(t, -1, errors.New("x"))
	off.queue(t, `{}`)
	_, _ = off.targets.SetEnabled(ctx(), "T", "t-phone", false, "", off.now)
	off.step(t, time.Minute)
	if off.sink.sends != 0 || off.retries.Pending() != 0 {
		t.Fatalf("switched-off target: sends %d, pending %d", off.sink.sends, off.retries.Pending())
	}
}

// The dispatcher queues a failed first send with its exact body; a
// delivered or blocked one is not queued.
func TestDispatcherQueuesAFailedDelivery(t *testing.T) {
	store := alert.NewMemoryStore()
	_ = store.CreateTarget(ctx(), alert.Target{ID: "t-1", TenantID: "T", URL: "https://h/fail", Enabled: true})
	retries := alert.NewMemoryRetryStore(store)
	sink := newCaptureSink(2)
	sink.failURL = "https://h/fail"
	d := alert.NewDispatcher(store, sink, alert.WithRetries(retries))
	d.Notify(alert.Event{TenantID: "T", Action: "refund", Outcome: "owner_required", DecidedAt: time.Now().UTC()})
	sink.waitFor(t, 1)
	deadline := time.Now().Add(2 * time.Second)
	for retries.Pending() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	due, ok, _ := retries.ClaimDueRetry(ctx(), time.Now().Add(time.Hour), time.Minute)
	if !ok || due.TargetID != "t-1" || due.Attempts != 1 || !strings.Contains(string(due.Body), `"event_id":"evt_`) {
		t.Fatalf("queued = %+v", due)
	}
}

// gateSink blocks the first send until released, then answers err.
type gateSink struct {
	mu      sync.Mutex
	sends   int
	waiting chan struct{}
	release chan struct{}
	err     error
}

func (s *gateSink) Send(_ context.Context, _ alert.Target, _ []byte) error {
	s.mu.Lock()
	s.sends++
	first := s.sends == 1
	s.mu.Unlock()
	if first {
		close(s.waiting)
		<-s.release
	}
	return s.err
}

// A worker claims a retry just before sending it, so a slow send holds
// only that retry: another worker sends the rest meanwhile. A worker whose
// claim lapsed and was taken can't overwrite or finish the retry after
// the new owner.
func TestRetriesAreClaimedOneAtATimeAndFenced(t *testing.T) {
	targets := alert.NewMemoryStore()
	retries := alert.NewMemoryRetryStore(targets)
	_ = targets.CreateTarget(ctx(), alert.Target{ID: "t-phone", TenantID: "T", URL: "https://h/phone", Enabled: true})
	var mu sync.Mutex
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	for i, id := range []string{"r1", "r2", "r3"} {
		_ = retries.ScheduleRetry(ctx(), alert.Retry{ID: id, TenantID: "T", TargetID: "t-phone", Body: []byte(`{}`), Attempts: 1,
			NextAt: now.Add(time.Duration(i) * time.Second)})
	}
	mu.Lock()
	now = now.Add(time.Minute)
	mu.Unlock()

	slow := &gateSink{waiting: make(chan struct{}), release: make(chan struct{}), err: errors.New("webhook returned 503")}
	a := &alert.Retrier{Targets: targets, Retries: retries, Sink: slow, Now: clock}
	done := make(chan error)
	go func() { done <- a.RunOnce(ctx()) }()
	<-slow.waiting // a is sending r1

	fast := &flakySink{}
	b := &alert.Retrier{Targets: targets, Retries: retries, Sink: fast, Now: clock}
	if err := b.RunOnce(ctx()); err != nil {
		t.Fatal(err)
	}
	if fast.sends != 2 || retries.Pending() != 1 {
		t.Fatalf("while a sends r1, b sent %d (want r2 and r3), pending %d", fast.sends, retries.Pending())
	}
	// a's send outlasts its lease: b claims r1 again and delivers it.
	mu.Lock()
	now = now.Add(3 * time.Minute)
	mu.Unlock()
	if err := b.RunOnce(ctx()); err != nil {
		t.Fatal(err)
	}
	if fast.sends != 3 || retries.Pending() != 0 {
		t.Fatalf("b after a's lease lapsed: sends %d, pending %d", fast.sends, retries.Pending())
	}
	// a's stale failure can't put r1 back in the queue.
	close(slow.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if slow.sends != 1 || retries.Pending() != 0 {
		t.Fatalf("a: sends %d, pending %d", slow.sends, retries.Pending())
	}
}

// failingSwitch fails switching a target off, the first `fail` times.
type failingSwitch struct {
	*alert.MemoryStore
	fail int
}

func (f *failingSwitch) SetEnabled(c context.Context, tenantID, id string, enabled bool, reason string, at time.Time) (bool, error) {
	if f.fail > 0 {
		f.fail--
		return false, errors.New("connection reset")
	}
	return f.MemoryStore.SetEnabled(c, tenantID, id, enabled, reason, at)
}

// Giving up switches the target off and takes the retry off the queue in
// one step: a switch-off that fails keeps the retry, and a later run
// completes it.
func TestRetrierKeepsTheRetryUntilTheTargetIsSwitchedOff(t *testing.T) {
	r := newRetryRig(t, -1, errors.New("webhook returned 500"))
	sw := &failingSwitch{MemoryStore: r.targets, fail: 1}
	r.retries = alert.NewMemoryRetryStore(sw)
	r.retrier.Retries = r.retries
	_ = r.retries.ScheduleRetry(ctx(), alert.Retry{ID: "r1", TenantID: "T", TargetID: "t-phone", Body: []byte(`{}`),
		Attempts: len(alert.RetryBackoff), NextAt: r.now})
	r.step(t, 0)
	if ts, _ := r.targets.ListTargets(ctx(), "T"); !ts[0].Enabled || r.retries.Pending() != 1 {
		t.Fatalf("after a failed switch-off: enabled %v, pending %d", ts[0].Enabled, r.retries.Pending())
	}
	r.step(t, 3*time.Minute) // its lease has ended
	ts, _ := r.targets.ListTargets(ctx(), "T")
	if ts[0].Enabled || !strings.Contains(ts[0].DisabledReason, "kept failing") || r.retries.Pending() != 0 {
		t.Fatalf("after the next run: target %+v, pending %d", ts[0], r.retries.Pending())
	}
}

// A worker whose claim lapsed while it sent the last retry can't switch
// the target off: another worker claimed the retry since and delivered
// it, so the target works.
func TestAStaleWorkerNeverSwitchesTheTargetOff(t *testing.T) {
	targets := alert.NewMemoryStore()
	retries := alert.NewMemoryRetryStore(targets)
	_ = targets.CreateTarget(ctx(), alert.Target{ID: "t-phone", TenantID: "T", URL: "https://h/phone", Enabled: true})
	var mu sync.Mutex
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	_ = retries.ScheduleRetry(ctx(), alert.Retry{ID: "r1", TenantID: "T", TargetID: "t-phone", Body: []byte(`{}`),
		Attempts: len(alert.RetryBackoff), NextAt: now})

	slow := &gateSink{waiting: make(chan struct{}), release: make(chan struct{}), err: errors.New("webhook returned 500")}
	a := &alert.Retrier{Targets: targets, Retries: retries, Sink: slow, Now: clock}
	done := make(chan error)
	go func() { done <- a.RunOnce(ctx()) }()
	<-slow.waiting // a is sending the last retry; it will fail
	mu.Lock()
	now = now.Add(3 * time.Minute)
	mu.Unlock()
	fast := &flakySink{}
	b := &alert.Retrier{Targets: targets, Retries: retries, Sink: fast, Now: clock}
	if err := b.RunOnce(ctx()); err != nil {
		t.Fatal(err)
	}
	if fast.sends != 1 || retries.Pending() != 0 {
		t.Fatalf("b: sends %d, pending %d", fast.sends, retries.Pending())
	}
	close(slow.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if ts, _ := targets.ListTargets(ctx(), "T"); !ts[0].Enabled || ts[0].DisabledReason != "" {
		t.Fatalf("a stale worker switched the target off: %+v", ts[0])
	}
}
