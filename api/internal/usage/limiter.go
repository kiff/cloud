package usage

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// PlanResolver looks up a tenant's effective plan limits. The
// proposal handler holds one and asks "is this tenant within
// their monthly proposal budget?" before incrementing counters.
//
// Implementations:
//
//   - tenant.Store-backed (production) reads tenant.Metadata via
//     the existing store.Get + tenant.PlanFor.
//   - test stubs return a pinned PlanLimits.
//
// The interface lives in usage rather than in tenant because the
// limiter is a usage concern; the tenant package owns the plan
// shape and stays free of any "current period count" logic.
type PlanResolver interface {
	// PlanProposalLimit returns the monthly proposal budget for
	// the tenant. A return of 0 (or negative) signals unlimited;
	// the limiter treats that case as a no-op. hardCap reports
	// what crossing the limit means: true (free Developer) rejects
	// the proposal with limit_exceeded; false (paid plans) treats
	// the limit as an included allowance and lets the proposal
	// through as metered overage rather than a runtime outage.
	PlanProposalLimit(ctx context.Context, tenantID string) (limit int, hardCap bool, err error)
}

// LimiterFlusher is the same shape UsageHandler uses. A nil
// flusher disables in-memory drain before reads — fine for
// tests, wrong for production where uncommitted increments must
// be visible to the limit check (otherwise the very proposal
// being counted gets a free pass).
type LimiterFlusher interface {
	FlushNow(ctx context.Context) error
}

// ProposalLimiter enforces the monthly governed-proposal cap.
// Built once per process; thread-safe (the underlying Store and
// PlanResolver implementations are concurrent-safe).
//
// Read path:
//
//	limiter.AllowProposal(ctx, tenantID)
//	  -> resolves the tenant's plan
//	  -> if unlimited: returns Allow{ok:true}
//	  -> flushes the aggregator
//	  -> sums kiff.cloud.proposals.submitted in the current
//	     UTC calendar month
//	  -> compares to the plan's monthly limit
//	  -> within limit: returns Allow{ok:true}
//	  -> over limit, free plan: returns Allow{ok:false}
//	  -> over limit, paid plan: returns Allow{ok:true, overage:true}
//
// The handler rejects with a stable "limit_exceeded" outcome only
// when ok=false (a free hard cap). A paid over-limit proposal is
// allowed through and its overage recorded for billing. The two-step (plan -> usage) keeps the limit shape
// decoupled from the count source: future plan knobs (eg
// per-domain limits) plug in here without changing the proposal
// gate.
type ProposalLimiter struct {
	store   Store
	plans   PlanResolver
	flusher LimiterFlusher
	now     func() time.Time
}

// NewProposalLimiter wires the dependencies. plans must be
// non-nil (the limiter has nothing to compare against without a
// plan source). store must be non-nil (no count source).
// flusher is optional; production wires the same Aggregator the
// usage handler uses so an in-flight counter is visible.
func NewProposalLimiter(store Store, plans PlanResolver, opts ...LimiterOption) (*ProposalLimiter, error) {
	if store == nil {
		return nil, errors.New("usage: limiter requires a Store")
	}
	if plans == nil {
		return nil, errors.New("usage: limiter requires a PlanResolver")
	}
	l := &ProposalLimiter{
		store: store,
		plans: plans,
		now:   func() time.Time { return time.Now().UTC() },
	}
	for _, opt := range opts {
		opt(l)
	}
	return l, nil
}

// LimiterOption configures a ProposalLimiter.
type LimiterOption func(*ProposalLimiter)

// WithLimiterFlusher wires the in-memory aggregator drain so the
// count under the limit reflects this request and concurrent
// requests in the same flush window. Without it, the limit can
// be exceeded by a 30-second margin.
func WithLimiterFlusher(f LimiterFlusher) LimiterOption {
	return func(l *ProposalLimiter) {
		if f != nil {
			l.flusher = f
		}
	}
}

// WithLimiterClock substitutes the clock for tests that need
// reproducible period boundaries.
func WithLimiterClock(now func() time.Time) LimiterOption {
	return func(l *ProposalLimiter) {
		if now != nil {
			l.now = now
		}
	}
}

