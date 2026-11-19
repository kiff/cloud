package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/operatoralerts"
	"github.com/kiff/cloud/api/internal/operatoraudit"
	"github.com/kiff/cloud/api/internal/receipt"
	"github.com/kiff/cloud/api/internal/tenant"
	"github.com/kiff/cloud/api/internal/usage"
)

// opsFullFixture wires the control plane AND the alert queue, seeding
// one tenant whose month-to-date usage trips the blocked-spike signal.
func opsFullFixture(t *testing.T) (*http.ServeMux, *operatoralerts.MemoryStore, *operatoraudit.MemoryStore, string) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	tenants := tenant.NewMemoryStore()
	id := tenant.NewID()
	_ = tenants.Create(ctx, tenant.Tenant{ID: id, Slug: "acme", Status: tenant.StatusActive,
		Metadata: map[string]any{tenant.MetadataKeyPlan: string(tenant.PlanFreeDev)}})

	usageStore := usage.NewMemoryStore()
	// 100 submitted / 40 blocked this month → 40% blocked (high).
	_ = usageStore.Flush(ctx, []usage.Counter{
		{TenantID: id, CounterName: usage.CloudCounterProposalsSubmitted, HourBucket: usage.HourBucket(now), Count: 100},
		{TenantID: id, CounterName: usage.CloudCounterProposalsBlocked, HourBucket: usage.HourBucket(now), Count: 40},
	})

	audit := operatoraudit.NewMemoryStore()
	alerts := operatoralerts.NewMemoryStore()
	h := NewOpsHandler([]string{opsSubject}, tenants, usageStore, receipt.NewMemoryStore(), nil, nil, nil,
		WithOpsControlPlane(audit, &recordingResetter{}), WithOpsAlertQueue(alerts))
	mux := http.NewServeMux()
	h.Register(mux)
	return mux, alerts, audit, id
}

func TestOps_AlertSyncFromRiskThenResolve(t *testing.T) {
	t.Parallel()
	mux, alerts, audit, id := opsFullFixture(t)

	// Sync computes risk → upserts a blocked_spike alert.
	rec := doOpsPost(mux, "/v1/ops/alerts/sync", opsSubject, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("sync: got %d body=%s", rec.Code, rec.Body.String())
	}
	open, _ := alerts.List(context.Background(), true, 10)
	if len(open) != 1 || open[0].Kind != "blocked_spike" || open[0].Severity != "high" {
		t.Fatalf("expected one high blocked_spike alert, got %+v", open)
	}
	if open[0].TenantID != id {
		t.Fatalf("alert tenant = %s, want %s", open[0].TenantID, id)
	}

	// Re-sync dedups (count bumps, still one row).
	_ = doOpsPost(mux, "/v1/ops/alerts/sync", opsSubject, "")
	open, _ = alerts.List(context.Background(), true, 10)
	if len(open) != 1 || open[0].Count != 2 {
		t.Fatalf("re-sync should dedup+bump; got %+v", open)
	}

	// Resolve it (audited).
	rec = doOpsPost(mux, "/v1/ops/alerts/"+open[0].ID+"/resolve", opsSubject, `{"reason":"misconfig fixed"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("resolve: got %d", rec.Code)
	}
	if remaining, _ := alerts.List(context.Background(), true, 10); len(remaining) != 0 {
		t.Fatalf("resolved alert should leave open list; got %d", len(remaining))
	}
	// The resolve is in the operator audit trail.
	entries, _ := audit.List(context.Background(), id, 10)
	found := false
	for _, e := range entries {
		if e.Action == operatoraudit.ActionAlertResolve {
			found = true
		}
	}
	if !found {
		t.Fatal("resolve not recorded in operator audit")
	}
}

func TestOps_ChangePlan(t *testing.T) {
	t.Parallel()
	mux, _, audit, id := opsFullFixture(t)

	rec := doOpsPost(mux, "/v1/ops/tenants/"+id+"/plan", opsSubject, `{"plan":"team","reason":"upgrade"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("plan change: got %d body=%s", rec.Code, rec.Body.String())
	}
	var out map[string]string
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out["plan"] != "team" || out["from"] != "free_dev" {
		t.Fatalf("plan response: %+v", out)
	}
	// Audited.
	entries, _ := audit.List(context.Background(), id, 10)
	if len(entries) != 1 || entries[0].Action != operatoraudit.ActionPlanChange {
		t.Fatalf("plan change audit: %+v", entries)
	}

	// Unknown plan → 400, no audit. ("enterprise" is now a valid
	// canonical plan, so use a genuinely bogus value.)
	rec = doOpsPost(mux, "/v1/ops/tenants/"+id+"/plan", opsSubject, `{"plan":"enterprise_unlimited"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown plan: got %d, want 400", rec.Code)
	}
}

func TestOps_AlertActionRequiresOperator(t *testing.T) {
	t.Parallel()
	mux, alerts, _, _ := opsFullFixture(t)
	_ = doOpsPost(mux, "/v1/ops/alerts/sync", opsSubject, "")
	open, _ := alerts.List(context.Background(), true, 10)
	// Non-operator cannot resolve.
	rec := doOpsPost(mux, "/v1/ops/alerts/"+open[0].ID+"/resolve", tenantSubject, "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("non-operator: got %d, want 404", rec.Code)
	}
}
