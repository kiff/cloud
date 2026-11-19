package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/alert"
	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/posture"
	"github.com/kiff/cloud/api/internal/proposals"
	"github.com/kiff/cloud/api/internal/receipt"
	"github.com/kiff/cloud/api/internal/tenant"
	"github.com/kiff/cloud/api/internal/usage"
)

const (
	opsSubject    = "ops-1"
	tenantSubject = "tenant-9"
)

// opsTestFixture builds an OpsHandler over in-memory stores seeded
// with two tenants, some usage, receipts across tenants, and a
// decision + alert delivery for the detail view.
func opsTestFixture(t *testing.T) (*http.ServeMux, string, string) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()

	tenants := tenant.NewMemoryStore()
	idA := tenant.NewID()
	idB := tenant.NewID()
	if err := tenants.Create(ctx, tenant.Tenant{
		ID: idA, Slug: "alpha", DisplayName: "Alpha",
		Status: tenant.StatusActive, CreatedAt: now.Add(-2 * 24 * time.Hour),
		Metadata: map[string]any{tenant.MetadataKeyPlan: string(tenant.PlanLaunchPilot)},
	}); err != nil {
		t.Fatalf("create alpha: %v", err)
	}
	if err := tenants.Create(ctx, tenant.Tenant{
		ID: idB, Slug: "beta", DisplayName: "Beta",
		Status: tenant.StatusSuspended, CreatedAt: now.Add(-40 * 24 * time.Hour),
	}); err != nil {
		t.Fatalf("create beta: %v", err)
	}

	usageStore := usage.NewMemoryStore()
	if err := usageStore.Flush(ctx, []usage.Counter{
		{TenantID: idA, CounterName: usage.CloudCounterProposalsSubmitted, HourBucket: usage.HourBucket(now), Count: 5},
		{TenantID: idA, CounterName: usage.CloudCounterProposalsBlocked, HourBucket: usage.HourBucket(now), Count: 2},
		{TenantID: idB, CounterName: usage.CloudCounterProposalsSubmitted, HourBucket: usage.HourBucket(now.Add(-1 * time.Hour)), Count: 3},
	}); err != nil {
		t.Fatalf("flush usage: %v", err)
	}

	receipts := receipt.NewMemoryStore()
	saveReceipt(t, receipts, idA, "trace-a1", "refund", now.Add(-1*time.Hour), true)
	saveReceipt(t, receipts, idB, "trace-b1", "payout", now.Add(-30*time.Minute), false)
	saveReceipt(t, receipts, idA, "trace-a2", "refund", now.Add(-10*time.Minute), false)

	decisions := proposals.NewMemoryReader()
	decisions.Append(idA, proposals.DecisionRow{
		ProposalID: "prop-a1", ActionName: "refund", Outcome: "blocked",
		CreatedAt: now.Add(-1 * time.Hour),
	})

	postureReader := posture.NewMemoryReader()
	postureReader.Append(idA, posture.Transition{From: "shadow", To: "enforce", ChangedAt: now.Add(-3 * time.Hour)})

	alerts := alert.NewMemoryDeliveryStore()
	_ = alerts.RecordDelivery(ctx, alert.Delivery{
		ID: "adlv_1", TenantID: idA, ProposalID: "prop-a1",
		Status: alert.DeliveryDelivered, AttemptedAt: now,
	})
	_ = alerts.RecordDelivery(ctx, alert.Delivery{
		ID: "adlv_2", TenantID: idA, ProposalID: "prop-a1",
		Status: alert.DeliveryFailed, AttemptedAt: now,
	})

	h := NewOpsHandler([]string{opsSubject}, tenants, usageStore, receipts, decisions, postureReader, alerts)
	if h == nil {
		t.Fatal("NewOpsHandler returned nil with valid deps")
	}
	mux := http.NewServeMux()
	h.Register(mux)
	return mux, idA, idB
}

