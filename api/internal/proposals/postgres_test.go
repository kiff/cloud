package proposals_test

import (
	"context"
	"github.com/kiff/cloud/api/internal/testdb"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/domain"
	"github.com/kiff/cloud/api/internal/executors"
	"github.com/kiff/cloud/api/internal/handlers"
	"github.com/kiff/cloud/api/internal/idempotency"
	"github.com/kiff/cloud/api/internal/proposals"
	"github.com/kiff/cloud/api/internal/receipt"
	cloudruntime "github.com/kiff/cloud/api/internal/runtime"
	"github.com/kiff/cloud/api/internal/tenant"

	kiffadapter "github.com/kiff/kiff/pkg/kiff/adapter"
	kiffaudit "github.com/kiff/kiff/pkg/kiff/audit"
	kiffevent "github.com/kiff/kiff/pkg/kiff/event"
)

// proposalListYAML is the integration-test domain. Three actions
// span every outcome we want to exercise on the read side:
//
//   - AUTO_REFUND  (low risk, no approval) — drives "allowed".
//   - REFUND_ORDER (high risk, approval required) — drives
//     "approval_required".
//   - GATED_REFUND (low risk, but requires `orders.refund`
//     permission) — drives "blocked" when the caller has no
//     matching role.
//
// "invalid" rides on a request with an unknown action_name or a
// missing required field; "limit_exceeded" rides on the
// proposal-limiter path. We surface both via the standard
// proposal request shape.
const proposalListYAML = `
domain: refund-list-test
entity: Order
events:
  - ORDER_PLACED
  - ORDER_PAID
states:
  - CREATED
  - PAID
transitions:
  - on: ORDER_PLACED
    from: ""
    to: CREATED
  - on: ORDER_PAID
    from: CREATED
    to: PAID
actions:
  - name: AUTO_REFUND
    allowed_states: [PAID]
    required_parameters: [amount, reason]
    risk: medium
    approval: never
    executor: refund.auto_refund
  - name: REFUND_ORDER
    allowed_states: [PAID]
    required_parameters: [amount, reason]
    risk: high
    approval: required
    executor: refund.issue_refund
  - name: GATED_REFUND
    allowed_states: [PAID]
    required_parameters: [amount, reason]
    required_permissions: [orders.refund]
    risk: low
    approval: never
    executor: refund.auto_refund
permissions:
  roles:
    ops_agent:
      - orders.refund
`

