package runtime_test

import (
	"context"
	"fmt"
	"github.com/kiff/cloud/api/internal/testdb"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	kiffaction "github.com/kiff/kiff/pkg/kiff/action"
	kiffactor "github.com/kiff/kiff/pkg/kiff/actor"
	kiffapproval "github.com/kiff/kiff/pkg/kiff/approval"
	kiffaudit "github.com/kiff/kiff/pkg/kiff/audit"
	kiffevent "github.com/kiff/kiff/pkg/kiff/event"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/domain"
	"github.com/kiff/cloud/api/internal/executors"
	cloudruntime "github.com/kiff/cloud/api/internal/runtime"
	"github.com/kiff/cloud/api/internal/tenant"
)

const e2eYAML = `
domain: refund
entity: Order

events:
  - ORDER_PLACED
  - ORDER_PAID
  - ORDER_REFUNDED

states:
  - CREATED
  - PAID
  - REFUNDED

transitions:
  - on: ORDER_PLACED
    from: ""
    to: CREATED
  - on: ORDER_PAID
    from: CREATED
    to: PAID
  - on: ORDER_REFUNDED
    from: PAID
    to: REFUNDED

actions:
  - name: MARK_PAID
    allowed_states: [CREATED]
    required_parameters: [payment_id]
    required_permissions: [orders.mark_paid]
    risk: low
    approval: never
    executor: refund.mark_paid
  - name: REFUND_ORDER
    allowed_states: [PAID]
    required_parameters: [amount, reason]
    required_permissions: [orders.refund]
    risk: high
    approval: required
    executor: refund.issue_refund

permissions:
  roles:
    ops_agent:    [orders.mark_paid, orders.refund]
    ops_operator: [orders.mark_paid, orders.refund, refund.approve]
`

