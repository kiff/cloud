package receipt

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand"
	"strings"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
)

// ThrottledAttester wraps an Attester and makes on-chain submission
// safe under public-RPC pressure. It does two things the bare
// attesters do not:
//
//   - Paces submits. At most one Attest runs at a time, and
//     consecutive submits are spaced by at least MinInterval. The
//     anchor sweep (Pipeline.AttestPending) otherwise fires one
//     transaction per unanchored receipt back-to-back, which a public
//     endpoint like sepolia.base.org answers with HTTP 429.
//
//   - Retries transient failures with exponential backoff + jitter.
//     "429 over rate limit", "replacement transaction underpriced",
//     and transport blips are recoverable: the same submit succeeds a
//     moment later. Non-transient errors (a reverted transaction, a
//     malformed receipt) return immediately — retrying them only
//     wastes time and gas.
//
// Pacing is process-local: it bounds the rate this binary submits at,
// not the global rate across replicas. A single anchor binary is the
// current production shape (one prod box, one anchor loop), so a
// local pacer is sufficient. If the anchor loop ever runs on multiple
// replicas, the pacing budget moves to a shared limiter; the decorator
// seam stays the same.
//
// ThrottledAttester is the outer layer: wrap the Sepolia and mainnet
// AnchorAttesters with it before handing them to the RoutingAttester
// and the pipeline default. The Submitter's own nonce management
// (anchor.Submitter) handles sequential nonces; the pacing here keeps
// the RPC from rejecting the burst before the nonce sequence matters.
type ThrottledAttester struct {
	inner Attester

	minInterval time.Duration
	maxRetries  int
	baseBackoff time.Duration
	maxBackoff  time.Duration

	logger *slog.Logger

	// Injectable for deterministic tests. now reports the current
	// time, sleep blocks for a duration (honoring ctx), and jitter
	// returns a value in [0,1) used to spread backoff.
	now    func() time.Time
	sleep  func(ctx context.Context, d time.Duration) error
	jitter func() float64

	mu         sync.Mutex
	lastSubmit time.Time
}

// Throttle defaults. Tuned for a single anchor loop against a public
// Base RPC endpoint: ~1 submit/sec steady state, backoff capped so a
// throttled sweep recovers within a sweep interval rather than
// stalling indefinitely.
const (
	defaultThrottleMinInterval = 750 * time.Millisecond
	defaultThrottleMaxRetries  = 5
	defaultThrottleBaseBackoff = 1 * time.Second
	defaultThrottleMaxBackoff  = 30 * time.Second
)

// ThrottleOption configures a ThrottledAttester.
type ThrottleOption func(*ThrottledAttester)

// WithThrottleInterval sets the minimum spacing between submits. A
// non-positive value disables pacing (retry/backoff still applies).
func WithThrottleInterval(d time.Duration) ThrottleOption {
	return func(t *ThrottledAttester) { t.minInterval = d }
}

// WithThrottleRetry sets the retry budget and backoff envelope.
// maxRetries < 0 is clamped to 0 (no retries). base/max <= 0 fall
// back to the defaults.
func WithThrottleRetry(maxRetries int, base, max time.Duration) ThrottleOption {
	return func(t *ThrottledAttester) {
		if maxRetries < 0 {
			maxRetries = 0
		}
		t.maxRetries = maxRetries
		if base > 0 {
			t.baseBackoff = base
		}
		if max > 0 {
			t.maxBackoff = max
		}
	}
}

// WithThrottleLogger sets the logger used for retry/backoff lines.
// nil discards.
func WithThrottleLogger(l *slog.Logger) ThrottleOption {
	return func(t *ThrottledAttester) {
		if l != nil {
			t.logger = l
		}
	}
}

// NewThrottledAttester wraps inner with pacing and transient-retry.
// inner is required. Sane defaults apply when no options are passed.
func NewThrottledAttester(inner Attester, opts ...ThrottleOption) *ThrottledAttester {
	t := &ThrottledAttester{
		inner:       inner,
		minInterval: defaultThrottleMinInterval,
		maxRetries:  defaultThrottleMaxRetries,
		baseBackoff: defaultThrottleBaseBackoff,
		maxBackoff:  defaultThrottleMaxBackoff,
		logger:      slog.New(discardHandler{}),
		now:         time.Now,
		sleep:       sleepCtx,
		jitter:      rand.Float64,
	}
	for _, opt := range opts {
		opt(t)
	}
	return t
}

// Address implements Attester. Passthrough to the wrapped attester so
// verifiers recover the same signer.
func (t *ThrottledAttester) Address() common.Address { return t.inner.Address() }

