package runtime_test

import (
	"context"
	"github.com/kiff/cloud/api/internal/testdb"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	kiffaction "github.com/kiff/kiff/pkg/kiff/action"
	kiffactor "github.com/kiff/kiff/pkg/kiff/actor"
	kiffevent "github.com/kiff/kiff/pkg/kiff/event"
	kiffruntime "github.com/kiff/kiff/pkg/kiff/runtime"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/domain"
	"github.com/kiff/cloud/api/internal/executors"
	cloudruntime "github.com/kiff/cloud/api/internal/runtime"
	"github.com/kiff/cloud/api/internal/tenant"
	"github.com/kiff/cloud/api/internal/usage"
)

// TestE2E_UsageCountersFlow walks the same provision-resolve-execute
// path as TestE2E_ProvisionUploadResolveExecute, but with a real
// usage Aggregator wired in. After the flow, cloud_meta.tenant_usage
// must contain the expected counters tagged to the right tenant.
func TestE2E_UsageCountersFlow(t *testing.T) {
	pool := requirePostgresWithUsage(t)
	resetCloudMetaWithUsage(t, pool)
	dropTenantSchemas(t, pool)
	defer func() {
		dropTenantSchemas(t, pool)
		resetCloudMetaWithUsage(t, pool)
	}()

	databaseURL := testdb.URL(t)
	store := tenant.NewPostgresStore(pool)
	prov := tenant.NewProvisioner(pool, store)
	source := domain.NewPostgresSource(pool)
	registry := executors.DefaultRegistry()
	usageStore := usage.NewPostgresStore(pool)
	agg := usage.NewAggregator(usageStore, usage.Options{Interval: time.Hour})

	resolver := cloudruntime.NewCloudResolver(databaseURL, store, source, registry, agg)
	defer resolver.Close()
	tenants := cloudruntime.New(resolver)

	tt := tenant.Tenant{
		ID:          tenant.NewID(),
		Slug:        "usage-e2e",
		DisplayName: "usage e2e",
		Status:      tenant.StatusProvisioning,
	}
	if err := store.Create(context.Background(), tt); err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	if err := prov.Provision(context.Background(), tt.ID); err != nil {
		t.Fatalf("provision: %v", err)
	}
	if err := source.Save(context.Background(), tt.ID, []byte(e2eYAML)); err != nil {
		t.Fatalf("save yaml: %v", err)
	}

	rt, err := tenants.For(context.Background(), tt.ID)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}

	// Drive the runtime through one full cycle: ingest + execute.
	if err := rt.IngestEvent(context.Background(), kiffevent.Event{
		ID:         "ev-u-1",
		Type:       "ORDER_PLACED",
		EntityID:   "order-u-1",
		EntityType: "Order",
		Source:     "test",
		ActorID:    "agent-u",
		OccurredAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("ingest: %v", err)
	}

	contract, ok := rt.Actions.Get("MARK_PAID")
	if !ok {
		t.Fatal("MARK_PAID missing")
	}
	actionCtx := kiffaction.ActionContext{
		ActionName:   "MARK_PAID",
		EntityID:     "order-u-1",
		EntityType:   "Order",
		Actor:        kiffactor.Actor{ID: "agent-u", Roles: []string{"ops_agent"}},
		CurrentState: "CREATED",
		Parameters:   map[string]any{"payment_id": "pay-u-1"},
	}
	// Authority comes from the trusted identity on the context, not
	// actor.Roles (ContextPolicy); seed it as the auth
	// middleware would in production.
	authed := auth.WithIdentity(context.Background(), auth.Identity{
		Subject: "agent-u",
		Tenant:  tt.ID,
		Roles:   []string{"ops_agent"},
		Method:  auth.MethodSession,
	})
	if _, err := rt.ExecuteAction(authed, actionCtx, contract); err != nil {
		t.Fatalf("execute MARK_PAID: %v", err)
	}

	// Flush the in-memory aggregate to Postgres so we can assert.
	if err := agg.FlushNow(context.Background()); err != nil {
		t.Fatalf("FlushNow: %v", err)
	}

	rows, err := usageStore.Snapshot(context.Background(), usage.Filter{TenantID: tt.ID})
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(rows) == 0 {
		t.Fatal("tenant_usage rows: got 0, want >0 after a full runtime cycle")
	}

	// Expected counters from the framework's instrumentation:
	//   kiff.events.ingested       (ORDER_PLACED + follow-up ORDER_PAID = 2)
	//   kiff.actions.validated     (one validate inside ExecuteAction = 1)
	//   kiff.actions.executed      (MARK_PAID succeeds = 1)
	wantCounters := map[string]uint64{
		kiffruntime.CounterEventsIngested:   2,
		kiffruntime.CounterActionsValidated: 1,
		kiffruntime.CounterActionsExecuted:  1,
	}
	got := map[string]uint64{}
	for _, r := range rows {
		got[r.CounterName] += r.Count
		if r.TenantID != tt.ID {
			t.Errorf("row leaked to wrong tenant: %+v", r)
		}
	}
	for name, want := range wantCounters {
		if got[name] != want {
			t.Errorf("counter %q: got %d, want %d (full=%v)", name, got[name], want, got)
		}
	}

	// Cross-tenant isolation: a fresh tenant with no flow should
	// have zero rows.
	tt2 := tenant.Tenant{
		ID:          tenant.NewID(),
		Slug:        "usage-e2e-empty",
		DisplayName: "empty",
		Status:      tenant.StatusProvisioning,
	}
	if err := store.Create(context.Background(), tt2); err != nil {
		t.Fatalf("create empty tenant: %v", err)
	}
	emptyRows, _ := usageStore.Snapshot(context.Background(), usage.Filter{TenantID: tt2.ID})
	if len(emptyRows) != 0 {
		t.Fatalf("empty tenant has rows: %+v", emptyRows)
	}
}

// requirePostgresWithUsage extends the existing requirePostgres
// helper with usage-schema apply.
func requirePostgresWithUsage(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool := requirePostgres(t)
	if err := usage.ApplySchema(context.Background(), pool); err != nil {
		t.Fatalf("usage.ApplySchema: %v", err)
	}
	return pool
}

// resetCloudMetaWithUsage truncates cloud_meta.tenants which
// cascades to tenant_domains and tenant_usage.
func resetCloudMetaWithUsage(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	if _, err := pool.Exec(context.Background(),
		"TRUNCATE cloud_meta.tenants CASCADE"); err != nil {
		t.Fatalf("truncate: %v", err)
	}
}
