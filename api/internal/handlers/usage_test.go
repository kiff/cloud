package handlers_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	kiffadapter "github.com/kiff/kiff/pkg/kiff/adapter"
	kiffevent "github.com/kiff/kiff/pkg/kiff/event"
	kiffruntime "github.com/kiff/kiff/pkg/kiff/runtime"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/domain"
	"github.com/kiff/cloud/api/internal/executors"
	"github.com/kiff/cloud/api/internal/handlers"
	cloudruntime "github.com/kiff/cloud/api/internal/runtime"
	"github.com/kiff/cloud/api/internal/tenant"
	"github.com/kiff/cloud/api/internal/usage"
)

// recordingRecorder collects every Inc call so tests can assert
// that the proposal handler emitted the right cloud counter for
// each outcome. Embeds usage.Recorder semantics (entity_type
// attribute is preserved); the framework's MetricsRecorder
// interface is two methods, so this is a tiny stub.
type recordingRecorder struct {
	calls []recordedInc
}

type recordedInc struct {
	Name       string
	N          uint64
	EntityType string
}

func (r *recordingRecorder) Inc(name string, n uint64, attrs ...kiffruntime.Attr) {
	entityType := ""
	for _, a := range attrs {
		if a.Key == "entity_type" {
			entityType = a.Value
		}
	}
	r.calls = append(r.calls, recordedInc{Name: name, N: n, EntityType: entityType})
}

// proposalsRigWithCounters builds a proposal-handler test rig
// that records every Inc into a per-tenant recordingRecorder so
// tests can read back what the handler emitted.
func proposalsRigWithCounters(t *testing.T) (*proposalRig, *recordingRecorder) {
	t.Helper()
	tenantStore := tenant.NewMemoryStore()
	tnt := tenant.Tenant{
		ID:     tenant.NewID(),
		Slug:   "tenant-counters",
		Status: tenant.StatusActive,
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

	rec := &recordingRecorder{}
	factory := func(_, _ string) kiffruntime.MetricsRecorder { return rec }

	h := handlers.NewProposalsHandler(rt, handlers.WithProposalsMetrics(factory))
	innerMux := http.NewServeMux()
	h.Register(innerMux)

	wrapped := http.NewServeMux()
	wrapped.Handle("/", withFakeIdentity(innerMux, auth.Identity{
		Tenant:  tnt.ID,
		Subject: "principal",
	}))

	return &proposalRig{
		mux:      wrapped,
		rt:       rt,
		tenantID: tnt.ID,
		source:   src,
	}, rec
}

func countByName(calls []recordedInc, name string) uint64 {
	var n uint64
	for _, c := range calls {
		if c.Name == name {
			n += c.N
		}
	}
	return n
}

func TestProposals_CloudCounters_AllowedOutcome(t *testing.T) {
	t.Parallel()
	rig, rec := proposalsRigWithCounters(t)
	rig.seedEntityInState(t, "order-c1")

	body := map[string]any{
		"entity_id":   "order-c1",
		"entity_type": "Order",
		"action_name": "AUTO_REFUND",
		"actor_id":    "agent",
		"parameters":  map[string]any{"amount": 100, "reason": "x"},
	}
	resp := drivePost(t, rig.mux, "/v1/proposals/decide", body)
	assertOutcome(t, resp, http.StatusOK, "allowed", nil)

	if got := countByName(rec.calls, usage.CloudCounterProposalsSubmitted); got != 1 {
		t.Errorf("submitted = %d, want 1", got)
	}
	if got := countByName(rec.calls, usage.CloudCounterProposalsAllowed); got != 1 {
		t.Errorf("allowed = %d, want 1", got)
	}
	for _, name := range []string{
		usage.CloudCounterProposalsBlocked,
		usage.CloudCounterProposalsApprovalRequired,
		usage.CloudCounterProposalsInvalid,
	} {
		if got := countByName(rec.calls, name); got != 0 {
			t.Errorf("counter %s = %d, want 0", name, got)
		}
	}
}

func TestProposals_CloudCounters_AllOutcomes(t *testing.T) {
	t.Parallel()
	rig, rec := proposalsRigWithCounters(t)
	rig.seedEntityInState(t, "order-mix")

	type tc struct {
		name    string
		body    map[string]any
		outcome string
		counter string
	}
	cases := []tc{
		{
			name: "allowed",
			body: map[string]any{
				"entity_id": "order-mix", "entity_type": "Order",
				"action_name": "AUTO_REFUND", "actor_id": "agent",
				"parameters": map[string]any{"amount": 100, "reason": "x"},
			},
			outcome: "allowed",
			counter: usage.CloudCounterProposalsAllowed,
		},
		{
			name: "approval_required",
			body: map[string]any{
				"entity_id": "order-mix", "entity_type": "Order",
				"action_name": "REFUND_ORDER", "actor_id": "agent",
				"parameters": map[string]any{"amount": 9000, "reason": "x"},
			},
			outcome: "approval_required",
			counter: usage.CloudCounterProposalsApprovalRequired,
		},
		{
			// A missing required parameter is a malformed proposal →
			// invalid (400), not a governance block (Slice 3).
			// It increments the invalid counter, not blocked.
			name: "invalid_missing_param",
			body: map[string]any{
				"entity_id": "order-mix", "entity_type": "Order",
				"action_name": "AUTO_REFUND", "actor_id": "agent",
				"parameters": map[string]any{"amount": 100}, // no reason
			},
			outcome: "invalid",
			counter: usage.CloudCounterProposalsInvalid,
		},
		{
			name: "invalid_unknown_action",
			body: map[string]any{
				"entity_id": "order-mix", "entity_type": "Order",
				"action_name": "DELETE_TENANT", "actor_id": "agent",
				"parameters": map[string]any{},
			},
			outcome: "invalid",
			counter: usage.CloudCounterProposalsInvalid,
		},
	}

	wantSubmitted := uint64(0)
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			before := countByName(rec.calls, c.counter)
			resp := drivePost(t, rig.mux, "/v1/proposals/decide", c.body)
			wantStatus := http.StatusOK
			if c.outcome == "invalid" {
				wantStatus = http.StatusBadRequest
			}
			assertOutcome(t, resp, wantStatus, c.outcome, nil)
			if got := countByName(rec.calls, c.counter); got != before+1 {
				t.Fatalf("counter %s = %d, want %d", c.counter, got, before+1)
			}
		})
		wantSubmitted++
	}

	// Submitted counter must equal total cases (one per call).
	if got := countByName(rec.calls, usage.CloudCounterProposalsSubmitted); got != wantSubmitted {
		t.Fatalf("submitted = %d, want %d", got, wantSubmitted)
	}
}