func saveReceipt(t *testing.T, store *receipt.MemoryStore, tenantID, traceID, action string, issued time.Time, anchored bool) {
	t.Helper()
	r := receipt.Receipt{
		SchemaVersion: receipt.SchemaVersion,
		TraceID:       traceID,
		TenantID:      tenantID,
		EntityID:      "ent-1",
		ActionName:    action,
		Outcome:       "allowed",
		IssuedAt:      issued,
	}
	if anchored {
		r.Attestation = &receipt.Attestation{AnchorTxHash: "0xdeadbeef"}
	}
	if err := store.Save(context.Background(), r); err != nil {
		t.Fatalf("save receipt %s: %v", traceID, err)
	}
}

// doOps issues a GET as the given subject (empty = no identity).
func doOps(mux *http.ServeMux, path, subject string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if subject != "" {
		req = req.WithContext(auth.WithIdentity(req.Context(), auth.Identity{
			Subject: subject, Method: auth.MethodSession,
		}))
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestOps_NonOperatorGets404(t *testing.T) {
	t.Parallel()
	mux, idA, _ := opsTestFixture(t)
	paths := []string{
		"/v1/ops/overview",
		"/v1/ops/tenants",
		"/v1/ops/tenants/" + idA,
		"/v1/ops/activity",
	}
	for _, p := range paths {
		// Authenticated but not an operator.
		if rec := doOps(mux, p, tenantSubject); rec.Code != http.StatusNotFound {
			t.Errorf("%s as tenant: got %d, want 404", p, rec.Code)
		}
		// No identity at all.
		if rec := doOps(mux, p, ""); rec.Code != http.StatusNotFound {
			t.Errorf("%s anon: got %d, want 404", p, rec.Code)
		}
	}
}

func TestOps_Overview(t *testing.T) {
	t.Parallel()
	mux, _, _ := opsTestFixture(t)
	rec := doOps(mux, "/v1/ops/overview", opsSubject)
	if rec.Code != http.StatusOK {
		t.Fatalf("overview: got %d, want 200", rec.Code)
	}
	var got OpsOverview
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Tenants.Total != 2 {
		t.Errorf("total tenants: got %d, want 2", got.Tenants.Total)
	}
	if got.Tenants.ByStatus["active"] != 1 || got.Tenants.ByStatus["suspended"] != 1 {
		t.Errorf("by_status: %+v", got.Tenants.ByStatus)
	}
	// By the plan each maps to: a legacy plan and none are both the trial.
	if got.Tenants.ByPlan["trial"] != 2 || len(got.Tenants.ByPlan) != 1 {
		t.Errorf("by_plan: %+v", got.Tenants.ByPlan)
	}
	if got.Tenants.New7d != 1 { // alpha (2d); beta is 40d
		t.Errorf("new_7d: got %d, want 1", got.Tenants.New7d)
	}
	if got.Tenants.New30d != 1 {
		t.Errorf("new_30d: got %d, want 1", got.Tenants.New30d)
	}
	if got.Activity.Counters[usage.CloudCounterProposalsSubmitted] != 8 {
		t.Errorf("submitted total: got %d, want 8", got.Activity.Counters[usage.CloudCounterProposalsSubmitted])
	}
	if got.Activity.Counters[usage.CloudCounterProposalsBlocked] != 2 {
		t.Errorf("blocked total: got %d, want 2", got.Activity.Counters[usage.CloudCounterProposalsBlocked])
	}
	if got.Activity.ReceiptsRecent != 3 {
		t.Errorf("receipts_recent: got %d, want 3", got.Activity.ReceiptsRecent)
	}
}

func TestOps_TenantsList(t *testing.T) {
	t.Parallel()
	mux, idA, _ := opsTestFixture(t)
	rec := doOps(mux, "/v1/ops/tenants", opsSubject)
	if rec.Code != http.StatusOK {
		t.Fatalf("tenants: got %d, want 200", rec.Code)
	}
	var got struct {
		Tenants []OpsTenantRow `json:"tenants"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Tenants) != 2 {
		t.Fatalf("rows: got %d, want 2", len(got.Tenants))
	}
	// Newest first: alpha (2d) before beta (40d).
	if got.Tenants[0].ID != idA {
		t.Errorf("expected alpha first, got %s", got.Tenants[0].Slug)
	}
	if got.Tenants[0].LastActivityAt == nil {
		t.Error("alpha should have last-activity from usage")
	}
}

func TestOps_TenantDetail(t *testing.T) {
	t.Parallel()
	mux, idA, _ := opsTestFixture(t)
	rec := doOps(mux, "/v1/ops/tenants/"+idA, opsSubject)
	if rec.Code != http.StatusOK {
		t.Fatalf("detail: got %d, want 200", rec.Code)
	}
	var got OpsTenantDetail
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Tenant.Slug != "alpha" {
		t.Errorf("tenant slug: got %s", got.Tenant.Slug)
	}
	if len(got.Receipts) != 2 { // alpha has trace-a1 + trace-a2
		t.Errorf("receipts: got %d, want 2", len(got.Receipts))
	}
	if len(got.Proposals) != 1 {
		t.Errorf("proposals: got %d, want 1", len(got.Proposals))
	}
	if len(got.Posture) != 1 {
		t.Errorf("posture: got %d, want 1", len(got.Posture))
	}
	if got.Alerts.Delivered != 1 || got.Alerts.Failed != 1 {
		t.Errorf("alert health: %+v", got.Alerts)
	}
}

func TestOps_TenantDetail_UnknownIs404(t *testing.T) {
	t.Parallel()
	mux, _, _ := opsTestFixture(t)
	rec := doOps(mux, "/v1/ops/tenants/"+tenant.NewID(), opsSubject)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown tenant: got %d, want 404", rec.Code)
	}
}

func TestOps_Activity(t *testing.T) {
	t.Parallel()
	mux, _, _ := opsTestFixture(t)
	rec := doOps(mux, "/v1/ops/activity", opsSubject)
	if rec.Code != http.StatusOK {
		t.Fatalf("activity: got %d, want 200", rec.Code)
	}
	var got struct {
		Activity []OpsActivityRow `json:"activity"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Activity) != 3 {
		t.Fatalf("activity rows: got %d, want 3", len(got.Activity))
	}
	// Newest first: trace-a2 (-10m), trace-b1 (-30m), trace-a1 (-1h).
	if got.Activity[0].TraceID != "trace-a2" {
		t.Errorf("expected trace-a2 first, got %s", got.Activity[0].TraceID)
	}
	// Cross-tenant: both tenants appear.
	seen := map[string]bool{}
	for _, a := range got.Activity {
		seen[a.TenantID] = true
	}
	if len(seen) != 2 {
		t.Errorf("expected receipts from 2 tenants, saw %d", len(seen))
	}
	// trace-a1 was anchored.
	for _, a := range got.Activity {
		if a.TraceID == "trace-a1" && !a.Anchored {
			t.Error("trace-a1 should be anchored")
		}
	}
}

func TestNewOpsHandler_NilWhenUnconfigured(t *testing.T) {
	t.Parallel()
	tenants := tenant.NewMemoryStore()
	usageStore := usage.NewMemoryStore()
	receipts := receipt.NewMemoryStore()

	// Empty allow-list → nil.
	if h := NewOpsHandler(nil, tenants, usageStore, receipts, nil, nil, nil); h != nil {
		t.Error("expected nil handler with empty allow-list")
	}
	// Missing required store → nil.
	if h := NewOpsHandler([]string{opsSubject}, nil, usageStore, receipts, nil, nil, nil); h != nil {
		t.Error("expected nil handler with nil tenant store")
	}
	// Register on nil handler is a no-op (does not panic).
	var nilH *OpsHandler
	nilH.Register(http.NewServeMux())
}
