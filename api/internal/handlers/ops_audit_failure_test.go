package handlers

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/operatoralerts"
	"github.com/kiff/cloud/api/internal/operatoraudit"
	"github.com/kiff/cloud/api/internal/receipt"
	"github.com/kiff/cloud/api/internal/tenant"
	"github.com/kiff/cloud/api/internal/usage"
)

// failingAudit fails every Record so tests can prove a state change
// does not commit (suspend/plan) — or at least is not silently
// unaudited (alerts) — when the audit write fails.
type failingAudit struct{}

func (failingAudit) Record(context.Context, operatoraudit.Entry) error {
	return errors.New("injected audit failure")
}
func (failingAudit) List(context.Context, string, int) ([]operatoraudit.Entry, error) {
	return nil, nil
}

// opsFailFixture wires the handler with a failing audit store (so the
// default in-memory mutator records audit first and refuses to mutate
// on failure), an active tenant, and the alert queue.
func opsFailFixture(t *testing.T) (*http.ServeMux, *tenant.MemoryStore, *operatoralerts.MemoryStore, *recordingResetter, string) {
	t.Helper()
	ctx := context.Background()
	tenants := tenant.NewMemoryStore()
	id := tenant.NewID()
	_ = tenants.Create(ctx, tenant.Tenant{ID: id, Slug: "acme", Status: tenant.StatusActive,
		Metadata: map[string]any{tenant.MetadataKeyPlan: string(tenant.PlanFreeDev)}})
	alerts := operatoralerts.NewMemoryStore()
	resetter := &recordingResetter{}
	h := NewOpsHandler([]string{opsSubject}, tenants, usage.NewMemoryStore(), receipt.NewMemoryStore(),
		nil, nil, nil, WithOpsControlPlane(failingAudit{}, resetter), WithOpsAlertQueue(alerts))
	mux := http.NewServeMux()
	h.Register(mux)
	return mux, tenants, alerts, resetter, id
}

// Invariant: If the audit write fails, the mutation does not happen. A
// suspended tenant with no audit record is not an outcome this system
// can produce.
// Category: failure-modes
// Attack: Suspend a tenant at the moment the audit write fails
// Refused: The mutation rolls back. A suspended tenant with no audit record
// is not a state this system can reach
func TestOps_Suspend_AuditFailureLeavesTenantUnchanged(t *testing.T) {
	t.Parallel()
	mux, tenants, _, resetter, id := opsFailFixture(t)

	rec := doOpsPost(mux, "/v1/ops/tenants/"+id+"/suspend", opsSubject, `{"reason":"x"}`)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("want 500 on audit failure, got %d", rec.Code)
	}
	// State must NOT have changed.
	tt, _ := tenants.Get(context.Background(), id)
	if tt.Status != tenant.StatusActive {
		t.Fatalf("tenant changed despite audit failure: status=%s", tt.Status)
	}
	// Cache must NOT have been evicted (no committed change).
	if calls := resetter.Calls(); len(calls) != 0 {
		t.Fatalf("resetter called despite failed mutation: %v", calls)
	}

	// Retry is NOT a silent no-op: tenant is still active, so the
	// retry path runs again (and fails again on the injected audit),
	// proving a failed mutation does not leave a hidden state change.
	rec = doOpsPost(mux, "/v1/ops/tenants/"+id+"/suspend", opsSubject, "")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("retry want 500 (not no-op 200), got %d", rec.Code)
	}
}

func TestOps_PlanChange_AuditFailureLeavesPlanUnchanged(t *testing.T) {
	t.Parallel()
	mux, tenants, _, _, id := opsFailFixture(t)

	rec := doOpsPost(mux, "/v1/ops/tenants/"+id+"/plan", opsSubject, `{"plan":"team","reason":"x"}`)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("want 500 on audit failure, got %d", rec.Code)
	}
	tt, _ := tenants.Get(context.Background(), id)
	if got := string(tenant.PlanFor(tt).Plan); got != "free_dev" {
		t.Fatalf("plan changed despite audit failure: %s", got)
	}
}

func TestOps_AlertResolve_AuditFailureNotIgnored(t *testing.T) {
	t.Parallel()
	mux, _, alerts, _, _ := opsFailFixture(t)
	// Seed an open alert directly.
	a, _ := alerts.Upsert(context.Background(), operatoralerts.Upsert{
		TenantID: "t1", Kind: "blocked_spike", Severity: "high", Detail: "x",
	}, time.Now())

	rec := doOpsPost(mux, "/v1/ops/alerts/"+a.ID+"/resolve", opsSubject, "")
	// The audit error must be surfaced, not swallowed.
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("want 500 (audit error surfaced), got %d", rec.Code)
	}
}