// requirePostgres returns a pool with all cloud schemas applied.
func requirePostgres(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := testdb.URL(t)
	if url == "" {
		t.Skip("KIFF_TEST_POSTGRES_URL not set; skipping end-to-end test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("ping: %v", err)
	}
	if err := tenant.ApplyCloudMeta(ctx, pool); err != nil {
		t.Fatalf("ApplyCloudMeta: %v", err)
	}
	if err := domain.ApplyTenantDomainsSchema(ctx, pool); err != nil {
		t.Fatalf("ApplyTenantDomainsSchema: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func resetCloudMeta(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	// CASCADE handles tenant_domains, tenant_usage, and any future
	// cloud_meta table that references cloud_meta.tenants.
	if _, err := pool.Exec(context.Background(),
		"TRUNCATE cloud_meta.tenants CASCADE"); err != nil {
		t.Fatalf("truncate cloud_meta: %v", err)
	}
}

func dropTenantSchemas(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	rows, err := pool.Query(context.Background(),
		"SELECT schema_name FROM information_schema.schemata WHERE schema_name LIKE 'kiff_t_%'")
	if err != nil {
		t.Fatalf("list tenant schemas: %v", err)
	}
	var schemas []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			rows.Close()
			t.Fatalf("scan: %v", err)
		}
		schemas = append(schemas, s)
	}
	rows.Close()
	for _, s := range schemas {
		if _, err := pool.Exec(context.Background(), "DROP SCHEMA "+s+" CASCADE"); err != nil {
			t.Fatalf("drop %s: %v", s, err)
		}
	}
}

// TestE2E_ProvisionUploadResolveExecute is the domain-upload acceptance
// proof. It walks the whole stack: provision two tenants, upload a
// kiff.yaml for each, resolve a runtime per tenant, ingest events,
// execute actions, and confirm tenant A's audit records are
// invisible to tenant B.
func TestE2E_ProvisionUploadResolveExecute(t *testing.T) {
	pool := requirePostgres(t)
	resetCloudMeta(t, pool)
	dropTenantSchemas(t, pool)
	defer func() {
		dropTenantSchemas(t, pool)
		resetCloudMeta(t, pool)
	}()

	databaseURL := testdb.URL(t)
	store := tenant.NewPostgresStore(pool)
	prov := tenant.NewProvisioner(pool, store)
	source := domain.NewPostgresSource(pool)
	registry := executors.DefaultRegistry()

	resolver := cloudruntime.NewCloudResolver(databaseURL, store, source, registry, nil)
	defer resolver.Close()
	tenants := cloudruntime.New(resolver)

	mustProvision := func(slug string) tenant.Tenant {
		tt := tenant.Tenant{
			ID:          tenant.NewID(),
			Slug:        slug,
			DisplayName: "E2E " + slug,
			Status:      tenant.StatusProvisioning,
		}
		if err := store.Create(context.Background(), tt); err != nil {
			t.Fatalf("create tenant %s: %v", slug, err)
		}
		if err := prov.Provision(context.Background(), tt.ID); err != nil {
			t.Fatalf("provision tenant %s: %v", slug, err)
		}
		if err := source.Save(context.Background(), tt.ID, []byte(e2eYAML)); err != nil {
			t.Fatalf("save yaml for %s: %v", slug, err)
		}
		return tt
	}

	tenantA := mustProvision("e2e-a")
	tenantB := mustProvision("e2e-b")

	rtA, err := tenants.For(context.Background(), tenantA.ID)
	if err != nil {
		t.Fatalf("resolve A: %v", err)
	}
	rtB, err := tenants.For(context.Background(), tenantB.ID)
	if err != nil {
		t.Fatalf("resolve B: %v", err)
	}

	// Tenant A: ingest ORDER_PLACED + MARK_PAID for one order.
	now := time.Now().UTC()
	if err := rtA.IngestEvent(context.Background(), kiffevent.Event{
		ID:         "ev-a-1",
		Type:       "ORDER_PLACED",
		EntityID:   "order-a-1",
		EntityType: "Order",
		Source:     "test",
		ActorID:    "agent-a",
		OccurredAt: now,
	}); err != nil {
		t.Fatalf("A ingest ORDER_PLACED: %v", err)
	}

	actionCtx := kiffaction.ActionContext{
		ActionName:   "MARK_PAID",
		EntityID:     "order-a-1",
		EntityType:   "Order",
		Actor:        kiffactor.Actor{ID: "agent-a", Roles: []string{"ops_agent"}},
		CurrentState: "CREATED",
		Parameters:   map[string]any{"payment_id": "pay-1"},
	}
	contract, ok := rtA.Actions.Get("MARK_PAID")
	if !ok {
		t.Fatal("A action MARK_PAID missing")
	}
	// Authority is resolved from the trusted identity on the context,
	// not from actor.Roles (ContextPolicy). The production
	// decide path seeds this via the auth middleware; a direct
	// ExecuteAction call must seed it too. An operator identity covers
	// mark_paid, refund, and refund.approve for the whole flow.
	authed := auth.WithIdentity(context.Background(), auth.Identity{
		Subject: "agent-a",
		Tenant:  tenantA.ID,
		Roles:   []string{"ops_operator"},
		Method:  auth.MethodSession,
	})
	if _, err := rtA.ExecuteAction(authed, actionCtx, contract); err != nil {
		t.Fatalf("A execute MARK_PAID: %v", err)
	}

	// Tenant A: confirm timeline contains the expected audit kinds.
	timelineA, err := rtA.Timeline(context.Background(), "order-a-1")
	if err != nil {
		t.Fatalf("A timeline: %v", err)
	}
	if len(timelineA) == 0 {
		t.Fatal("A timeline is empty after execute")
	}
	wantKinds := map[kiffaudit.Kind]bool{
		kiffaudit.KindEventIngested:   false,
		kiffaudit.KindStateChanged:    false,
		kiffaudit.KindActionValidated: false,
		kiffaudit.KindActionExecuted:  false,
	}
	for _, rec := range timelineA {
		if _, ok := wantKinds[rec.Kind]; ok {
			wantKinds[rec.Kind] = true
		}
	}
	for k, seen := range wantKinds {
		if !seen {
			t.Errorf("A audit missing kind %q", k)
		}
	}

	// Tenant B: timeline for the same entity id should be empty.
	timelineB, err := rtB.Timeline(context.Background(), "order-a-1")
	if err != nil {
		t.Fatalf("B timeline: %v", err)
	}
	if len(timelineB) != 0 {
		t.Fatalf("B leaked %d audit records from A: %v", len(timelineB), timelineB)
	}

	// And cross-check the cache is actually returning the same
	// runtime instance per tenant.
	rtA2, err := tenants.For(context.Background(), tenantA.ID)
	if err != nil {
		t.Fatalf("re-resolve A: %v", err)
	}
	if rtA != rtA2 {
		t.Fatalf("cache miss: For() returned a fresh runtime instead of cached")
	}

	// And invalidation works: ResetTenant evicts.
	tenants.ResetTenant(tenantA.ID)
	rtA3, err := tenants.For(context.Background(), tenantA.ID)
	if err != nil {
		t.Fatalf("re-resolve A after reset: %v", err)
	}
	if rtA == rtA3 {
		t.Fatalf("ResetTenant did not evict: same runtime returned")
	}

	// Approval flow: REFUND_ORDER requires approval. Without one
	// it should refuse to execute.
	//
	// Continue using the *original* runtime here. Per-tenant
	// runtimes hold state-machine state in-memory (the framework's
	// default); evicting the runtime after the runtime drove a
	// state transition means the rebuilt runtime has no in-memory
	// state for that entity until RebuildState is called. A
	// production cloud rebuilds state on resolve; that is a
	// follow-up RFC. The test exercises the cached runtime
	// behaviour intentionally to keep the assertions on the data
	// plane.
	rt := rtA
	contract, ok = rt.Actions.Get("REFUND_ORDER")
	if !ok {
		t.Fatal("REFUND_ORDER missing in domain")
	}
	refundCtx := kiffaction.ActionContext{
		ActionName:   "REFUND_ORDER",
		EntityID:     "order-a-1",
		EntityType:   "Order",
		Actor:        kiffactor.Actor{ID: "agent-a", Roles: []string{"ops_agent"}},
		CurrentState: "PAID",
		Parameters:   map[string]any{"amount": 49.5, "reason": "duplicate charge"},
	}
	if err := rt.ValidateAction(authed, refundCtx, contract); err == nil {
		t.Fatal("REFUND_ORDER without approval should be rejected")
	}

	// Grant approval, then re-attempt with the approval id.
	approvalID := fmt.Sprintf("appr-%d", time.Now().UnixNano())
	if _, err := rt.RequestApproval(authed, approvalID, refundCtx, contract, "test approval"); err != nil {
		t.Fatalf("RequestApproval: %v", err)
	}
	if _, err := rt.ReviewApproval(authed, approvalID, "operator-1", kiffapproval.StatusGranted, "ok"); err != nil {
		t.Fatalf("ReviewApproval: %v", err)
	}
	refundCtx.ApprovalID = approvalID
	res, err := rt.ExecuteAction(authed, refundCtx, contract)
	if err != nil {
		t.Fatalf("REFUND_ORDER with approval: %v", err)
	}
	if res.Status != kiffaction.ExecutionSucceeded {
		t.Fatalf("status: got %q, want succeeded", res.Status)
	}
}
