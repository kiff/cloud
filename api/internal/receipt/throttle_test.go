package receipt

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
)

// stubAttester is a controllable Attester for the throttle tests. It
// records each call and returns scripted results.
type stubAttester struct {
	addr common.Address
	// results is consumed one per Attest call. When exhausted, the
	// last entry repeats.
	results []stubResult
	calls   int
}

type stubResult struct {
	att *Attestation
	err error
}

func (s *stubAttester) Attest(ctx context.Context, r Receipt) (*Attestation, error) {
	idx := s.calls
	if idx >= len(s.results) {
		idx = len(s.results) - 1
	}
	res := s.results[idx]
	s.calls++
	if res.err != nil {
		return nil, res.err
	}
	if res.att != nil {
		return res.att, nil
	}
	return &Attestation{Algorithm: AttestationAlgorithm}, nil
}

func (s *stubAttester) Address() common.Address { return s.addr }

// newTestThrottle builds a ThrottledAttester with a deterministic
// clock: now is driven by a manual counter, sleep advances it, and
// jitter is fixed at 0 so backoff is exactly the capped exponential.
// Returns the throttle and a pointer to the slice of recorded sleeps.
func newTestThrottle(inner Attester, opts ...ThrottleOption) (*ThrottledAttester, *[]time.Duration) {
	t := NewThrottledAttester(inner, opts...)
	var clock time.Time = time.Unix(0, 0)
	sleeps := &[]time.Duration{}
	t.now = func() time.Time { return clock }
	t.sleep = func(ctx context.Context, d time.Duration) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		*sleeps = append(*sleeps, d)
		clock = clock.Add(d)
		return nil
	}
	t.jitter = func() float64 { return 0 }
	return t, sleeps
}

func TestThrottledAttester_TransientRetriedThenSuccess(t *testing.T) {
	t.Parallel()
	inner := &stubAttester{results: []stubResult{
		{err: errors.New("429 over rate limit")},
		{err: errors.New("replacement transaction underpriced")},
		{att: &Attestation{Algorithm: AttestationAlgorithm, AnchorTxHash: "0xabc"}},
	}}
	thr, sleeps := newTestThrottle(inner, WithThrottleInterval(0))

	att, err := thr.Attest(context.Background(), Receipt{TraceID: "t1"})
	if err != nil {
		t.Fatalf("Attest: %v", err)
	}
	if att.AnchorTxHash != "0xabc" {
		t.Fatalf("unexpected attestation: %+v", att)
	}
	if inner.calls != 3 {
		t.Fatalf("want 3 inner calls, got %d", inner.calls)
	}
	// Two backoff sleeps before the two retries: base (1s) and 2x base (2s),
	// jitter fixed at 0 → 0.5x each.
	if len(*sleeps) != 2 {
		t.Fatalf("want 2 backoff sleeps, got %d: %v", len(*sleeps), *sleeps)
	}
	if (*sleeps)[0] != 500*time.Millisecond {
		t.Fatalf("first backoff: want 500ms, got %v", (*sleeps)[0])
	}
	if (*sleeps)[1] != 1*time.Second {
		t.Fatalf("second backoff: want 1s, got %v", (*sleeps)[1])
	}
}

func TestThrottledAttester_NonTransientReturnsImmediately(t *testing.T) {
	t.Parallel()
	inner := &stubAttester{results: []stubResult{
		{err: errors.New("execution reverted: bad root")},
	}}
	thr, sleeps := newTestThrottle(inner, WithThrottleInterval(0))

	_, err := thr.Attest(context.Background(), Receipt{TraceID: "t1"})
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if inner.calls != 1 {
		t.Fatalf("want 1 inner call (no retry), got %d", inner.calls)
	}
	if len(*sleeps) != 0 {
		t.Fatalf("want no backoff sleeps, got %v", *sleeps)
	}
}

func TestThrottledAttester_MaxRetriesExhausted(t *testing.T) {
	t.Parallel()
	inner := &stubAttester{results: []stubResult{
		{err: errors.New("429 too many requests")},
	}}
	thr, sleeps := newTestThrottle(inner,
		WithThrottleInterval(0),
		WithThrottleRetry(2, time.Second, 30*time.Second),
	)

	_, err := thr.Attest(context.Background(), Receipt{TraceID: "t1"})
	if err == nil {
		t.Fatal("want error after exhausting retries, got nil")
	}
	// 1 initial + 2 retries = 3 calls.
	if inner.calls != 3 {
		t.Fatalf("want 3 inner calls, got %d", inner.calls)
	}
	if len(*sleeps) != 2 {
		t.Fatalf("want 2 backoff sleeps, got %d", len(*sleeps))
	}
}

