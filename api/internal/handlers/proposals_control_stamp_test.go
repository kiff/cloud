package handlers_test

import (
	"context"
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

	"net/http"
)

// G0b: the decide path stamps the governing control's id +
// environment onto the decision's audit record, so the windowed query
// and the evidence object can scope a decision to one control.
func TestProposals_StampsControlIdentity(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	tenantStore := tenant.NewMemoryStore()
	tnt := tenant.Tenant{ID: tenant.NewID(), Slug: "tenant-g0b", Status: tenant.StatusActive}
	if err := tenantStore.Create(ctx, tnt); err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	src := domain.NewMemorySource()
	if err := src.Save(ctx, tnt.ID, []byte(proposalYAML)); err != nil {
		t.Fatalf("seed yaml: %v", err)
	}

	// An active control governing the action under the default env.
	controls := domain.NewMemoryControlStore()
	if err := controls.Create(ctx, domain.Control{
		ID: "ctrl-g0b", TenantID: tnt.ID, Domain: "orders", Entity: "Order",
		Actions: []string{"GATED_REFUND"}, Status: "active",
	}); err != nil {
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
			ID: kind + "-o1", Adapter: "cloud", Type: kind, Source: "test",
			ReceivedAt: time.Now().UTC(), EntityID: "o1", EntityType: "Order",
			ActorID: "a", Metadata: kiffevent.Metadata{TraceID: "t-o1"},
			Payload: map[string]any{},
		}
		if _, err := resolved.IngestRaw(ctx, raw); err != nil {
			t.Fatalf("ingest %s: %v", kind, err)
		}
	}

	rec := drivePost(t, wrapped, "/v1/proposals/decide", map[string]any{
		"entity_id": "o1", "entity_type": "Order", "action_name": "GATED_REFUND",
		"actor_id": "agent-label", "parameters": map[string]any{"amount": 100, "reason": "x"},
	})
	assertOutcome(t, rec, http.StatusOK, "allowed", nil)

	records, err := resolved.Audit.Query(ctx, kiffaudit.Filter{})
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
		t.Fatal("no proposal_decision audit record")
	}
	if found.Data["control_id"] != "ctrl-g0b" {
		t.Errorf("control_id = %v, want ctrl-g0b", found.Data["control_id"])
	}
	if found.Data["environment"] != domain.DefaultControlEnvironment {
		t.Errorf("environment = %v, want %q", found.Data["environment"], domain.DefaultControlEnvironment)
	}
	// G3: the governing control version is stamped (memory store assigns
	// v1 to the first activation). The in-memory audit store keeps the
	// value as the int attributionData wrote.
	if v, ok := found.Data["control_version"].(int); !ok || v != 1 {
		t.Errorf("control_version = %v (%T), want 1", found.Data["control_version"], found.Data["control_version"])
	}
}

// With no control store wired (or no matching control), the decision
// still records and simply carries no control reference — honest, not
// an error.
func TestProposals_NoControlStore_NoStamp(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	tenantStore := tenant.NewMemoryStore()
	tnt := tenant.Tenant{ID: tenant.NewID(), Slug: "tenant-nostamp", Status: tenant.StatusActive}
	if err := tenantStore.Create(ctx, tnt); err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	src := domain.NewMemorySource()
	if err := src.Save(ctx, tnt.ID, []byte(proposalYAML)); err != nil {
		t.Fatalf("seed yaml: %v", err)
	}
	resolver := cloudruntime.NewMemoryResolver(tenantStore, src, executors.DefaultRegistry())
	rt := cloudruntime.New(resolver)

	// No control store option.
	h := handlers.NewProposalsHandler(rt)
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
			ID: kind + "-o2", Adapter: "cloud", Type: kind, Source: "test",
			ReceivedAt: time.Now().UTC(), EntityID: "o2", EntityType: "Order",
			ActorID: "a", Metadata: kiffevent.Metadata{TraceID: "t-o2"},
			Payload: map[string]any{},
		}
		if _, err := resolved.IngestRaw(ctx, raw); err != nil {
			t.Fatalf("ingest %s: %v", kind, err)
		}
	}

	rec := drivePost(t, wrapped, "/v1/proposals/decide", map[string]any{
		"entity_id": "o2", "entity_type": "Order", "action_name": "GATED_REFUND",
		"actor_id": "agent-label", "parameters": map[string]any{"amount": 100, "reason": "x"},
	})
	assertOutcome(t, rec, http.StatusOK, "allowed", nil)

	records, _ := resolved.Audit.Query(ctx, kiffaudit.Filter{})
	for i := range records {
		if records[i].Kind == kiffaudit.Kind("proposal_decision") {
			if _, ok := records[i].Data["control_id"]; ok {
				t.Error("control_id must be absent when no control store is wired")
			}
		}
	}
}

// stubLimiterAlwaysDeny forces a limit_exceeded outcome.
type stubLimiterAlwaysDeny struct{}

func (stubLimiterAlwaysDeny) AllowProposal(_ context.Context, _ string) (bool, uint64, int, bool, error) {
	return false, 1000, 1000, false, nil
}