func TestProposals_CloudCounters_NilFactoryDoesNotPanic(t *testing.T) {
	t.Parallel()
	// No metrics factory configured. The handler must still
	// answer proposals; counters silently no-op.
	tenantStore := tenant.NewMemoryStore()
	tnt := tenant.Tenant{ID: tenant.NewID(), Slug: "tenant-no-metrics", Status: tenant.StatusActive}
	if err := tenantStore.Create(context.Background(), tnt); err != nil {
		t.Fatalf("create: %v", err)
	}
	src := domain.NewMemorySource()
	if err := src.Save(context.Background(), tnt.ID, []byte(proposalYAML)); err != nil {
		t.Fatalf("seed: %v", err)
	}
	resolver := cloudruntime.NewMemoryResolver(tenantStore, src, executors.DefaultRegistry())
	rt := cloudruntime.New(resolver)

	h := handlers.NewProposalsHandler(rt) // no metrics
	innerMux := http.NewServeMux()
	h.Register(innerMux)
	wrapped := http.NewServeMux()
	wrapped.Handle("/", withFakeIdentity(innerMux, auth.Identity{Tenant: tnt.ID, Subject: "principal"}))

	body := map[string]any{
		"entity_id": "order-nm", "entity_type": "Order",
		"action_name": "AUTO_REFUND", "actor_id": "agent",
		"parameters": map[string]any{"amount": 1, "reason": "x"},
	}
	resp := drivePost(t, wrapped, "/v1/proposals/decide", body)
	// Without seeding the entity into PAID, this is "blocked"
	// but the point is it doesn't panic.
	if resp.Code < 200 || resp.Code >= 600 {
		t.Fatalf("status = %d", resp.Code)
	}
}

