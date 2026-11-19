package handlers_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	kiffruntime "github.com/kiff/kiff/pkg/kiff/runtime"

	"github.com/kiff/cloud/api/internal/domain"
	"github.com/kiff/cloud/api/internal/handlers"
)

// stubAccessor is the minimal TenantRuntimeAccessor for the gate
// tests below. The hook-firing contract is exercised end-to-end by
// the integration test in cmd/api/main_test.go where a real
// runtime is wired; here we only need to confirm the per-request
// gating decisions (401 / 502 / unauthorized → no hook).
type stubAccessor struct {
	rt         *kiffruntime.Runtime
	err        error
	calls      int
	lastDomain string // domainID passed to the most recent ForDomain call
	// spec is what SpecForDomain returns. Zero declares nothing, which
	// is enough for routing tests; the approval-review gate reads the
	// reviewer permission from it.
	spec domain.Spec
}

func (s *stubAccessor) For(_ context.Context, _ string) (*kiffruntime.Runtime, error) {
	s.calls++
	if s.err != nil {
		return nil, s.err
	}
	return s.rt, nil
}

// ForDomain records the resolved domainID (so the selector tests can
// assert routing) and delegates to For for the runtime itself.
func (s *stubAccessor) ForDomain(ctx context.Context, tenantID, domainID string) (*kiffruntime.Runtime, error) {
	s.lastDomain = domainID
	return s.For(ctx, tenantID)
}

// SpecForDomain returns the stub's spec (zero unless a test sets one).
func (*stubAccessor) DomainIDFor(_ context.Context, _ string, domainID string) (string, error) {
	return domainID, nil
}

func (s *stubAccessor) SpecForDomain(_ context.Context, _ string, domainID string) (domain.Spec, error) {
	s.lastDomain = domainID
	return s.spec, nil
}

func TestTenantRuntime_NoIdentity_Returns401(t *testing.T) {
	t.Parallel()
	accessor := &stubAccessor{rt: &kiffruntime.Runtime{}}
	h := handlers.NewTenantRuntimeHandler(accessor, "/v1", nil)

	req := httptest.NewRequest(http.MethodPost, "/v1/events/raw", strings.NewReader("{}"))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if accessor.calls != 0 {
		t.Fatalf("accessor.For called %d times; expected 0 when identity missing", accessor.calls)
	}
}

func TestTenantRuntime_RuntimeResolutionFails_Returns502(t *testing.T) {
	t.Parallel()
	accessor := &stubAccessor{err: errors.New("tenant runtime broken")}
	h := handlers.NewTenantRuntimeHandler(accessor, "/v1", nil)

	req := requestWithIdentity(http.MethodPost, "/v1/events/raw")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rec.Code)
	}
}

func TestTenantRuntime_WithPostExecuteHook_ReturnsSameHandler(t *testing.T) {
	// Sanity check that the builder returns *self for chaining.
	t.Parallel()
	accessor := &stubAccessor{rt: &kiffruntime.Runtime{}}
	h := handlers.NewTenantRuntimeHandler(accessor, "/v1", nil)
	if got := h.WithPostExecuteHook(nil); got != h {
		t.Fatalf("WithPostExecuteHook did not return same handler")
	}
}

// The synchronous-hook contract is asserted end-to-end in
// cmd/api/main_test.go::Test_NewRouter_PostExecuteHook_BuildsAndPersistsReceipt
// where a real runtime is wired and the integration test reads the
// receipt store immediately after the execute returns (no polling).
// A handler-level test here would need a stub framework runtime
// that returns 2xx, which is more machinery than the contract
// warrants.
