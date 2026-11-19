package handlers_test

import (
	"context"
	"net/http"
	"testing"

	kiffaudit "github.com/kiff/kiff/pkg/kiff/audit"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/domain"
	"github.com/kiff/cloud/api/internal/executors"
	"github.com/kiff/cloud/api/internal/handlers"
	cloudruntime "github.com/kiff/cloud/api/internal/runtime"
	"github.com/kiff/cloud/api/internal/tenant"
)

// domainRoutingMux builds a decide handler whose synthetic auth
// middleware stamps a domain-scoped API-key identity (DomainID set).
// The seeded domain's slug is "refund-test" (from proposalYAML).
func domainRoutingMux(t *testing.T, keyDomainID string) (http.Handler, string) {
	t.Helper()
	mux, _, tenantID := domainRoutingMuxRT(t, keyDomainID)
	return mux, tenantID
}

// domainRoutingMuxRT is domainRoutingMux but also returns the tenant
// runtime accessor, so a test can query the audit store the decide
// path wrote to.
func domainRoutingMuxRT(t *testing.T, keyDomainID string) (http.Handler, *cloudruntime.TenantRuntime, string) {
	t.Helper()
	tenantStore := tenant.NewMemoryStore()
	tnt := tenant.Tenant{
		ID:          tenant.NewID(),
		Slug:        "tenant-routing",
		DisplayName: "tenant routing",
		Status:      tenant.StatusActive,
	}
	if err := tenantStore.Create(context.Background(), tnt); err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	src := domain.NewMemorySource()
	if err := src.Save(context.Background(), tnt.ID, []byte(proposalYAML)); err != nil {
		t.Fatalf("seed yaml: %v", err)
	}
	resolver := cloudruntime.NewMemoryResolver(tenantStore, src, executors.DefaultRegistry())
	rt := cloudruntime.New(resolver)

	h := handlers.NewProposalsHandler(rt)
	innerMux := http.NewServeMux()
	h.Register(innerMux)

	wrapped := http.NewServeMux()
	wrapped.Handle("/", withFakeIdentity(innerMux, auth.Identity{
		Tenant:   tnt.ID,
		Subject:  "svc",
		Method:   auth.MethodAPIKey,
		APIKey:   true,
		DomainID: keyDomainID,
		Roles:    []string{"ops_operator"},
	}))
	return wrapped, rt, tnt.ID
}

// TestDecide_DomainScopedKey_RefusesConflictingSelector is the
// security-critical case: a key bound to one domain must not be
// steered onto another by an explicit `domain` request field.
// The decide path returns 403.
func TestDecide_DomainScopedKey_RefusesConflictingSelector(t *testing.T) {
	t.Parallel()
	mux, _ := domainRoutingMux(t, "refunds")

	rec := drivePost(t, mux, "/v1/proposals/decide", map[string]any{
		"entity_id":   "order-1",
		"entity_type": "Order",
		"action_name": "MARK_PAID",
		"actor_id":    "agent-1",
		"domain":      "payouts", // conflicts with the key's binding
	})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body = %s", rec.Code, rec.Body.String())
	}
}

// TestDecide_DomainScopedKey_MatchingSelectorRoutes confirms a
// domain-scoped key whose binding matches the seeded domain resolves
// the runtime and decides (no 403): the binding routed to a real
// domain rather than being refused.
func TestDecide_DomainScopedKey_MatchingSelectorRoutes(t *testing.T) {
	t.Parallel()
	// proposalYAML's domain slugifies to "refund-test".
	mux, _ := domainRoutingMux(t, "refund-test")

	rec := drivePost(t, mux, "/v1/proposals/decide", map[string]any{
		"entity_id":   "order-1",
		"entity_type": "Order",
		"action_name": "MARK_PAID",
		"actor_id":    "agent-1",
	})
	if rec.Code == http.StatusForbidden {
		t.Fatalf("matching-domain key must not be refused; got 403, body = %s", rec.Body.String())
	}
}

// TestDecide_StampsDomainID confirms the decision the gate records
// carries the resolved domain_id, so the receipt built
// from it knows its domain instead of the web re-deriving domains[0].
func TestDecide_StampsDomainID(t *testing.T) {
	t.Parallel()
	// Tenant-wide key; route explicitly to the seeded domain
	// (proposalYAML slugifies to "refund-test").
	mux, rt, tenantID := domainRoutingMuxRT(t, "")

	rec := drivePost(t, mux, "/v1/proposals/decide", map[string]any{
		"entity_id":   "order-stamp-1",
		"entity_type": "Order",
		"action_name": "MARK_PAID",
		"actor_id":    "agent-1",
		"domain":      "refund-test",
	})
	if rec.Code == http.StatusForbidden {
		t.Fatalf("unexpected 403; body = %s", rec.Body.String())
	}

	runtime, err := rt.ForDomain(context.Background(), tenantID, "refund-test")
	if err != nil {
		t.Fatalf("resolve runtime: %v", err)
	}
	records, err := runtime.Audit.Query(context.Background(), kiffaudit.Filter{})
	if err != nil {
		t.Fatalf("audit query: %v", err)
	}
	var found *kiffaudit.Record
	for i := range records {
		if records[i].Kind == kiffaudit.Kind("proposal_decision") {
			found = &records[i]
			break
		}
	}
	if found == nil {
		t.Fatal("no proposal_decision audit record recorded")
	}
	if found.Data["domain_id"] != "refund-test" {
		t.Errorf("domain_id = %v, want refund-test", found.Data["domain_id"])
	}
}