// ─── /v1/me/usage handler tests ──────────────────────────────

// usageRig wraps the UsageHandler under a synthetic auth so we
// can drive it from tests. Returns the rig + the underlying
// usage.MemoryStore so tests can pre-seed counters.
type usageRig struct {
	mux         http.Handler
	store       *usage.MemoryStore
	tenantStore *tenant.MemoryStore
	tenantID    string
}

func newUsageRig(t *testing.T, now time.Time) *usageRig {
	return newUsageRigWithTenantMetadata(t, now, nil)
}

func newUsageRigWithTenantMetadata(t *testing.T, now time.Time, metadata map[string]any) *usageRig {
	t.Helper()
	store := usage.NewMemoryStore()
	tenantStore := tenant.NewMemoryStore()
	tenantID := tenant.NewID()
	tnt := tenant.Tenant{
		ID:       tenantID,
		Slug:     "usage-" + tenantID,
		Status:   tenant.StatusActive,
		Metadata: metadata,
	}
	if err := tenantStore.Create(context.Background(), tnt); err != nil {
		t.Fatalf("create tenant: %v", err)
	}

	h := handlers.NewUsageHandler(store,
		handlers.WithUsageClock(func() time.Time { return now }),
		handlers.WithUsageTenantStore(tenantStore),
	)
	if h == nil {
		t.Fatalf("NewUsageHandler returned nil")
	}
	innerMux := http.NewServeMux()
	h.Register(innerMux)
	wrapped := http.NewServeMux()
	wrapped.Handle("/", withFakeIdentity(innerMux, auth.Identity{Tenant: tenantID, Subject: "principal"}))
	return &usageRig{mux: wrapped, store: store, tenantStore: tenantStore, tenantID: tenantID}
}

func TestUsage_SummaryReturnsTenantCounters(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 5, 15, 14, 0, 0, 0, time.UTC)
	rig := newUsageRig(t, now)

	// Seed two counters in the current month.
	hour := usage.HourBucket(now)
	if err := rig.store.Flush(context.Background(), []usage.Counter{
		{TenantID: rig.tenantID, CounterName: usage.CloudCounterProposalsSubmitted, EntityType: "Order", HourBucket: hour, Count: 5},
		{TenantID: rig.tenantID, CounterName: usage.CloudCounterProposalsAllowed, EntityType: "Order", HourBucket: hour, Count: 3},
	}); err != nil {
		t.Fatalf("flush: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/me/usage/counters", nil)
	rec := httptest.NewRecorder()
	rig.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var got map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got["tenant_id"] != rig.tenantID {
		t.Errorf("tenant_id = %v", got["tenant_id"])
	}
	counters, _ := got["counters"].(map[string]any)
	if counters[usage.CloudCounterProposalsSubmitted] != float64(5) {
		t.Errorf("submitted = %v", counters[usage.CloudCounterProposalsSubmitted])
	}
	if counters[usage.CloudCounterProposalsAllowed] != float64(3) {
		t.Errorf("allowed = %v", counters[usage.CloudCounterProposalsAllowed])
	}
	// Cloud counters always present, defaulted to 0.
	if counters[usage.CloudCounterProposalsBlocked] != float64(0) {
		t.Errorf("blocked = %v, want 0", counters[usage.CloudCounterProposalsBlocked])
	}
}

func TestUsage_SummaryIncludesDefaultPlanAndLimit(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 5, 15, 14, 0, 0, 0, time.UTC)
	rig := newUsageRig(t, now)

	req := httptest.NewRequest(http.MethodGet, "/v1/me/usage/counters", nil)
	rec := httptest.NewRecorder()
	rig.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var got map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got["plan"] != string(tenant.PlanTrial) {
		t.Errorf("plan = %v, want %s", got["plan"], tenant.PlanTrial)
	}
	if got["monthly_proposal_limit"] != float64(5000) {
		t.Errorf("monthly_proposal_limit = %v, want the trial's 5000 checks", got["monthly_proposal_limit"])
	}
}

