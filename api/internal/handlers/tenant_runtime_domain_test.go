package handlers_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/handlers"
)

// requestWithBoundIdentity builds a request whose identity carries a
// domain-scoped API key (DomainID set) — the multi-domain design case where a
// key is bound to a single domain.
func requestWithBoundIdentity(method, path, boundDomain string) *http.Request {
	r := httptest.NewRequest(method, path, strings.NewReader("{}"))
	r.Header.Set("Content-Type", "application/json")
	id := auth.Identity{
		Subject:  "subj-1",
		Tenant:   "t1",
		Method:   auth.MethodAPIKey,
		APIKey:   true,
		DomainID: boundDomain,
	}
	return r.WithContext(auth.WithIdentity(r.Context(), id))
}

// TestTenantRuntime_DomainSelector_RoutesToDomain asserts a
// ?domain=<slug> selector on a framework-proxied surface resolves the
// named domain (not the default). The stub errors after recording the
// domain, so the handler returns 502 before delegating to the
// framework — enough to assert the routing without a real runtime.
func TestTenantRuntime_DomainSelector_RoutesToDomain(t *testing.T) {
	t.Parallel()
	accessor := &stubAccessor{err: errors.New("stop before framework")}
	h := handlers.NewTenantRuntimeHandler(accessor, "/v1", nil)

	req := requestWithIdentity(http.MethodPost, "/v1/events/raw?domain=payments")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if accessor.lastDomain != "payments" {
		t.Fatalf("ForDomain domainID = %q, want %q", accessor.lastDomain, "payments")
	}
}

// TestTenantRuntime_NoSelector_ResolvesDefault asserts the absence of
// a selector resolves the tenant's default domain ("") — unchanged
// back-compat behavior.
func TestTenantRuntime_NoSelector_ResolvesDefault(t *testing.T) {
	t.Parallel()
	accessor := &stubAccessor{err: errors.New("stop before framework")}
	h := handlers.NewTenantRuntimeHandler(accessor, "/v1", nil)

	req := requestWithIdentity(http.MethodPost, "/v1/events/raw")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if accessor.lastDomain != "" {
		t.Fatalf("ForDomain domainID = %q, want \"\" (default)", accessor.lastDomain)
	}
}

// TestTenantRuntime_BoundKeyMatchingSelector_OK asserts a domain-scoped
// key with a selector naming its own domain resolves that domain.
func TestTenantRuntime_BoundKeyMatchingSelector_OK(t *testing.T) {
	t.Parallel()
	accessor := &stubAccessor{err: errors.New("stop before framework")}
	h := handlers.NewTenantRuntimeHandler(accessor, "/v1", nil)

	req := requestWithBoundIdentity(http.MethodPost, "/v1/entities/e1/actions/PAY/execute?domain=refunds", "refunds")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if accessor.lastDomain != "refunds" {
		t.Fatalf("ForDomain domainID = %q, want %q", accessor.lastDomain, "refunds")
	}
}

// TestTenantRuntime_BoundKeyConflictingSelector_Returns403 asserts the
// the multi-domain design isolation wall: a domain-scoped key cannot be redirected
// to a different domain by an explicit selector. The runtime is never
// resolved.
func TestTenantRuntime_BoundKeyConflictingSelector_Returns403(t *testing.T) {
	t.Parallel()
	accessor := &stubAccessor{rt: nil, err: errors.New("must not be called")}
	h := handlers.NewTenantRuntimeHandler(accessor, "/v1", nil)

	req := requestWithBoundIdentity(http.MethodPost, "/v1/events/raw?domain=payments", "refunds")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	if accessor.calls != 0 {
		t.Fatalf("runtime resolved %d times; a conflicting selector must be refused before resolution", accessor.calls)
	}
}
