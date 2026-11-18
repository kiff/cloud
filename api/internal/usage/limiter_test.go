package usage_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/usage"
)

// stubResolver returns a fixed limit per tenant id. Tests use it
// to drive the limiter without a tenant.Store. hardCaps marks a
// tenant's plan as a free hard cap (reject over-limit) vs a paid
// metered allowance (allow over-limit as overage); the zero value
// is paid/metered.
type stubResolver struct {
	limits   map[string]int
	hardCaps map[string]bool
	err      error
}

func (s *stubResolver) PlanProposalLimit(_ context.Context, tenantID string) (int, bool, error) {
	if s.err != nil {
		return 0, false, s.err
	}
	return s.limits[tenantID], s.hardCaps[tenantID], nil
}

// recordingFlusher counts FlushNow invocations so the limiter
// tests can verify the in-memory aggregator is drained before
// the count read.
type recordingFlusher struct {
	calls int
	err   error
}

func (f *recordingFlusher) FlushNow(_ context.Context) error {
	f.calls++
	return f.err
}

// seedAllowed seeds the kiff.cloud.proposals.allowed counter — the
// measure a metered (paid) plan's allowance/overage is compared
// against (the billed unit).
func seedAllowed(t *testing.T, store *usage.MemoryStore, tenantID string, count uint64, when time.Time) {
	t.Helper()
	if err := store.Flush(context.Background(), []usage.Counter{{
		TenantID:    tenantID,
		CounterName: usage.CloudCounterProposalsAllowed,
		EntityType:  usage.CloudEntityProposal,
		HourBucket:  usage.HourBucket(when),
		Count:       count,
	}}); err != nil {
		t.Fatalf("seed: %v", err)
	}
}

// seedSubmitted seeds the kiff.cloud.proposals.submitted counter — the
// outcome-independent measure a hard-capped (free) plan's volume
// ceiling is compared against (all evaluated proposals: allowed +
// blocked + approval_required + invalid).
func seedSubmitted(t *testing.T, store *usage.MemoryStore, tenantID string, count uint64, when time.Time) {
	t.Helper()
	if err := store.Flush(context.Background(), []usage.Counter{{
		TenantID:    tenantID,
		CounterName: usage.CloudCounterProposalsSubmitted,
		EntityType:  usage.CloudEntityProposal,
		HourBucket:  usage.HourBucket(when),
		Count:       count,
	}}); err != nil {
		t.Fatalf("seed: %v", err)
	}
}

func TestLimiter_AllowsUnderLimit(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 5, 15, 14, 0, 0, 0, time.UTC)
	store := usage.NewMemoryStore()
	tenantID := "01tenant001"
	resolver := &stubResolver{limits: map[string]int{tenantID: 100}}
	limiter, err := usage.NewProposalLimiter(store, resolver,
		usage.WithLimiterClock(func() time.Time { return now }),
	)
	if err != nil {
		t.Fatalf("NewProposalLimiter: %v", err)
	}
	seedAllowed(t, store, tenantID, 50, now)

	allowed, used, limit, overage, err := limiter.AllowProposal(context.Background(), tenantID)
	if err != nil {
		t.Fatalf("AllowProposal: %v", err)
	}
	if !allowed {
		t.Errorf("expected allowed=true at 50/100")
	}
	if overage {
		t.Errorf("expected overage=false under limit")
	}
	if used != 50 || limit != 100 {
		t.Errorf("used/limit = %d/%d, want 50/100", used, limit)
	}
}

func TestLimiter_FreeHardCapBlocksAtLimit(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 5, 15, 14, 0, 0, 0, time.UTC)
	store := usage.NewMemoryStore()
	tenantID := "01tenant001"
	resolver := &stubResolver{
		limits:   map[string]int{tenantID: 100},
		hardCaps: map[string]bool{tenantID: true},
	}
	limiter, err := usage.NewProposalLimiter(store, resolver,
		usage.WithLimiterClock(func() time.Time { return now }),
	)
	if err != nil {
		t.Fatalf("NewProposalLimiter: %v", err)
	}
	// At-limit must reject the next request on a free hard cap:
	// used=limit means the disclosed free ceiling is consumed. The
	// free cap counts submitted (all outcomes), so this bound holds
	// regardless of how many were allowed vs blocked.
	seedSubmitted(t, store, tenantID, 100, now)

	allowed, used, _, overage, err := limiter.AllowProposal(context.Background(), tenantID)
	if err != nil {
		t.Fatalf("AllowProposal: %v", err)
	}
	if allowed {
		t.Errorf("expected allowed=false at 100/100 on a free hard cap")
	}
	if overage {
		t.Errorf("a free hard cap never reports overage")
	}
	if used != 100 {
		t.Errorf("used = %d, want 100", used)
	}
}