// limit_exceeded carries the governing control's id: it has a valid
// action and an active control, and the evidence-timeline design counts it among a
// control's decisions-by-outcome (a review point 1).
func TestProposals_LimitExceeded_StampsControl(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	tenantStore := tenant.NewMemoryStore()
	tnt := tenant.Tenant{ID: tenant.NewID(), Slug: "tenant-limit-g0b", Status: tenant.StatusActive}
	if err := tenantStore.Create(ctx, tnt); err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	src := domain.NewMemorySource()
	if err := src.Save(ctx, tnt.ID, []byte(proposalYAML)); err != nil {
		t.Fatalf("seed yaml: %v", err)
	}
	controls := domain.NewMemoryControlStore()
	if err := controls.Create(ctx, domain.Control{
		ID: "ctrl-lim", TenantID: tnt.ID, Domain: "orders", Entity: "Order",
		Actions: []string{"AUTO_REFUND"}, Status: "active",
	}); err != nil {
		t.Fatalf("create control: %v", err)
	}
	resolver := cloudruntime.NewMemoryResolver(tenantStore, src, executors.DefaultRegistry())
	rt := cloudruntime.New(resolver)

	h := handlers.NewProposalsHandler(rt,
		handlers.WithProposalsLimiter(stubLimiterAlwaysDeny{}),
		handlers.WithProposalsControlStore(controls),
	)
	inner := http.NewServeMux()
	h.Register(inner)
	wrapped := http.NewServeMux()
	wrapped.Handle("/", withFakeIdentity(inner, auth.Identity{Tenant: tnt.ID, Subject: "subj"}))

	rec := drivePost(t, wrapped, "/v1/proposals/decide", map[string]any{
		"entity_id": "o-lim", "entity_type": "Order", "action_name": "AUTO_REFUND",
		"actor_id": "agent", "parameters": map[string]any{"amount": 100, "reason": "x"},
	})
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429; body=%s", rec.Code, rec.Body.String())
	}

	resolved, _ := rt.For(ctx, tnt.ID)
	records, _ := resolved.Audit.Query(ctx, kiffaudit.Filter{})
	var found *kiffaudit.Record
	for i := range records {
		if records[i].Kind == kiffaudit.Kind("proposal_rejected") {
			found = &records[i]
			break
		}
	}
	if found == nil {
		t.Fatal("no proposal_rejected audit record")
	}
	if found.Data["control_id"] != "ctrl-lim" {
		t.Errorf("control_id = %v, want ctrl-lim", found.Data["control_id"])
	}
}

// Control store wired but no active control declares the action →
// empty control_id; the decision still records (a review point 4).
func TestProposals_ControlStoreButNoMatch_NoStamp(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	tenantStore := tenant.NewMemoryStore()
	tnt := tenant.Tenant{ID: tenant.NewID(), Slug: "tenant-nomatch", Status: tenant.StatusActive}
	if err := tenantStore.Create(ctx, tnt); err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	src := domain.NewMemorySource()
	if err := src.Save(ctx, tnt.ID, []byte(proposalYAML)); err != nil {
		t.Fatalf("seed yaml: %v", err)
	}
	// Active control declares a DIFFERENT action than the proposal.
	controls := domain.NewMemoryControlStore()
	if err := controls.Create(ctx, domain.Control{
		ID: "ctrl-other", TenantID: tnt.ID, Domain: "orders", Entity: "Order",
		Actions: []string{"SOME_OTHER_ACTION"}, Status: "active",
	}); err != nil {
		t.Fatalf("create control: %v", err)
	}
	resolver := cloudruntime.NewMemoryResolver(tenantStore, src, executors.DefaultRegistry())
	rt := cloudruntime.New(resolver)

	h := handlers.NewProposalsHandler(rt, handlers.WithProposalsControlStore(controls))
	inner := http.NewServeMux()
	h.Register(inner)
	wrapped := http.NewServeMux()
	wrapped.Handle("/", withFakeIdentity(inner, auth.Identity{Tenant: tnt.ID, Subject: "subj", Roles: []string{"ops_agent"}}))

	resolved, _ := rt.For(ctx, tnt.ID)
	for _, kind := range []string{"ORDER_PLACED", "ORDER_PAID"} {
		raw := kiffadapter.RawInput{
			ID: kind + "-o3", Adapter: "cloud", Type: kind, Source: "test",
			ReceivedAt: time.Now().UTC(), EntityID: "o3", EntityType: "Order",
			ActorID: "a", Metadata: kiffevent.Metadata{TraceID: "t-o3"},
			Payload: map[string]any{},
		}
		if _, err := resolved.IngestRaw(ctx, raw); err != nil {
			t.Fatalf("ingest %s: %v", kind, err)
		}
	}

	rec := drivePost(t, wrapped, "/v1/proposals/decide", map[string]any{
		"entity_id": "o3", "entity_type": "Order", "action_name": "GATED_REFUND",
		"actor_id": "agent", "parameters": map[string]any{"amount": 100, "reason": "x"},
	})
	assertOutcome(t, rec, http.StatusOK, "allowed", nil)

	records, _ := resolved.Audit.Query(ctx, kiffaudit.Filter{})
	for i := range records {
		if records[i].Kind == kiffaudit.Kind("proposal_decision") {
			if _, ok := records[i].Data["control_id"]; ok {
				t.Error("control_id must be absent when no active control declares the action")
			}
		}
	}
}