// A legacy plan is the trial's (: no free tier, for old
// accounts either).
func TestUsage_SummaryLegacyPlanHasTheTrialsLimit(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 5, 15, 14, 0, 0, 0, time.UTC)
	rig := newUsageRigWithTenantMetadata(t, now, map[string]any{
		tenant.MetadataKeyPlan: string(tenant.PlanBuilder),
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/me/usage/counters", nil)
	rec := httptest.NewRecorder()
	rig.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var got map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got["plan"] != string(tenant.PlanBuilder) {
		t.Errorf("plan = %v, want %s", got["plan"], tenant.PlanBuilder)
	}
	if got["monthly_proposal_limit"] != float64(5000) {
		t.Errorf("monthly_proposal_limit = %v, want the trial's 5000", got["monthly_proposal_limit"])
	}
}

func TestUsage_SummaryUnknownPlanFallsBackToTrial(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 5, 15, 14, 0, 0, 0, time.UTC)
	rig := newUsageRigWithTenantMetadata(t, now, map[string]any{
		tenant.MetadataKeyPlan: "enterprise-that-does-not-exist",
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/me/usage/counters", nil)
	rec := httptest.NewRecorder()
	rig.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var got map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got["plan"] != string(tenant.PlanTrial) {
		t.Errorf("plan = %v, want %s", got["plan"], tenant.PlanTrial)
	}
	if got["monthly_proposal_limit"] != float64(5000) {
		t.Errorf("monthly_proposal_limit = %v, want the trial's 5000 checks", got["monthly_proposal_limit"])
	}
}

func TestUsage_SummaryIsTenantScoped(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 5, 15, 14, 0, 0, 0, time.UTC)
	rig := newUsageRig(t, now)

	// Seed counter for THIS tenant + a different tenant.
	hour := usage.HourBucket(now)
	other := tenant.NewID()
	if err := rig.store.Flush(context.Background(), []usage.Counter{
		{TenantID: rig.tenantID, CounterName: usage.CloudCounterProposalsAllowed, EntityType: "Order", HourBucket: hour, Count: 7},
		{TenantID: other, CounterName: usage.CloudCounterProposalsAllowed, EntityType: "Order", HourBucket: hour, Count: 99},
	}); err != nil {
		t.Fatalf("flush: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/me/usage/counters", nil)
	rec := httptest.NewRecorder()
	rig.mux.ServeHTTP(rec, req)
	var got map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	counters, _ := got["counters"].(map[string]any)
	if counters[usage.CloudCounterProposalsAllowed] != float64(7) {
		t.Fatalf("expected only tenant's own count (7), got %v", counters[usage.CloudCounterProposalsAllowed])
	}
}

func TestUsage_RequiresAuth(t *testing.T) {
	t.Parallel()
	store := usage.NewMemoryStore()
	h := handlers.NewUsageHandler(store)
	mux := http.NewServeMux()
	h.Register(mux)
	req := httptest.NewRequest(http.MethodGet, "/v1/me/usage/counters", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestUsage_RejectsBadPeriod(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 5, 15, 14, 0, 0, 0, time.UTC)
	rig := newUsageRig(t, now)
	req := httptest.NewRequest(http.MethodGet, "/v1/me/usage/counters?from=not-a-time", nil)
	rec := httptest.NewRecorder()
	rig.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

// TestProposals_CloudCounters_BoundedEntityType is the
// regression test for the cardinality-DoS the reviewer flagged
// in a review. Cloud proposal counters must NOT use the
// caller-supplied entity_type as a usage dimension; otherwise an
// authenticated tenant could fan out cloud_meta.tenant_usage
// rows by varying the body. The handler emits a fixed bounded
// label (usage.CloudEntityProposal).
func TestProposals_CloudCounters_BoundedEntityType(t *testing.T) {
	t.Parallel()
	rig, rec := proposalsRigWithCounters(t)
	rig.seedEntityInState(t, "order-c1")

	// Drive proposals with three distinct caller-supplied
	// entity types. All three must aggregate under the same
	// bounded label.
	for _, et := range []string{"Order", "TotallyMadeUp", "another-arbitrary-string"} {
		body := map[string]any{
			"entity_id":   "order-c1",
			"entity_type": et,
			"action_name": "AUTO_REFUND",
			"actor_id":    "agent",
			"parameters":  map[string]any{"amount": 100, "reason": "x"},
		}
		drivePost(t, rig.mux, "/v1/proposals/decide", body)
	}

	// Every cloud-counter Inc must carry CloudEntityProposal,
	// never a caller-supplied string.
	cloudCounterNames := map[string]bool{}
	for _, n := range usage.CloudCounterNames {
		cloudCounterNames[n] = true
	}
	for _, c := range rec.calls {
		if !cloudCounterNames[c.Name] {
			continue
		}
		if c.EntityType != usage.CloudEntityProposal {
			t.Errorf("counter %s emitted with entity_type=%q; want %q (caller-supplied dimensions are a cardinality DoS vector)",
				c.Name, c.EntityType, usage.CloudEntityProposal)
		}
	}
}

// TestProposals_CloudCounters_MalformedJSONStillCounts is the
// regression test for the third review concern: invalid JSON
// returned outcome=invalid but bypassed the counters because
// decode failed before incCounter. The fix bumps submitted +
// invalid before returning.
func TestProposals_CloudCounters_MalformedJSONStillCounts(t *testing.T) {
	t.Parallel()
	rig, rec := proposalsRigWithCounters(t)

	req := httptest.NewRequest(http.MethodPost, "/v1/proposals/decide", strings.NewReader("not-json"))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	rig.mux.ServeHTTP(resp, req)
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", resp.Code, resp.Body.String())
	}

	if got := countByName(rec.calls, usage.CloudCounterProposalsSubmitted); got != 1 {
		t.Errorf("submitted = %d, want 1", got)
	}
	if got := countByName(rec.calls, usage.CloudCounterProposalsInvalid); got != 1 {
		t.Errorf("invalid = %d, want 1", got)
	}
}

// fakeFlusher records FlushNow calls so the freshness test can
// assert the handler invokes the flusher before reading.
type fakeFlusher struct {
	calls int
	err   error
}

func (f *fakeFlusher) FlushNow(_ context.Context) error {
	f.calls++
	return f.err
}

// TestUsage_FlushBeforeSnapshot is the regression test for the
// staleness concern a review flagged. The handler must flush
// in-flight aggregator counters before reading the Store, so a
// post-then-read sequence sees the increment from the same
// request window.
func TestUsage_FlushBeforeSnapshot(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 5, 15, 14, 0, 0, 0, time.UTC)
	store := usage.NewMemoryStore()
	flusher := &fakeFlusher{}
	tenantID := tenant.NewID()

	h := handlers.NewUsageHandler(store,
		handlers.WithUsageClock(func() time.Time { return now }),
		handlers.WithUsageFlusher(flusher),
	)
	innerMux := http.NewServeMux()
	h.Register(innerMux)
	wrapped := http.NewServeMux()
	wrapped.Handle("/", withFakeIdentity(innerMux, auth.Identity{Tenant: tenantID, Subject: "principal"}))

	req := httptest.NewRequest(http.MethodGet, "/v1/me/usage/counters", nil)
	rec := httptest.NewRecorder()
	wrapped.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if flusher.calls != 1 {
		t.Errorf("FlushNow called %d times, want 1", flusher.calls)
	}
}

// TestUsage_FlushFailureDoesNotFailRequest documents that a
// flusher error is logged and the handler still serves whatever
// the Store has. Stale-by-one-cycle beats a 500 on the
// activation path.
func TestUsage_FlushFailureDoesNotFailRequest(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 5, 15, 14, 0, 0, 0, time.UTC)
	store := usage.NewMemoryStore()
	tenantID := tenant.NewID()
	flusher := &fakeFlusher{err: errors.New("simulated flush failure")}

	h := handlers.NewUsageHandler(store,
		handlers.WithUsageClock(func() time.Time { return now }),
		handlers.WithUsageFlusher(flusher),
	)
	innerMux := http.NewServeMux()
	h.Register(innerMux)
	wrapped := http.NewServeMux()
	wrapped.Handle("/", withFakeIdentity(innerMux, auth.Identity{Tenant: tenantID, Subject: "principal"}))

	req := httptest.NewRequest(http.MethodGet, "/v1/me/usage/counters", nil)
	rec := httptest.NewRecorder()
	wrapped.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (flush failure must not fail the read)", rec.Code)
	}
}