// TestLimiter_PaidPlanOverageAllowsAtLimit is the core case: a paid
// tenant past its included allowance is NOT hard-stopped. The
// limiter allows the proposal and flags it as overage so the
// handler records it for billing instead of returning an outage.
func TestLimiter_PaidPlanOverageAllowsAtLimit(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 5, 15, 14, 0, 0, 0, time.UTC)
	store := usage.NewMemoryStore()
	tenantID := "01tenantPaid"
	resolver := &stubResolver{
		limits:   map[string]int{tenantID: 100},
		hardCaps: map[string]bool{tenantID: false}, // paid / metered
	}
	limiter, _ := usage.NewProposalLimiter(store, resolver,
		usage.WithLimiterClock(func() time.Time { return now }),
	)
	// Well past the allowance — a free plan would hard-stop here.
	seedAllowed(t, store, tenantID, 150, now)

	allowed, used, limit, overage, err := limiter.AllowProposal(context.Background(), tenantID)
	if err != nil {
		t.Fatalf("AllowProposal: %v", err)
	}
	if !allowed {
		t.Errorf("paid plan must not hard-stop past its allowance")
	}
	if !overage {
		t.Errorf("paid over-allowance must report overage=true for billing")
	}
	if used != 150 || limit != 100 {
		t.Errorf("used/limit = %d/%d, want 150/100", used, limit)
	}
}

// TestLimiter_FreeCapCountsRefusedOutcomes pins a review fix:
// the free hard cap is the decide endpoint's only volume/abuse bound,
// so it must count refused outcomes too. Here the tenant has zero
// `allowed` but 100 `submitted` (all blocked/approval/invalid) at a
// limit of 100 — it must still hard-stop, or a free tenant could send
// unbounded refused proposals (each a full evaluation) unbilled and
// unbounded.
func TestLimiter_FreeCapCountsRefusedOutcomes(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 5, 15, 14, 0, 0, 0, time.UTC)
	store := usage.NewMemoryStore()
	tenantID := "01tenantFreeAbuse"
	resolver := &stubResolver{
		limits:   map[string]int{tenantID: 100},
		hardCaps: map[string]bool{tenantID: true},
	}
	limiter, _ := usage.NewProposalLimiter(store, resolver,
		usage.WithLimiterClock(func() time.Time { return now }),
	)
	// 100 submitted, 0 allowed — i.e. every proposal was refused.
	seedSubmitted(t, store, tenantID, 100, now)
	seedAllowed(t, store, tenantID, 0, now)

	allowed, used, _, _, err := limiter.AllowProposal(context.Background(), tenantID)
	if err != nil {
		t.Fatalf("AllowProposal: %v", err)
	}
	if allowed {
		t.Errorf("free cap must count refused outcomes (submitted); got allowed=true at 100 refused/100")
	}
	if used != 100 {
		t.Errorf("used = %d, want 100 (submitted, outcome-independent)", used)
	}
}

// TestLimiter_PaidOverageIgnoresRefusedOutcomes is the paid mirror:
// the metered allowance is the billed unit, so refused calls don't
// accrue overage. 500 submitted but only 50 allowed against a 100
// allowance → no overage, allowed, used reflects `allowed` only.
func TestLimiter_PaidOverageIgnoresRefusedOutcomes(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 5, 15, 14, 0, 0, 0, time.UTC)
	store := usage.NewMemoryStore()
	tenantID := "01tenantPaidProbed"
	resolver := &stubResolver{
		limits:   map[string]int{tenantID: 100},
		hardCaps: map[string]bool{tenantID: false}, // paid / metered
	}
	limiter, _ := usage.NewProposalLimiter(store, resolver,
		usage.WithLimiterClock(func() time.Time { return now }),
	)
	// Heavily probed: 500 refused, only 50 productive.
	seedSubmitted(t, store, tenantID, 500, now)
	seedAllowed(t, store, tenantID, 50, now)

	allowed, used, limit, overage, err := limiter.AllowProposal(context.Background(), tenantID)
	if err != nil {
		t.Fatalf("AllowProposal: %v", err)
	}
	if !allowed || overage {
		t.Errorf("paid: refused calls must not accrue overage; got allowed=%v overage=%v", allowed, overage)
	}
	if used != 50 || limit != 100 {
		t.Errorf("used/limit = %d/%d, want 50/100 (allowed only)", used, limit)
	}
}

func TestLimiter_UnlimitedPlanNeverBlocks(t *testing.T) {
	t.Parallel()
	store := usage.NewMemoryStore()
	tenantID := "01tenant001"
	// Limit 0 = unlimited. Pre-seed a huge count to confirm it
	// is ignored.
	resolver := &stubResolver{limits: map[string]int{tenantID: 0}}
	limiter, _ := usage.NewProposalLimiter(store, resolver)
	seedAllowed(t, store, tenantID, 9_999_999, time.Now())

	allowed, used, _, overage, err := limiter.AllowProposal(context.Background(), tenantID)
	if err != nil {
		t.Fatalf("AllowProposal: %v", err)
	}
	if !allowed {
		t.Errorf("unlimited plan must always allow")
	}
	if overage {
		t.Errorf("unlimited plan never reports overage")
	}
	if used != 0 {
		t.Errorf("unlimited plan should skip count read; used = %d", used)
	}
}