// Attest implements Attester. Serializes against other in-flight
// Attest calls, paces by MinInterval, and retries transient failures
// with exponential backoff + jitter. Returns the wrapped attester's
// Attestation on success, the original error on a non-transient
// failure, or a wrapped error once the retry budget is exhausted.
func (t *ThrottledAttester) Attest(ctx context.Context, r Receipt) (*Attestation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	// One submit at a time. Serializing both paces the RPC and keeps
	// the on-chain nonce sequence ordered alongside the Submitter's
	// own nonce counter.
	t.mu.Lock()
	defer t.mu.Unlock()

	var lastErr error
	for attempt := 0; attempt <= t.maxRetries; attempt++ {
		if attempt > 0 {
			backoff := t.backoffFor(attempt)
			t.logger.WarnContext(ctx, "receipt: anchor submit transient failure; backing off",
				slog.Int("attempt", attempt),
				slog.Int("max_retries", t.maxRetries),
				slog.String("trace_id", r.TraceID),
				slog.Duration("backoff", backoff),
				slog.String("err", lastErr.Error()),
			)
			if err := t.sleep(ctx, backoff); err != nil {
				return nil, err
			}
		}

		// Pace before every submit, including retries: the gap since
		// the previous submit must be at least MinInterval. The
		// backoff above may already cover part or all of it; this tops
		// up the remainder. Enforcing it here (not just before the
		// first attempt) keeps the "consecutive submits are spaced by
		// MinInterval" guarantee true even when a short backoff would
		// otherwise let a retry fire sooner.
		if err := t.pace(ctx); err != nil {
			return nil, err
		}

		att, err := t.inner.Attest(ctx, r)
		// Every call to inner is an RPC submit; pace from now whether
		// it succeeded or failed.
		t.lastSubmit = t.now()
		if err == nil {
			return att, nil
		}
		lastErr = err
		if !isTransient(err) {
			return nil, err
		}
		if cerr := ctx.Err(); cerr != nil {
			return nil, cerr
		}
	}
	return nil, fmt.Errorf("receipt: anchor submit failed after %d retries: %w", t.maxRetries, lastErr)
}

// pace blocks until at least MinInterval has elapsed since the last
// submit. A non-positive MinInterval or a zero lastSubmit (the very
// first submit) is a no-op.
func (t *ThrottledAttester) pace(ctx context.Context) error {
	if t.minInterval <= 0 || t.lastSubmit.IsZero() {
		return nil
	}
	if wait := t.minInterval - t.now().Sub(t.lastSubmit); wait > 0 {
		return t.sleep(ctx, wait)
	}
	return nil
}

// backoffFor returns the sleep before the given attempt (attempt >=
// 1). Exponential on baseBackoff, capped at maxBackoff, with full
// jitter in [0.5, 1.0]x to spread retries across a throttled fleet.
func (t *ThrottledAttester) backoffFor(attempt int) time.Duration {
	backoff := t.baseBackoff
	for i := 1; i < attempt; i++ {
		backoff *= 2
		if backoff >= t.maxBackoff {
			backoff = t.maxBackoff
			break
		}
	}
	if backoff > t.maxBackoff {
		backoff = t.maxBackoff
	}
	frac := 0.5 + 0.5*t.jitter()
	return time.Duration(float64(backoff) * frac)
}

// isTransient reports whether err is the kind of failure a retry can
// clear: public-RPC throttling, the nonce-collision "underpriced"
// signature, and transport blips. Anything else (a reverted
// transaction, a malformed receipt, a context error) is returned to
// the caller unretried.
//
// Matching is substring-based on the lowercased message because
// go-ethereum surfaces RPC errors as opaque strings from the remote
// node; there is no typed error to switch on across the JSON-RPC
// boundary.
func isTransient(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, sub := range transientSubstrings {
		if strings.Contains(msg, sub) {
			return true
		}
	}
	return false
}

// transientSubstrings is the match set for isTransient. Kept narrow
// on purpose: a false positive retries a genuinely-failed submit
// (wasting a sweep), a false negative drops a recoverable receipt
// back to the next sweep (still anchored eventually, just slower).
var transientSubstrings = []string{
	"429",
	"rate limit",
	"too many requests",
	"replacement transaction underpriced",
	"transaction underpriced",
	"underpriced",
	"nonce too low",
	"already known",
	"timeout",
	"timed out",
	"deadline exceeded",
	"connection reset",
	"connection refused",
	"temporarily unavailable",
	"service unavailable",
	"502",
	"503",
	"504",
	"eof",
}

// sleepCtx blocks for d or until ctx is cancelled, whichever comes
// first. The default sleep for ThrottledAttester.
func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// Compile-time interface assertion.
var _ Attester = (*ThrottledAttester)(nil)