// AllowProposal reports whether the next proposal would fit
// under the tenant's plan. Returns ok=true for unlimited plans
// without touching the count source. For finite plans, the
// limiter flushes the aggregator (so the count includes
// in-flight increments from this request and any peer requests
// in the same flush window) and reads the current UTC calendar
// month's count.
//
// The measure depends on the plan kind: a **metered (paid)** plan's included allowance is the
// billed unit, so it counts kiff.cloud.proposals.allowed only —
// blocked / approval_required / invalid don't accrue overage, matching
// the invoice. A **hard-capped (free)** plan's cap is the tenant's
// only volume/abuse ceiling on the decide endpoint, so it stays
// outcome-independent and counts kiff.cloud.proposals.submitted (all
// evaluated proposals).
//
// When usage has crossed the limit, the return depends on the
// plan kind:
//
//   - hard-capped (free Developer): allowed=false. The handler
//     rejects with a disclosed "limit_exceeded" ceiling.
//   - metered (paid plans): allowed=true, overage=true. The limit
//     is an included allowance, not a wall — the proposal is still
//     evaluated and the handler records the overage for billing.
//     A paid commercial allowance must never become an unplanned
//     production outage on a consequential path.
//
// A plan-resolver error or a store error returns ok=true with
// the error attached: the limiter is allowed-when-uncertain by
// design — surfacing a "limit_exceeded" because Postgres blipped
// would be a worse failure mode than a brief over-count, and a
// billing-subsystem failure must never change an otherwise valid
// runtime decision. The caller logs the error.
//
// Return shape matches handlers.Limiter so the proposal handler
// can consume the limiter directly without a translation layer.
func (l *ProposalLimiter) AllowProposal(ctx context.Context, tenantID string) (allowed bool, used uint64, limit int, overage bool, err error) {
	limit, hardCap, perr := l.plans.PlanProposalLimit(ctx, tenantID)
	if perr != nil {
		return true, 0, 0, false, fmt.Errorf("plan resolve: %w", perr)
	}
	if limit <= 0 {
		return true, 0, limit, false, nil
	}

	if l.flusher != nil {
		if ferr := l.flusher.FlushNow(ctx); ferr != nil {
			// Same contract as UsageHandler: a flush failure must
			// not turn the limiter into a 5xx. The Snapshot below
			// runs on the last persisted state.
			return true, 0, limit, false, fmt.Errorf("limiter flush: %w", ferr)
		}
	}

	// Which measure bounds the plan depends on the plan kind:
	//   - metered (paid): the included allowance is the *billed* unit,
	//     so it counts kiff.cloud.proposals.allowed only — refused or
	//     adversarial calls don't accrue overage, matching the invoice.
	//   - hard-capped (free): this cap is the tenant's ONLY volume/abuse
	//     ceiling on the decide endpoint (there is no separate per-window
	//     request limiter), so it stays outcome-independent and counts
	//     kiff.cloud.proposals.submitted (all evaluated proposals).
	//     Counting allowed-only here would let a free tenant send
	//     unbounded blocked/approval_required/invalid proposals — each a
	//     full domain+state evaluation — with nothing to bound it.
	counterName := CloudCounterProposalsAllowed
	if hardCap {
		counterName = CloudCounterProposalsSubmitted
	}
	from := startOfMonth(l.now())
	rows, serr := l.store.Snapshot(ctx, Filter{
		TenantID:    tenantID,
		CounterName: counterName,
		HourFrom:    from,
	})
	if serr != nil {
		return true, 0, limit, false, fmt.Errorf("limiter snapshot: %w", serr)
	}
	for _, r := range rows {
		used += r.Count
	}
	if int(used) < limit {
		return true, used, limit, false, nil
	}
	// Over the included allowance. A hard-capped (free) plan is
	// rejected; a metered (paid) plan is allowed and the caller
	// records the overage for billing.
	if hardCap {
		return false, used, limit, false, nil
	}
	return true, used, limit, true, nil
}

// startOfMonth truncates t to the first instant of its UTC month.
// Same convention the /v1/me/usage handler's default period uses,
// so a tenant looking at their dashboard sees the same window
// the limiter compares against.
func startOfMonth(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
}