func TestLimiter_TenantScoped(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 5, 15, 14, 0, 0, 0, time.UTC)
	store := usage.NewMemoryStore()
	tA, tB := "01tenantA", "01tenantB"
	resolver := &stubResolver{
		limits:   map[string]int{tA: 100, tB: 100},
		hardCaps: map[string]bool{tA: true, tB: true},
	}
	limiter, _ := usage.NewProposalLimiter(store, resolver,
		usage.WithLimiterClock(func() time.Time { return now }),
	)
	// Tenant B is over its limit; tenant A is at zero. The
	// limiter must scope the count read so A is not affected.
	seedSubmitted(t, store, tB, 200, now)

	allowedA, _, _, _, _ := limiter.AllowProposal(context.Background(), tA)
	allowedB, _, _, _, _ := limiter.AllowProposal(context.Background(), tB)
	if !allowedA {
		t.Errorf("tenant A should be allowed; usage from tenant B leaked across")
	}
	if allowedB {
		t.Errorf("tenant B should be blocked")
	}
}

func TestLimiter_FlushesBeforeReadingCount(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 5, 15, 14, 0, 0, 0, time.UTC)
	store := usage.NewMemoryStore()
	tenantID := "01tenantF"
	resolver := &stubResolver{limits: map[string]int{tenantID: 100}}
	flusher := &recordingFlusher{}
	limiter, _ := usage.NewProposalLimiter(store, resolver,
		usage.WithLimiterClock(func() time.Time { return now }),
		usage.WithLimiterFlusher(flusher),
	)
	if _, _, _, _, err := limiter.AllowProposal(context.Background(), tenantID); err != nil {
		t.Fatalf("AllowProposal: %v", err)
	}
	if flusher.calls != 1 {
		t.Errorf("FlushNow calls = %d, want 1 (limiter must drain in-memory aggregator)", flusher.calls)
	}
}

// Invariant: When the usage limiter cannot establish its own state, the
// request is allowed. Blocking a customer's production traffic because
// our meter was uncertain is the worse failure, and this default is
// deliberately the opposite of the governance one.
// Category: failure-modes
// Attack: The usage limiter cannot reach its own store while a customer is
// mid-request
// Refused: The request is allowed. Blocking production traffic because our
// meter was uncertain is the worse failure
func TestLimiter_FlushFailureIsAllowedWhenUncertain(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 5, 15, 14, 0, 0, 0, time.UTC)
	store := usage.NewMemoryStore()
	tenantID := "01tenantF"
	resolver := &stubResolver{limits: map[string]int{tenantID: 100}}
	flusher := &recordingFlusher{err: errors.New("simulated db blip")}
	limiter, _ := usage.NewProposalLimiter(store, resolver,
		usage.WithLimiterClock(func() time.Time { return now }),
		usage.WithLimiterFlusher(flusher),
	)
	allowed, _, _, _, err := limiter.AllowProposal(context.Background(), tenantID)
	if err == nil {
		t.Errorf("expected error to be reported alongside allow=true")
	}
	if !allowed {
		t.Errorf("flush failure must yield allowed=true (allow-when-uncertain)")
	}
}

func TestLimiter_PlanResolverFailureIsAllowedWhenUncertain(t *testing.T) {
	t.Parallel()
	store := usage.NewMemoryStore()
	resolver := &stubResolver{err: errors.New("simulated resolve failure")}
	limiter, _ := usage.NewProposalLimiter(store, resolver)
	allowed, _, _, _, err := limiter.AllowProposal(context.Background(), "01tenantX")
	if err == nil {
		t.Errorf("expected error reported")
	}
	if !allowed {
		t.Errorf("resolver failure must yield allowed=true")
	}
}

func TestLimiter_PreviousMonthDoesNotCountAgainstCurrent(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 5, 15, 14, 0, 0, 0, time.UTC)
	prev := time.Date(2026, 4, 20, 14, 0, 0, 0, time.UTC)
	store := usage.NewMemoryStore()
	tenantID := "01tenantP"
	resolver := &stubResolver{limits: map[string]int{tenantID: 100}}
	limiter, _ := usage.NewProposalLimiter(store, resolver,
		usage.WithLimiterClock(func() time.Time { return now }),
	)
	// 200 allowed in April should NOT bleed into May's budget.
	seedAllowed(t, store, tenantID, 200, prev)
	// 50 allowed in May.
	seedAllowed(t, store, tenantID, 50, now)

	allowed, used, _, _, err := limiter.AllowProposal(context.Background(), tenantID)
	if err != nil {
		t.Fatalf("AllowProposal: %v", err)
	}
	if !allowed {
		t.Errorf("expected allowed=true: only May counts (50/100)")
	}
	if used != 50 {
		t.Errorf("used = %d, want 50 (April rows must not count)", used)
	}
}