// TestUsage_PostThenRead_SeesFreshCounters is the end-to-end
// version of the freshness fix: post a proposal and immediately
// read /v1/me/usage; submitted + the outcome counter must be
// visible in the response, not just on the next 30-second flush.
func TestUsage_PostThenRead_SeesFreshCounters(t *testing.T) {
	t.Parallel()
	tenantStore := tenant.NewMemoryStore()
	tnt := tenant.Tenant{ID: tenant.NewID(), Slug: "tenant-fresh", Status: tenant.StatusActive}
	if err := tenantStore.Create(context.Background(), tnt); err != nil {
		t.Fatalf("create: %v", err)
	}
	src := domain.NewMemorySource()
	if err := src.Save(context.Background(), tnt.ID, []byte(proposalYAML)); err != nil {
		t.Fatalf("seed: %v", err)
	}
	resolver := cloudruntime.NewMemoryResolver(tenantStore, src, executors.DefaultRegistry())
	rt := cloudruntime.New(resolver)

	usageStore := usage.NewMemoryStore()
	// Use a long flush interval so the test only sees what
	// FlushNow drains. The handler is supposed to flush the
	// aggregator before reading.
	agg := usage.NewAggregator(usageStore, usage.Options{Interval: time.Hour})

	propH := handlers.NewProposalsHandler(rt, handlers.WithProposalsMetrics(agg.RecorderForDomain))
	usageH := handlers.NewUsageHandler(usageStore, handlers.WithUsageFlusher(agg))

	innerMux := http.NewServeMux()
	propH.Register(innerMux)
	usageH.Register(innerMux)

	wrapped := http.NewServeMux()
	wrapped.Handle("/", withFakeIdentity(innerMux, auth.Identity{Tenant: tnt.ID, Subject: "principal"}))

	// Seed the entity into PAID so the proposal is "allowed".
	resolved, err := rt.For(context.Background(), tnt.ID)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	for _, kind := range []string{"ORDER_PLACED", "ORDER_PAID"} {
		raw := kiffadapter.RawInput{
			ID: kind + "-fresh", Adapter: "cloud", Type: kind, Source: "test",
			ReceivedAt: time.Now().UTC(),
			EntityID:   "order-fresh", EntityType: "Order",
			ActorID:  "actor-test",
			Metadata: kiffevent.Metadata{TraceID: "trace-fresh"},
			Payload:  map[string]any{},
		}
		if _, err := resolved.IngestRaw(context.Background(), raw); err != nil {
			t.Fatalf("ingest %s: %v", kind, err)
		}
	}

	// 1) post a proposal.
	body := map[string]any{
		"entity_id":   "order-fresh",
		"entity_type": "Order",
		"action_name": "AUTO_REFUND",
		"actor_id":    "agent",
		"parameters":  map[string]any{"amount": 100, "reason": "x"},
	}
	resp := drivePost(t, wrapped, "/v1/proposals/decide", body)
	assertOutcome(t, resp, http.StatusOK, "allowed", nil)

	// 2) read /v1/me/usage. The aggregator's flush interval is an
	//    hour, so the only way submitted + allowed appear here is
	//    if the handler ran FlushNow before Snapshot.
	getReq := httptest.NewRequest(http.MethodGet, "/v1/me/usage/counters", nil)
	getRec := httptest.NewRecorder()
	wrapped.ServeHTTP(getRec, getReq)
	if getRec.Code != http.StatusOK {
		t.Fatalf("usage status = %d, body = %s", getRec.Code, getRec.Body.String())
	}
	var got map[string]any
	_ = json.Unmarshal(getRec.Body.Bytes(), &got)
	counters, _ := got["counters"].(map[string]any)
	if counters[usage.CloudCounterProposalsSubmitted] != float64(1) {
		t.Errorf("submitted = %v, want 1 (counter not flushed before snapshot)", counters[usage.CloudCounterProposalsSubmitted])
	}
	if counters[usage.CloudCounterProposalsAllowed] != float64(1) {
		t.Errorf("allowed = %v, want 1 (counter not flushed before snapshot)", counters[usage.CloudCounterProposalsAllowed])
	}
}