func TestThrottledAttester_PacingEnforced(t *testing.T) {
	t.Parallel()
	inner := &stubAttester{results: []stubResult{{}}} // always succeed
	thr, sleeps := newTestThrottle(inner, WithThrottleInterval(750*time.Millisecond))

	ctx := context.Background()
	// First submit: no prior submit, so no pacing sleep.
	if _, err := thr.Attest(ctx, Receipt{TraceID: "t1"}); err != nil {
		t.Fatalf("first attest: %v", err)
	}
	if len(*sleeps) != 0 {
		t.Fatalf("first submit should not pace, got %v", *sleeps)
	}
	// Second submit immediately after: must wait the full interval
	// (the deterministic clock did not advance between submits).
	if _, err := thr.Attest(ctx, Receipt{TraceID: "t2"}); err != nil {
		t.Fatalf("second attest: %v", err)
	}
	if len(*sleeps) != 1 || (*sleeps)[0] != 750*time.Millisecond {
		t.Fatalf("want one 750ms pacing sleep, got %v", *sleeps)
	}
}

func TestThrottledAttester_PacingEnforcedOnRetry(t *testing.T) {
	t.Parallel()
	// First attempt fails transiently, second succeeds. Backoff base
	// is small (100ms) so the backoff alone is shorter than the
	// 750ms min interval; pacing must top up the remainder before the
	// retry submit fires.
	inner := &stubAttester{results: []stubResult{
		{err: errors.New("429 rate limit")},
		{att: &Attestation{Algorithm: AttestationAlgorithm, AnchorTxHash: "0xok"}},
	}}
	thr, sleeps := newTestThrottle(inner,
		WithThrottleInterval(750*time.Millisecond),
		WithThrottleRetry(3, 100*time.Millisecond, 30*time.Second),
	)

	att, err := thr.Attest(context.Background(), Receipt{TraceID: "t1"})
	if err != nil {
		t.Fatalf("Attest: %v", err)
	}
	if att.AnchorTxHash != "0xok" {
		t.Fatalf("unexpected attestation: %+v", att)
	}
	if inner.calls != 2 {
		t.Fatalf("want 2 inner calls, got %d", inner.calls)
	}
	// Two sleeps before the retry submit: the backoff (50ms = 100ms
	// base * 0.5 jitter floor) then the pacing top-up (700ms) so the
	// total gap since the first submit reaches the 750ms interval.
	if len(*sleeps) != 2 {
		t.Fatalf("want backoff + pacing sleep, got %d: %v", len(*sleeps), *sleeps)
	}
	if (*sleeps)[0] != 50*time.Millisecond {
		t.Fatalf("backoff sleep: want 50ms, got %v", (*sleeps)[0])
	}
	if (*sleeps)[1] != 700*time.Millisecond {
		t.Fatalf("pacing top-up: want 700ms, got %v", (*sleeps)[1])
	}
	// The full gap between the two submits is the sum, which must be
	// at least the min interval.
	var total time.Duration
	for _, s := range *sleeps {
		total += s
	}
	if total < 750*time.Millisecond {
		t.Fatalf("retry submit fired sooner than min interval: gap=%v", total)
	}
}

func TestThrottledAttester_ContextCancelDuringBackoff(t *testing.T) {
	t.Parallel()
	inner := &stubAttester{results: []stubResult{
		{err: errors.New("429 rate limit")},
	}}
	thr := NewThrottledAttester(inner, WithThrottleInterval(0))
	// Real-ish sleep that observes ctx; cancel before it fires.
	ctx, cancel := context.WithCancel(context.Background())
	thr.sleep = func(ctx context.Context, d time.Duration) error {
		cancel()
		return ctx.Err()
	}
	thr.jitter = func() float64 { return 0 }

	_, err := thr.Attest(ctx, Receipt{TraceID: "t1"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
}

func TestIsTransient(t *testing.T) {
	t.Parallel()
	transient := []string{
		"429 over rate limit",
		"rate limit exceeded",
		"too many requests",
		"replacement transaction underpriced",
		"transaction underpriced",
		"nonce too low",
		"i/o timeout",
		"context deadline exceeded",
		"connection reset by peer",
		"connection refused",
		"503 Service Unavailable",
		"502 Bad Gateway",
		"unexpected EOF",
	}
	for _, msg := range transient {
		if !isTransient(errors.New(msg)) {
			t.Errorf("isTransient(%q) = false, want true", msg)
		}
	}
	notTransient := []string{
		"execution reverted",
		"invalid opcode",
		"insufficient funds for gas",
		"receipt not receiptable",
	}
	for _, msg := range notTransient {
		if isTransient(errors.New(msg)) {
			t.Errorf("isTransient(%q) = true, want false", msg)
		}
	}
	if isTransient(nil) {
		t.Error("isTransient(nil) = true, want false")
	}
}

func TestThrottledAttester_AddressPassthrough(t *testing.T) {
	t.Parallel()
	addr := common.HexToAddress("0x1234567890abcdef1234567890abcdef12345678")
	inner := &stubAttester{addr: addr, results: []stubResult{{}}}
	thr := NewThrottledAttester(inner)
	if thr.Address() != addr {
		t.Fatalf("Address passthrough: got %s, want %s", thr.Address(), addr)
	}
}
