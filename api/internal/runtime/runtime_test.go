package runtime

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	kiffruntime "github.com/kiff/kiff/pkg/kiff/runtime"
)

func TestForWithoutResolver(t *testing.T) {
	t.Parallel()

	rt := New(nil)
	if _, err := rt.For(context.Background(), "tenant-1"); !errors.Is(err, ErrNoResolver) {
		t.Fatalf("err: got %v, want %v", err, ErrNoResolver)
	}
}

// stubResolver counts the number of times Resolve is called and
// returns a unique *kiffruntime.Runtime each time so the cache test
// can assert single materialization.
type stubResolver struct {
	calls atomic.Int64
}

func (s *stubResolver) Resolve(_ context.Context, _ string, _ string) (Resolved, error) {
	s.calls.Add(1)
	return Resolved{Runtime: &kiffruntime.Runtime{}}, nil
}

func TestForCachesPerTenant(t *testing.T) {
	t.Parallel()

	r := &stubResolver{}
	tr := New(r)

	first, err := tr.For(context.Background(), "tenant-a")
	if err != nil {
		t.Fatalf("first call: %v", err)
	}
	second, err := tr.For(context.Background(), "tenant-a")
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	if first != second {
		t.Fatal("cached runtime mismatch: For returned a fresh runtime on the second call")
	}
	if got := r.calls.Load(); got != 1 {
		t.Fatalf("resolver calls: got %d, want 1", got)
	}
}

func TestResetTenantEvicts(t *testing.T) {
	t.Parallel()

	r := &stubResolver{}
	tr := New(r)

	if _, err := tr.For(context.Background(), "tenant-b"); err != nil {
		t.Fatalf("populate: %v", err)
	}
	tr.ResetTenant("tenant-b")
	if _, err := tr.For(context.Background(), "tenant-b"); err != nil {
		t.Fatalf("post-reset: %v", err)
	}
	if got := r.calls.Load(); got != 2 {
		t.Fatalf("resolver calls: got %d, want 2", got)
	}
}

// TestForDomainCachesPerDomain proves the (tenant_id, domain_id)
// cache rekey: two domains of the same tenant resolve
// independently and each caches, while a repeat of either is a hit.
func TestForDomainCachesPerDomain(t *testing.T) {
	t.Parallel()

	r := &stubResolver{}
	tr := New(r)
	ctx := context.Background()

	refundsA, err := tr.ForDomain(ctx, "tenant-c", "refunds")
	if err != nil {
		t.Fatalf("refunds first: %v", err)
	}
	payouts, err := tr.ForDomain(ctx, "tenant-c", "payouts")
	if err != nil {
		t.Fatalf("payouts: %v", err)
	}
	if refundsA == payouts {
		t.Fatal("distinct domains must resolve to distinct runtimes")
	}
	refundsB, err := tr.ForDomain(ctx, "tenant-c", "refunds")
	if err != nil {
		t.Fatalf("refunds second: %v", err)
	}
	if refundsA != refundsB {
		t.Fatal("same (tenant, domain) must hit the cache")
	}
	if got := r.calls.Load(); got != 2 {
		t.Fatalf("resolver calls: got %d, want 2 (one per domain)", got)
	}
}

// TestForIsDefaultDomain confirms For(tenant) is the default-domain
// shorthand: it shares a cache slot with ForDomain(tenant, "").
func TestForIsDefaultDomain(t *testing.T) {
	t.Parallel()

	r := &stubResolver{}
	tr := New(r)
	ctx := context.Background()

	viaFor, err := tr.For(ctx, "tenant-d")
	if err != nil {
		t.Fatalf("For: %v", err)
	}
	viaForDomain, err := tr.ForDomain(ctx, "tenant-d", "")
	if err != nil {
		t.Fatalf("ForDomain empty: %v", err)
	}
	if viaFor != viaForDomain {
		t.Fatal("For(tenant) must alias ForDomain(tenant, \"\")")
	}
	if got := r.calls.Load(); got != 1 {
		t.Fatalf("resolver calls: got %d, want 1 (shared default slot)", got)
	}
}

// TestResetTenantEvictsAllDomains confirms ResetTenant drops every
// domain runtime for the tenant, not just the default.
func TestResetTenantEvictsAllDomains(t *testing.T) {
	t.Parallel()

	r := &stubResolver{}
	tr := New(r)
	ctx := context.Background()

	if _, err := tr.ForDomain(ctx, "tenant-e", "refunds"); err != nil {
		t.Fatalf("seed refunds: %v", err)
	}
	if _, err := tr.ForDomain(ctx, "tenant-e", "payouts"); err != nil {
		t.Fatalf("seed payouts: %v", err)
	}
	tr.ResetTenant("tenant-e")
	if _, err := tr.ForDomain(ctx, "tenant-e", "refunds"); err != nil {
		t.Fatalf("post-reset refunds: %v", err)
	}
	if _, err := tr.ForDomain(ctx, "tenant-e", "payouts"); err != nil {
		t.Fatalf("post-reset payouts: %v", err)
	}
	// 2 initial + 2 after reset = 4 (reset evicted both domains).
	if got := r.calls.Load(); got != 4 {
		t.Fatalf("resolver calls: got %d, want 4", got)
	}
}

// domainResolver reports the domain it loaded: the default ("billing")
// for an empty selector, else the selector itself.
type domainResolver struct {
	calls atomic.Int64
}

func (d *domainResolver) Resolve(_ context.Context, _ string, domainID string) (Resolved, error) {
	d.calls.Add(1)
	if domainID == "" {
		domainID = "billing"
	}
	return Resolved{Runtime: &kiffruntime.Runtime{}, DomainID: domainID}, nil
}

// One domain, one runtime: the default-domain selector and the default domain's own name
// are one domain, so they share one runtime (and its entity state),
// whichever is asked for first. Other domains stay separate.
func TestForDomainSharesRuntimeForDefaultDomain(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	for _, order := range [][2]string{{"", "billing"}, {"billing", ""}} {
		r := &domainResolver{}
		tr := New(r)
		first, err := tr.ForDomain(ctx, "tenant-a", order[0])
		if err != nil {
			t.Fatalf("ForDomain(%q): %v", order[0], err)
		}
		second, err := tr.ForDomain(ctx, "tenant-a", order[1])
		if err != nil {
			t.Fatalf("ForDomain(%q): %v", order[1], err)
		}
		if first != second {
			t.Errorf("%q then %q: two runtimes for one domain", order[0], order[1])
		}
		other, err := tr.ForDomain(ctx, "tenant-a", "payouts")
		if err != nil {
			t.Fatalf("ForDomain(payouts): %v", err)
		}
		if other == first {
			t.Errorf("%q then %q: another domain shared the default's runtime", order[0], order[1])
		}
	}
}
