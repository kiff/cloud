package handlers_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	kiffadapter "github.com/kiff/kiff/pkg/kiff/adapter"
	kiffevent "github.com/kiff/kiff/pkg/kiff/event"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/domain"
	"github.com/kiff/cloud/api/internal/executors"
	"github.com/kiff/cloud/api/internal/handlers"
	cloudruntime "github.com/kiff/cloud/api/internal/runtime"
	"github.com/kiff/cloud/api/internal/tenant"
)

// lifecycleResponse mirrors the fields of kifflifecycle.Lifecycle the
// test asserts on. Kept local so the test reads the wire JSON, not the
// framework struct directly.
type lifecycleResponse struct {
	EntityID     string `json:"entity_id"`
	EntityType   string `json:"entity_type"`
	CurrentState string `json:"current_state"`
	Stages       []struct {
		Kind string `json:"kind"`
	} `json:"stages"`
}

// newLifecycleRig builds a tenant + runtime and mounts only the
// EntityLifecycleHandler behind a fake identity, so the test drives
// the read endpoint directly.
func newLifecycleRig(t *testing.T) (http.Handler, *cloudruntime.TenantRuntime, string) {
	t.Helper()
	tenantStore := tenant.NewMemoryStore()
	tnt := tenant.Tenant{
		ID:          tenant.NewID(),
		Slug:        "tenant-lc",
		DisplayName: "tenant lc",
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

	inner := http.NewServeMux()
	handlers.NewEntityLifecycleHandler(rt, nil).Register(inner)
	wrapped := http.NewServeMux()
	wrapped.Handle("/", withFakeIdentity(inner, auth.Identity{Tenant: tnt.ID, Subject: "lc-subject"}))
	return wrapped, rt, tnt.ID
}

// seedLifecycleEntity ingests ORDER_PLACED + ORDER_PAID so the entity
// has a real audit trail and a current state to project.
func seedLifecycleEntity(t *testing.T, rt *cloudruntime.TenantRuntime, tenantID, orderID string) {
	t.Helper()
	resolved, err := rt.For(context.Background(), tenantID)
	if err != nil {
		t.Fatalf("resolve runtime: %v", err)
	}
	for _, kind := range []string{"ORDER_PLACED", "ORDER_PAID"} {
		raw := kiffadapter.RawInput{
			ID:         kind + "-" + orderID,
			Adapter:    "cloud",
			Type:       kind,
			Source:     "test",
			ReceivedAt: time.Now().UTC(),
			EntityID:   orderID,
			EntityType: "Order",
			ActorID:    "actor-test",
			Metadata:   kiffevent.Metadata{TraceID: "trace-" + orderID},
			Payload:    map[string]any{},
		}
		if _, err := resolved.IngestRaw(context.Background(), raw); err != nil {
			t.Fatalf("ingest %s: %v", kind, err)
		}
	}
}

// TestEntityLifecycle_ProjectsSeededHistory: an entity with an
// ingested event history returns 200 with a lifecycle whose stages
// reflect the events and whose current state is the latest transition.
func TestEntityLifecycle_ProjectsSeededHistory(t *testing.T) {
	t.Parallel()
	mux, rt, tenantID := newLifecycleRig(t)
	seedLifecycleEntity(t, rt, tenantID, "order-lc-1")

	rec := driveGet(t, mux, "/v1/entities/order-lc-1/lifecycle")
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp lifecycleResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v; body=%s", err, rec.Body.String())
	}
	if resp.EntityID != "order-lc-1" {
		t.Errorf("entity_id = %q, want order-lc-1", resp.EntityID)
	}
	if len(resp.Stages) == 0 {
		t.Errorf("expected stages from the ingested history, got none; body=%s", rec.Body.String())
	}
	if resp.CurrentState == "" {
		t.Errorf("expected a current_state derived from the state changes, got empty; body=%s", rec.Body.String())
	}
}

// TestEntityLifecycle_EmptyEntityIsEmptyLifecycle: an entity with no
// history returns 200 with an empty (but well-formed) lifecycle, never
// a 404 — a read model has no notion of "not found" for a valid tenant.
func TestEntityLifecycle_EmptyEntityIsEmptyLifecycle(t *testing.T) {
	t.Parallel()
	mux, _, _ := newLifecycleRig(t)

	rec := driveGet(t, mux, "/v1/entities/never-seen/lifecycle")
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp lifecycleResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.EntityID != "never-seen" {
		t.Errorf("entity_id = %q, want never-seen", resp.EntityID)
	}
	if len(resp.Stages) != 0 {
		t.Errorf("expected no stages for an entity with no history, got %d", len(resp.Stages))
	}
}

// TestEntityLifecycle_RequiresTenant: a request whose identity has no
// tenant is rejected 401 (the auth context, not the path, owns tenant).
func TestEntityLifecycle_RequiresTenant(t *testing.T) {
	t.Parallel()
	resolver := cloudruntime.NewMemoryResolver(tenant.NewMemoryStore(), domain.NewMemorySource(), executors.DefaultRegistry())
	rt := cloudruntime.New(resolver)
	inner := http.NewServeMux()
	handlers.NewEntityLifecycleHandler(rt, nil).Register(inner)
	wrapped := http.NewServeMux()
	wrapped.Handle("/", withFakeIdentity(inner, auth.Identity{Subject: "no-tenant"}))

	rec := driveGet(t, wrapped, "/v1/entities/order-x/lifecycle")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status: got %d, want 401", rec.Code)
	}
}
