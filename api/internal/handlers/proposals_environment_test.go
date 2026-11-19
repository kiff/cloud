package handlers_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	kiffadapter "github.com/kiff/kiff/pkg/kiff/adapter"
	kiffaudit "github.com/kiff/kiff/pkg/kiff/audit"
	kiffevent "github.com/kiff/kiff/pkg/kiff/event"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/domain"
	"github.com/kiff/cloud/api/internal/executors"
	"github.com/kiff/cloud/api/internal/handlers"
	cloudruntime "github.com/kiff/cloud/api/internal/runtime"
	"github.com/kiff/cloud/api/internal/tenant"
)

// envDecideSetup builds a tenant + domain + one active control (with the
// given control environment) and returns a wired decide mux + runtime.
func envDecideSetup(t *testing.T, controlEnv string) (http.Handler, *cloudruntime.TenantRuntime, string) {
	t.Helper()
	ctx := context.Background()

	tenantStore := tenant.NewMemoryStore()
	tnt := tenant.Tenant{ID: tenant.NewID(), Slug: "tenant-env", Status: tenant.StatusActive}
	if err := tenantStore.Create(ctx, tnt); err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	src := domain.NewMemorySource()
	if err := src.Save(ctx, tnt.ID, []byte(proposalYAML)); err != nil {
		t.Fatalf("seed yaml: %v", err)
	}
	controls := domain.NewMemoryControlStore()
	ctrl := domain.Control{
		ID: "ctrl-env", TenantID: tnt.ID, Domain: "orders", Entity: "Order",
		Actions: []string{"GATED_REFUND"}, Status: "active",
	}
	if controlEnv != "" {
		ctrl.Environment = controlEnv
	}
	if err := controls.Create(ctx, ctrl); err != nil {
		t.Fatalf("create control: %v", err)
	}

	resolver := cloudruntime.NewMemoryResolver(tenantStore, src, executors.DefaultRegistry())
	rt := cloudruntime.New(resolver)
	h := handlers.NewProposalsHandler(rt, handlers.WithProposalsControlStore(controls))
	inner := http.NewServeMux()
	h.Register(inner)
	wrapped := http.NewServeMux()
	wrapped.Handle("/", withFakeIdentity(inner, auth.Identity{
		Tenant: tnt.ID, Subject: "subj", Roles: []string{"ops_agent"},
	}))

	resolved, err := rt.For(ctx, tnt.ID)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	for _, kind := range []string{"ORDER_PLACED", "ORDER_PAID"} {
		raw := kiffadapter.RawInput{
			ID: kind + "-env", Adapter: "cloud", Type: kind, Source: "test",
			ReceivedAt: time.Now().UTC(), EntityID: "o-env", EntityType: "Order",
			ActorID: "a", Metadata: kiffevent.Metadata{TraceID: "t-env"},
			Payload: map[string]any{},
		}
		if _, err := resolved.IngestRaw(ctx, raw); err != nil {
			t.Fatalf("ingest %s: %v", kind, err)
		}
	}
	return wrapped, rt, tnt.ID
}

func decisionRecordFor(t *testing.T, rt *cloudruntime.TenantRuntime, tenantID string) *kiffaudit.Record {
	t.Helper()
	resolved, err := rt.For(context.Background(), tenantID)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	records, err := resolved.Audit.Query(context.Background(), kiffaudit.Filter{})
	if err != nil {
		t.Fatalf("audit query: %v", err)
	}
	for i := range records {
		if records[i].Kind == kiffaudit.Kind("proposal_decision") {
			return &records[i]
		}
	}
	return nil
}

// A request carrying environment that matches an env-scoped active control
// stamps that control's identity + environment.
func TestProposals_EnvironmentMatchesControl(t *testing.T) {
	t.Parallel()
	wrapped, rt, tenantID := envDecideSetup(t, "aws")

	rec := drivePost(t, wrapped, "/v1/proposals/decide", map[string]any{
		"entity_id": "o-env", "entity_type": "Order", "action_name": "GATED_REFUND",
		"actor_id": "agent", "environment": "aws",
		"parameters": map[string]any{"amount": 100, "reason": "x"},
	})
	assertOutcome(t, rec, http.StatusOK, "allowed", nil)

	found := decisionRecordFor(t, rt, tenantID)
	if found == nil {
		t.Fatal("no proposal_decision audit record")
	}
	if found.Data["control_id"] != "ctrl-env" {
		t.Errorf("control_id = %v, want ctrl-env", found.Data["control_id"])
	}
	if found.Data["environment"] != "aws" {
		t.Errorf("environment = %v, want aws", found.Data["environment"])
	}
}

// The critical non-regression: a request environment that does NOT match
// the (single) active control's environment must still stamp the control
// via the fallback — never drop to an empty control_id.
func TestProposals_EnvironmentFallbackNeverRegresses(t *testing.T) {
	t.Parallel()
	// Control activated under the default environment; request reports a
	// different one (as a connected runtime would).
	wrapped, rt, tenantID := envDecideSetup(t, "") // default env control

	rec := drivePost(t, wrapped, "/v1/proposals/decide", map[string]any{
		"entity_id": "o-env", "entity_type": "Order", "action_name": "GATED_REFUND",
		"actor_id": "agent", "environment": "some-other-env",
		"parameters": map[string]any{"amount": 100, "reason": "x"},
	})
	assertOutcome(t, rec, http.StatusOK, "allowed", nil)

	found := decisionRecordFor(t, rt, tenantID)
	if found == nil {
		t.Fatal("no proposal_decision audit record")
	}
	if found.Data["control_id"] != "ctrl-env" {
		t.Errorf("control_id = %v, want ctrl-env (fallback must not regress stamping)", found.Data["control_id"])
	}
}

// No environment in the request behaves exactly as before (single active).
func TestProposals_NoEnvironmentUnchanged(t *testing.T) {
	t.Parallel()
	wrapped, rt, tenantID := envDecideSetup(t, "")

	rec := drivePost(t, wrapped, "/v1/proposals/decide", map[string]any{
		"entity_id": "o-env", "entity_type": "Order", "action_name": "GATED_REFUND",
		"actor_id":   "agent",
		"parameters": map[string]any{"amount": 100, "reason": "x"},
	})
	assertOutcome(t, rec, http.StatusOK, "allowed", nil)

	found := decisionRecordFor(t, rt, tenantID)
	if found == nil || found.Data["control_id"] != "ctrl-env" {
		t.Fatalf("control_id stamping changed with no environment: %+v", found)
	}
}
