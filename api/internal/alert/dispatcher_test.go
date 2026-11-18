package alert_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/alert"
)

// captureSink records the targets and payloads it was asked to send,
// and signals each send on a channel so a test can wait for async
// delivery deterministically. An optional failURL errors to exercise
// the best-effort path.
type captureSink struct {
	mu      sync.Mutex
	sent    []alert.Target
	bodies  [][]byte
	signal  chan struct{}
	failURL string
}

func newCaptureSink(buf int) *captureSink {
	return &captureSink{signal: make(chan struct{}, buf)}
}

func (s *captureSink) Send(_ context.Context, target alert.Target, payload []byte) error {
	s.mu.Lock()
	s.sent = append(s.sent, target)
	s.bodies = append(s.bodies, payload)
	s.mu.Unlock()
	if s.signal != nil {
		s.signal <- struct{}{}
	}
	if s.failURL != "" && target.URL == s.failURL {
		return errors.New("boom")
	}
	return nil
}

func (s *captureSink) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.sent)
}

// waitFor waits until the sink has recorded n sends or the timeout
// elapses.
func (s *captureSink) waitFor(t *testing.T, n int) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	got := 0
	for got < n {
		select {
		case <-s.signal:
			got++
		case <-deadline:
			t.Fatalf("timed out waiting for %d sends; got %d", n, s.count())
		}
	}
}

func ctx() context.Context { return context.Background() }

// Only enabled targets whose action + outcome match receive the
// alert; the payload carries the essentials and no parameters.
func TestDispatcher_FansOutToMatchingTargetsOnly(t *testing.T) {
	t.Parallel()
	store := alert.NewMemoryStore()
	// Matches approval_required for any action.
	_ = store.CreateTarget(ctx(), alert.Target{ID: "t-approval", TenantID: "T", URL: "https://h/approval", Outcomes: []string{"approval_required"}, Enabled: true})
	// Matches blocked only — should NOT receive approval_required.
	_ = store.CreateTarget(ctx(), alert.Target{ID: "t-blocked", TenantID: "T", URL: "https://h/blocked", Outcomes: []string{"blocked"}, Enabled: true})
	// All outcomes but scoped to a different action — should NOT match.
	_ = store.CreateTarget(ctx(), alert.Target{ID: "t-otheraction", TenantID: "T", URL: "https://h/other", Action: "AUTO_REFUND", Enabled: true})
	// Disabled — should NOT receive anything.
	_ = store.CreateTarget(ctx(), alert.Target{ID: "t-disabled", TenantID: "T", URL: "https://h/disabled", Enabled: false})
	// All outcomes, all actions — should match.
	_ = store.CreateTarget(ctx(), alert.Target{ID: "t-all", TenantID: "T", URL: "https://h/all", Enabled: true})

	sink := newCaptureSink(8)
	d := alert.NewDispatcher(store, sink, alert.WithBaseURL("https://app.kiff.dev"))

	d.Notify(alert.Event{
		TenantID: "T", Action: "REFUND_ORDER", EntityID: "o1", EntityType: "Order",
		Outcome: "approval_required", Reasons: []string{"approval_required"},
		Approval:  &alert.Approval{ID: "apr_1", Status: "pending", ReviewURL: "/dashboard/approvals"},
		DecidedAt: time.Now().UTC(),
	})

	// Expect exactly 2 sends: t-approval and t-all.
	sink.waitFor(t, 2)
	// Give a brief grace to catch any erroneous extra send.
	time.Sleep(100 * time.Millisecond)
	if sink.count() != 2 {
		t.Fatalf("expected 2 matching sends; got %d", sink.count())
	}

	urls := map[string]bool{}
	var body []byte
	sink.mu.Lock()
	for i, tg := range sink.sent {
		urls[tg.URL] = true
		body = sink.bodies[i]
	}
	sink.mu.Unlock()
	if !urls["https://h/approval"] || !urls["https://h/all"] {
		t.Fatalf("wrong targets matched: %v", urls)
	}

	// Payload: review URL absolutized; no "parameters" key.
	var p map[string]any
	if err := json.Unmarshal(body, &p); err != nil {
		t.Fatalf("payload not JSON: %v", err)
	}
	if _, leaked := p["parameters"]; leaked {
		t.Fatal("payload must not carry request parameters")
	}
	ap, _ := p["approval"].(map[string]any)
	if ap == nil || ap["review_url"] != "https://app.kiff.dev/dashboard/approvals" {
		t.Fatalf("review_url not absolutized: %+v", ap)
	}
}

// One failing target must not stop delivery to the others, and must
// not panic — best-effort.
func TestDispatcher_BestEffortOnSinkFailure(t *testing.T) {
	t.Parallel()
	store := alert.NewMemoryStore()
	_ = store.CreateTarget(ctx(), alert.Target{ID: "t-bad", TenantID: "T", URL: "https://bad", Enabled: true})
	_ = store.CreateTarget(ctx(), alert.Target{ID: "t-good", TenantID: "T", URL: "https://good", Enabled: true})

	sink := newCaptureSink(8)
	sink.failURL = "https://bad"
	d := alert.NewDispatcher(store, sink)

	d.Notify(alert.Event{TenantID: "T", Action: "X", EntityID: "e", Outcome: "blocked", DecidedAt: time.Now().UTC()})

	// Both are attempted (the failure is logged, not fatal).
	sink.waitFor(t, 2)
}

// Notify returns immediately even if the sink blocks — it must never
// block the caller (the decide path).
func TestDispatcher_NotifyIsNonBlocking(t *testing.T) {
	t.Parallel()
	store := alert.NewMemoryStore()
	_ = store.CreateTarget(ctx(), alert.Target{ID: "t", TenantID: "T", URL: "https://h", Enabled: true})

	block := make(chan struct{})
	d := alert.NewDispatcher(store, blockingSink{release: block})

	done := make(chan struct{})
	go func() {
		d.Notify(alert.Event{TenantID: "T", Action: "X", EntityID: "e", Outcome: "blocked", DecidedAt: time.Now().UTC()})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Notify blocked the caller")
	}
	close(block) // let the background delivery finish
}

type blockingSink struct{ release chan struct{} }

func (b blockingSink) Send(_ context.Context, _ alert.Target, _ []byte) error {
	<-b.release
	return nil
}

// With no base URL configured, a relative review URL is delivered
// as-is (the receiver knows its own app host).
func TestDispatcher_EmptyBaseURLLeavesReviewURLRelative(t *testing.T) {
	t.Parallel()
	store := alert.NewMemoryStore()
	_ = store.CreateTarget(ctx(), alert.Target{ID: "t", TenantID: "T", URL: "https://h", Enabled: true})

	sink := newCaptureSink(4)
	d := alert.NewDispatcher(store, sink) // no WithBaseURL

	d.Notify(alert.Event{
		TenantID: "T", Action: "REFUND_ORDER", EntityID: "o1", Outcome: "approval_required",
		Approval:  &alert.Approval{ID: "apr_1", Status: "pending", ReviewURL: "/dashboard/approvals"},
		DecidedAt: time.Now().UTC(),
	})
	sink.waitFor(t, 1)

	sink.mu.Lock()
	body := sink.bodies[0]
	sink.mu.Unlock()
	var p map[string]any
	if err := json.Unmarshal(body, &p); err != nil {
		t.Fatalf("payload not JSON: %v", err)
	}
	ap, _ := p["approval"].(map[string]any)
	if ap == nil || ap["review_url"] != "/dashboard/approvals" {
		t.Fatalf("review_url should be relative as-is; got %+v", ap)
	}
}