// requirePostgresForList returns a pool with all cloud schemas
// the proposal-list integration test needs. Skips the test when
// KIFF_TEST_POSTGRES_URL is unset.
func requirePostgresForList(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := testdb.URL(t)
	if url == "" {
		t.Skip("KIFF_TEST_POSTGRES_URL not set; skipping proposal-list integration test")
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
	if err := receipt.ApplySchema(ctx, pool); err != nil {
		t.Fatalf("receipt.ApplySchema: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// resetCloudMetaForList truncates cloud_meta.tenants (CASCADE
// drops tenant_domains, tenant_usage, and receipts that
// reference it) plus drops every per-tenant kiff_t_* schema.
func resetCloudMetaForList(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	if _, err := pool.Exec(context.Background(),
		"TRUNCATE cloud_meta.tenants CASCADE"); err != nil {
		t.Fatalf("truncate cloud_meta: %v", err)
	}
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
			t.Fatalf("scan schema name: %v", err)
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

// listIntegrationRig wires a real Postgres-backed cloud against
// the GET /v1/proposals handler. It provisions two tenants so
// cross-tenant isolation can be asserted on the same Postgres
// instance.
type listIntegrationRig struct {
	pool        *pgxpool.Pool
	tenants     *cloudruntime.TenantRuntime
	tenantA     tenant.Tenant
	tenantB     tenant.Tenant
	receipts    receipt.Store
	reader      proposals.Reader
	listHandler http.Handler // wraps /v1/proposals with tenant-A identity
}

func newListIntegrationRig(t *testing.T) *listIntegrationRig {
	t.Helper()
	pool := requirePostgresForList(t)
	resetCloudMetaForList(t, pool)
	t.Cleanup(func() { resetCloudMetaForList(t, pool) })

	databaseURL := testdb.URL(t)
	store := tenant.NewPostgresStore(pool)
	prov := tenant.NewProvisioner(pool, store)
	source := domain.NewPostgresSource(pool)
	registry := executors.DefaultRegistry()

	resolver := cloudruntime.NewCloudResolver(databaseURL, store, source, registry, nil)
	t.Cleanup(resolver.Close)
	tenants := cloudruntime.New(resolver)

	mustProvision := func(slug string) tenant.Tenant {
		tt := tenant.Tenant{
			ID:          tenant.NewID(),
			Slug:        slug,
			DisplayName: "list " + slug,
			Status:      tenant.StatusProvisioning,
		}
		if err := store.Create(context.Background(), tt); err != nil {
			t.Fatalf("create tenant %s: %v", slug, err)
		}
		if err := prov.Provision(context.Background(), tt.ID); err != nil {
			t.Fatalf("provision tenant %s: %v", slug, err)
		}
		if err := source.Save(context.Background(), tt.ID, []byte(proposalListYAML)); err != nil {
			t.Fatalf("save yaml for %s: %v", slug, err)
		}
		return tt
	}

	tenantA := mustProvision("list-a")
	tenantB := mustProvision("list-b")

	receipts := receipt.NewPostgresStore(pool)
	reader := proposals.NewPostgresReader(tenant.NewSchemaPool(pool), receipts)

	// Build the handler stack with tenant-A's identity. Tests
	// that need tenant-B re-wrap with a different identity.
	gate := handlers.NewProposalsHandler(tenants, handlers.WithProposalsReader(reader))
	innerMux := http.NewServeMux()
	gate.Register(innerMux)
	wrapped := http.NewServeMux()
	wrapped.Handle("/", withFakeIdentity(innerMux, auth.Identity{
		Tenant:  tenantA.ID,
		Subject: "list-test-a",
		Roles:   []string{"ops_agent"},
	}))

	return &listIntegrationRig{
		pool:        pool,
		tenants:     tenants,
		tenantA:     tenantA,
		tenantB:     tenantB,
		receipts:    receipts,
		reader:      reader,
		listHandler: wrapped,
	}
}

// withFakeIdentity is the helper the handler tests use; copied
// here so the proposals package's integration test is
// self-contained.
func withFakeIdentity(next http.Handler, id auth.Identity) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := auth.WithIdentity(r.Context(), id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// seedEntityInState ingests ORDER_PLACED + ORDER_PAID for the
// given entity so the proposal gate can decide against an entity
// in the PAID state.
func (rig *listIntegrationRig) seedEntityInState(t *testing.T, tenantID, orderID string) {
	t.Helper()
	rt, err := rig.tenants.For(context.Background(), tenantID)
	if err != nil {
		t.Fatalf("resolve runtime for %s: %v", tenantID, err)
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
		if _, err := rt.IngestRaw(context.Background(), raw); err != nil {
			t.Fatalf("ingest %s for %s: %v", kind, orderID, err)
		}
	}
}

// drivePostAs runs a JSON-body POST against the proposal gate
// using a custom identity. Used to drive proposals as different
// principals (e.g. cross-tenant isolation tests).
func (rig *listIntegrationRig) drivePostAs(t *testing.T, id auth.Identity, body string) *httptest.ResponseRecorder {
	t.Helper()
	gate := handlers.NewProposalsHandler(rig.tenants, handlers.WithProposalsReader(nil))
	innerMux := http.NewServeMux()
	gate.Register(innerMux)
	wrapped := http.NewServeMux()
	wrapped.Handle("/", withFakeIdentity(innerMux, id))

	req := httptest.NewRequest(http.MethodPost, "/v1/proposals/decide", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	wrapped.ServeHTTP(rec, req)
	return rec
}

// TestPostgresReader_ListReturnsRowsAfterDecide is the
// integration-style proof: drive proposals through the real
// gate against Postgres-backed runtimes, then read them back
// through PostgresReader. Asserts every outcome we can drive
// without orchestrating the limit_exceeded path (which would
// require provisioning the limiter against a stub plan).
//
// "limit_exceeded" is exercised in handlers/proposals_test.go's
// TestProposals_LimitExceeded via a stub Limiter; the wire
// shape on the list endpoint is exercised by the unit test in
// proposals_list_test.go's TestProposalsList_AllOutcomeShapesRoundTrip
// (which seeds the row directly). Doubling up here would
// duplicate setup without adding signal.
func TestPostgresReader_ListReturnsRowsAfterDecide(t *testing.T) {
	rig := newListIntegrationRig(t)

	// Seed two orders in tenant-A: one PAID (allows AUTO_REFUND
	// and REFUND_ORDER), one CREATED-only (will block actions
	// gated on PAID state).
	rig.seedEntityInState(t, rig.tenantA.ID, "order-paid")

	identityA := auth.Identity{Tenant: rig.tenantA.ID, Subject: "list-test-a", Roles: []string{"ops_agent"}}

	// Drive an "allowed" decision.
	rig.drivePostAs(t, identityA, `{
		"id": "prop-allowed",
		"entity_id": "order-paid",
		"entity_type": "Order",
		"action_name": "AUTO_REFUND",
		"actor_id": "agent-test",
		"parameters": {"amount": 4900, "reason": "damaged"}
	}`)

	// Drive an "approval_required" decision.
	rig.drivePostAs(t, identityA, `{
		"id": "prop-approval",
		"entity_id": "order-paid",
		"entity_type": "Order",
		"action_name": "REFUND_ORDER",
		"actor_id": "agent-test",
		"parameters": {"amount": 9900, "reason": "fraud"}
	}`)

	// Drive a "blocked" decision (missing role; GATED_REFUND
	// requires orders.refund, identityA has ops_agent which has
	// it, so we drive against an identity without the role).
	noRoleIdentity := auth.Identity{Tenant: rig.tenantA.ID, Subject: "norole", Roles: nil}
	rig.drivePostAs(t, noRoleIdentity, `{
		"id": "prop-blocked",
		"entity_id": "order-paid",
		"entity_type": "Order",
		"action_name": "GATED_REFUND",
		"actor_id": "agent-test",
		"parameters": {"amount": 1000, "reason": "test"}
	}`)

	// Drive an "invalid" decision (unknown action).
	rig.drivePostAs(t, identityA, `{
		"id": "prop-invalid",
		"entity_id": "order-paid",
		"entity_type": "Order",
		"action_name": "UNKNOWN_ACTION",
		"actor_id": "agent-test"
	}`)

	// Read back through GET /v1/proposals.
	req := httptest.NewRequest(http.MethodGet, "/v1/proposals", nil)
	rec := httptest.NewRecorder()
	rig.listHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	body := rec.Body.String()

	// Wire-shape regression: parameters and reasoning_summary
	// must never appear, regardless of write-side changes.
	for _, banned := range []string{`"parameters"`, `"reasoning_summary"`} {
		if strings.Contains(body, banned) {
			t.Errorf("response leaked %q: %s", banned, body)
		}
	}

	// Each driven outcome must show up by id in the list.
	wantIDs := []string{"prop-allowed", "prop-approval", "prop-blocked", "prop-invalid"}
	for _, id := range wantIDs {
		if !strings.Contains(body, `"`+id+`"`) {
			t.Errorf("response missing %q: %s", id, body)
		}
	}

	// proposal_id on the wire must be the exact prop.ID the
	// caller posted, not the framework's audit row id (which
	// has ":<outcome>" / ":rejected" suffixes appended by the
	// gate to dedupe primary keys). Pin the contract so a
	// future change that sources proposal_id from a different
	// field (e.g. trace_id, audit row id) fails loudly.
	for _, id := range wantIDs {
		bannedSuffix := `"proposal_id":"` + id + `:`
		if strings.Contains(body, bannedSuffix) {
			t.Errorf("proposal_id surfaced with audit-row suffix; got %s near %q", body, bannedSuffix)
		}
	}
}

// TestPostgresReader_TenantScoping drives a proposal against
// tenant-A and asserts tenant-B's GET sees zero rows. The
// per-tenant pool's search_path makes this mechanical: the SQL
// "SELECT ... FROM kiff_audit" against tenant-B's connection
// resolves to kiff_t_<tenantB-id>.kiff_audit, which has none of
// tenant-A's rows.
func TestPostgresReader_TenantScoping(t *testing.T) {
	rig := newListIntegrationRig(t)
	rig.seedEntityInState(t, rig.tenantA.ID, "order-a")

	identityA := auth.Identity{Tenant: rig.tenantA.ID, Subject: "test-a", Roles: []string{"ops_agent"}}

	// Drive one allowed proposal as tenant-A.
	rig.drivePostAs(t, identityA, `{
		"id": "prop-a-isolation",
		"entity_id": "order-a",
		"entity_type": "Order",
		"action_name": "AUTO_REFUND",
		"actor_id": "agent-test",
		"parameters": {"amount": 100, "reason": "smoke"}
	}`)

	// Read back as tenant-B.
	gate := handlers.NewProposalsHandler(
		rig.tenants,
		handlers.WithProposalsReader(proposals.NewPostgresReader(tenant.NewSchemaPool(rig.pool), rig.receipts)),
	)
	innerMux := http.NewServeMux()
	gate.Register(innerMux)
	wrapped := http.NewServeMux()
	wrapped.Handle("/", withFakeIdentity(innerMux, auth.Identity{
		Tenant:  rig.tenantB.ID,
		Subject: "test-b",
	}))

	req := httptest.NewRequest(http.MethodGet, "/v1/proposals", nil)
	rec := httptest.NewRecorder()
	wrapped.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, "prop-a-isolation") {
		t.Fatalf("tenant-A's row leaked into tenant-B's response: %s", body)
	}
	if !strings.Contains(body, `"proposals":[]`) {
		t.Errorf("expected empty proposals array for tenant-B; got %s", body)
	}
	// And no last_received_at on empty result.
	if strings.Contains(body, "last_received_at") {
		t.Errorf("empty result must omit last_received_at; got %s", body)
	}
}

// poolDrawLimiter is a stub Limiter that draws a connection from
// the given pool during AllowProposal, the way usage.ProposalLimiter
// does in production (FlushNow + Snapshot). It always allows; its
// only job is to contend for a connection on the same pool the
// advisory lock must NOT use.
type poolDrawLimiter struct{ pool *pgxpool.Pool }

func (l poolDrawLimiter) AllowProposal(ctx context.Context, _ string) (bool, uint64, int, bool, error) {
	c, err := l.pool.Acquire(ctx)
	if err != nil {
		return true, 0, 0, false, err // allow-when-uncertain
	}
	defer c.Release()
	_, _ = c.Exec(ctx, "SELECT 1")
	return true, 0, 1000, false, nil
}

// TestProposals_ConcurrentRetriesCollapseWithLock is the
// acceptance plus the-review deadlock guard. It wires BOTH a
// limiter (which draws a connection from a deliberately small pool)
// AND the advisory locker on a SEPARATE dedicated pool, then fires
// N concurrent retries of one key.
//
// Two things must hold:
//   - No deadlock. If the locker drew from the same small pool as
//     the limiter, the waiters parked in pg_advisory_lock would
//     exhaust it and the leader could never get its limiter
//     connection — the bug the review caught. A dedicated
//     lock pool decouples them. The test fails via timeout if that
//     regresses.
//   - Exactly one proposal_decision row — the dedup is atomic.
func TestProposals_ConcurrentRetriesCollapseWithLock(t *testing.T) {
	rig := newListIntegrationRig(t)
	rig.seedEntityInState(t, rig.tenantA.ID, "order-paid")

	url := testdb.URL(t)

	// Small pool the limiter contends on (mirrors the cloud_meta
	// pool in production). MaxConns deliberately tiny so a shared
	// lock+limiter pool would exhaust under the storm.
	limCfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatalf("parse limiter pool: %v", err)
	}
	limCfg.MaxConns = 2
	limPool, err := pgxpool.NewWithConfig(context.Background(), limCfg)
	if err != nil {
		t.Fatalf("new limiter pool: %v", err)
	}
	defer limPool.Close()

	// Dedicated lock pool — the fix.
	lockPool, err := pgxpool.New(context.Background(), url)
	if err != nil {
		t.Fatalf("new lock pool: %v", err)
	}
	defer lockPool.Close()

	gate := handlers.NewProposalsHandler(rig.tenants,
		handlers.WithProposalsLimiter(poolDrawLimiter{pool: limPool}),
		handlers.WithProposalsLocker(idempotency.NewAdvisoryLocker(lockPool)),
	)
	innerMux := http.NewServeMux()
	gate.Register(innerMux)
	wrapped := http.NewServeMux()
	wrapped.Handle("/", withFakeIdentity(innerMux, auth.Identity{
		Tenant:  rig.tenantA.ID,
		Subject: "list-test-a",
		Roles:   []string{"ops_agent"},
	}))

	const body = `{
		"id": "prop-concurrent",
		"entity_id": "order-paid",
		"entity_type": "Order",
		"action_name": "AUTO_REFUND",
		"actor_id": "agent-test",
		"parameters": {"amount": 4900, "reason": "damaged"}
	}`

	const n = 12 // > limiter pool MaxConns, to force the contention
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start // release together to maximize the race
			req := httptest.NewRequest(http.MethodPost, "/v1/proposals/decide", strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			wrapped.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Errorf("concurrent decide: status %d body %s", rec.Code, rec.Body.String())
			}
		}()
	}
	close(start)

	// Deadlock guard: a shared lock+limiter pool would hang here.
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("concurrent retries did not complete in 20s — likely a lock/limiter pool-exhaustion deadlock")
	}

	// Exactly one proposal_decision row for the shared key.
	rt, err := rig.tenants.For(context.Background(), rig.tenantA.ID)
	if err != nil {
		t.Fatalf("resolve runtime: %v", err)
	}
	recs, err := rt.Audit.Query(context.Background(), kiffaudit.Filter{
		TraceID: "prop-concurrent",
		Kind:    kiffaudit.Kind("proposal_decision"),
	})
	if err != nil {
		t.Fatalf("query audit: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("concurrent retries produced %d decision rows; want exactly 1", len(recs))
	}
}

// TestPostgresReader_WindowQueryAndCounts exercises the SQL paths the
// memory tests can't: ListDecisionsInWindow's predicate assembly +
// ORDER/LIMIT, and CountDecisionsByOutcome's GROUP BY data->>'outcome'
// over the full window (independent of the row cap). Gated on
// KIFF_TEST_POSTGRES_URL via requirePostgresForList.
func TestPostgresReader_WindowQueryAndCounts(t *testing.T) {
	rig := newListIntegrationRig(t)
	rig.seedEntityInState(t, rig.tenantA.ID, "order-paid")
	identityA := auth.Identity{Tenant: rig.tenantA.ID, Subject: "win-a", Roles: []string{"ops_agent"}}

	// Two AUTO_REFUND (allowed) + one REFUND_ORDER (approval_required).
	rig.drivePostAs(t, identityA, `{"id":"w-allow-1","entity_id":"order-paid","entity_type":"Order","action_name":"AUTO_REFUND","actor_id":"a","parameters":{"amount":100,"reason":"x"}}`)
	rig.drivePostAs(t, identityA, `{"id":"w-allow-2","entity_id":"order-paid","entity_type":"Order","action_name":"AUTO_REFUND","actor_id":"a","parameters":{"amount":200,"reason":"y"}}`)
	rig.drivePostAs(t, identityA, `{"id":"w-appr-1","entity_id":"order-paid","entity_type":"Order","action_name":"REFUND_ORDER","actor_id":"a","parameters":{"amount":9900,"reason":"z"}}`)

	ctx := context.Background()
	now := time.Now().UTC()
	window := proposals.DecisionQuery{
		Action: "AUTO_REFUND",
		From:   now.Add(-1 * time.Hour),
		To:     now.Add(1 * time.Hour),
	}

	// ListDecisionsInWindow: only the two AUTO_REFUND rows, action-scoped.
	rows, err := rig.reader.ListDecisionsInWindow(ctx, rig.tenantA.ID, window)
	if err != nil {
		t.Fatalf("ListDecisionsInWindow: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("window rows: got %d, want 2 (AUTO_REFUND in window); rows=%+v", len(rows), rows)
	}
	for _, r := range rows {
		if r.ActionName != "AUTO_REFUND" {
			t.Errorf("row action = %q, want AUTO_REFUND", r.ActionName)
		}
	}

	// Out-of-window: a future-only window returns nothing.
	future := proposals.DecisionQuery{Action: "AUTO_REFUND", From: now.Add(24 * time.Hour), To: now.Add(48 * time.Hour)}
	if fr, err := rig.reader.ListDecisionsInWindow(ctx, rig.tenantA.ID, future); err != nil || len(fr) != 0 {
		t.Fatalf("future window: got %d rows (err %v), want 0", len(fr), err)
	}

	// CountDecisionsByOutcome over the action, full window — window-complete.
	counts, total, err := rig.reader.CountDecisionsByOutcome(ctx, rig.tenantA.ID, window)
	if err != nil {
		t.Fatalf("CountDecisionsByOutcome: %v", err)
	}
	if total != 2 || counts["allowed"] != 2 {
		t.Errorf("AUTO_REFUND counts: got total=%d counts=%+v, want total=2 allowed=2", total, counts)
	}

	// Count is independent of a tiny limit (the load-bearing property).
	capped := window
	capped.Limit = 1
	cCounts, cTotal, err := rig.reader.CountDecisionsByOutcome(ctx, rig.tenantA.ID, capped)
	if err != nil {
		t.Fatalf("CountDecisionsByOutcome (capped): %v", err)
	}
	if cTotal != 2 || cCounts["allowed"] != 2 {
		t.Errorf("counts must ignore limit: got total=%d counts=%+v, want total=2 allowed=2", cTotal, cCounts)
	}
	cappedRows, _ := rig.reader.ListDecisionsInWindow(ctx, rig.tenantA.ID, capped)
	if len(cappedRows) != 1 {
		t.Errorf("capped rows: got %d, want 1", len(cappedRows))
	}

	// Counting all actions over the window sees the approval too.
	allCounts, allTotal, err := rig.reader.CountDecisionsByOutcome(ctx, rig.tenantA.ID, proposals.DecisionQuery{From: window.From, To: window.To})
	if err != nil {
		t.Fatalf("CountDecisionsByOutcome (all actions): %v", err)
	}
	if allCounts["approval_required"] != 1 || allTotal < 3 {
		t.Errorf("all-action counts: got total=%d counts=%+v, want approval_required>=1 total>=3", allTotal, allCounts)
	}
}

// ListCalls keeps one row a call, its latest decision, and marks a
// call any of whose decisions waited for a person; counts cover every
// call whatever the page.
func TestPostgresReader_ListCallsOneRowPerCall(t *testing.T) {
	rig := newListIntegrationRig(t)
	rig.seedEntityInState(t, rig.tenantA.ID, "order-paid")
	identityA := auth.Identity{Tenant: rig.tenantA.ID, Subject: "calls-a", Roles: []string{"ops_agent"}}

	rig.drivePostAs(t, identityA, `{"id":"c-allow","entity_id":"order-paid","entity_type":"Order","action_name":"AUTO_REFUND","actor_id":"a","parameters":{"amount":100,"reason":"x"}}`)
	// The same held proposal asked twice: two decisions, one call.
	rig.drivePostAs(t, identityA, `{"id":"c-held","entity_id":"order-paid","entity_type":"Order","action_name":"REFUND_ORDER","actor_id":"a","parameters":{"amount":9900,"reason":"z"}}`)
	rig.drivePostAs(t, identityA, `{"id":"c-held","entity_id":"order-paid","entity_type":"Order","action_name":"REFUND_ORDER","actor_id":"a","parameters":{"amount":9900,"reason":"z"}}`)
	rig.drivePostAs(t, identityA, `{"id":"c-other","entity_id":"order-paid","entity_type":"Order","action_name":"AUTO_REFUND","actor_id":"b","parameters":{"amount":100,"reason":"y"}}`)

	ctx := context.Background()
	reader, ok := rig.reader.(proposals.CallsReader)
	if !ok {
		t.Fatal("the Postgres reader does not list calls")
	}
	page, err := reader.ListCalls(ctx, rig.tenantA.ID, proposals.CallQuery{})
	if err != nil {
		t.Fatalf("ListCalls: %v", err)
	}
	calls, counts := page.Calls, page.Counts
	// Every agent with a call, whatever the filter.
	if strings.Join(page.Agents, ",") != "a,b" {
		t.Errorf("agents = %v, want a,b", page.Agents)
	}
	if len(calls) != 3 || counts.All != 3 || counts.Held != 1 || counts.Allowed != 2 {
		t.Fatalf("calls = %+v, counts = %+v; want 3 calls, 1 held, 2 allowed", calls, counts)
	}
	var held *proposals.CallRow
	for i := range calls {
		if calls[i].ProposalID == "c-held" {
			held = &calls[i]
		}
	}
	if held == nil || !held.Held || held.Decisions < 2 || held.Outcome != "approval_required" {
		t.Errorf("held call = %+v, want held with both decisions", held)
	}
	list := func(tenantID string, q proposals.CallQuery) proposals.CallsPage {
		t.Helper()
		p, err := reader.ListCalls(ctx, tenantID, q)
		if err != nil {
			t.Fatalf("ListCalls %+v: %v", q, err)
		}
		return p
	}
	if got := list(rig.tenantA.ID, proposals.CallQuery{Show: proposals.CallsHeld}).Calls; len(got) != 1 || got[0].ProposalID != "c-held" {
		t.Errorf("held only = %+v", got)
	}
	if p := list(rig.tenantA.ID, proposals.CallQuery{Actor: "b"}); len(p.Calls) != 1 || p.Calls[0].ProposalID != "c-other" || p.Counts.All != 1 || len(p.Agents) != 2 {
		t.Errorf("actor b = %+v", p)
	}
	if p := list(rig.tenantA.ID, proposals.CallQuery{Limit: 1, Offset: 2}); len(p.Calls) != 1 || p.Counts.All != 3 {
		t.Errorf("last page = %+v", p)
	}
	// A person's own part: every call of b, which they
	// manage, and c-held, which their run made. The counts and the agents follow.
	own := list(rig.tenantA.ID, proposals.CallQuery{Scope: &proposals.CallScope{Agents: map[string]bool{"b": true}, Mine: map[string]bool{"c-held": true}}})
	var ids []string
	for _, c := range own.Calls {
		ids = append(ids, c.ProposalID)
	}
	sort.Strings(ids)
	if strings.Join(ids, ",") != "c-held,c-other" || own.Counts.All != 2 || own.Counts.Held != 1 || strings.Join(own.Agents, ",") != "a,b" {
		t.Errorf("own part = %v, counts %+v, agents %v; want c-held and c-other, of a and b", ids, own.Counts, own.Agents)
	}
	if none := list(rig.tenantA.ID, proposals.CallQuery{Scope: &proposals.CallScope{}}); len(none.Calls) != 0 || none.Counts.All != 0 || len(none.Agents) != 0 {
		t.Errorf("no part = %+v, want nothing", none)
	}
	if got := list(rig.tenantA.ID, proposals.CallQuery{Proposal: "c-held"}).Calls; len(got) != 1 || !got[0].Held || got[0].Decisions < 2 {
		t.Errorf("one call = %+v", got)
	}
	// Another tenant's reader sees none of these.
	if p := list(rig.tenantB.ID, proposals.CallQuery{}); len(p.Calls) != 0 || p.Counts.All != 0 || len(p.Agents) != 0 {
		t.Errorf("tenant B sees %+v", p)
	}
	// A trial's calls (kiff/tafs#173): those recorded under its draft, made
	// since it started.
	if _, err := rig.pool.Exec(ctx, `UPDATE `+rig.tenantA.SchemaName()+`.kiff_audit SET data = data || '{"trial_draft":"drf_t"}'
		WHERE data->>'proposal_id' = 'c-held'`); err != nil {
		t.Fatal(err)
	}
	if p := list(rig.tenantA.ID, proposals.CallQuery{Trial: "drf_t"}); len(p.Calls) != 1 || p.Calls[0].TrialDraft != "drf_t" || p.Counts.Held != 1 || p.Counts.All != 1 {
		t.Errorf("trial drf_t = %+v", p)
	}
	if p := list(rig.tenantA.ID, proposals.CallQuery{Trial: "drf_t", Since: time.Now().Add(time.Hour)}); p.Counts.All != 0 {
		t.Errorf("trial drf_t since later = %+v", p.Counts)
	}
	if p := list(rig.tenantA.ID, proposals.CallQuery{Trial: "drf_other"}); p.Counts.All != 0 {
		t.Errorf("another trial = %+v", p.Counts)
	}
}
