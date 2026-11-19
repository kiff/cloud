package handlers

import (
	"context"
	"net/http"
	"testing"

	"github.com/kiff/cloud/api/internal/operatoraudit"
	"github.com/kiff/cloud/api/internal/packs"
	"github.com/kiff/cloud/api/internal/receipt"
	"github.com/kiff/cloud/api/internal/tenant"
	"github.com/kiff/cloud/api/internal/usage"
)

// opsBonusFixture is the control plane with bonus credits, over audit,
// and one account on plan.
func opsBonusFixture(t *testing.T, audit operatoraudit.Store, plan string) (*http.ServeMux, *packs.Memory, string) {
	t.Helper()
	ctx := context.Background()
	tenants := tenant.NewMemoryStore()
	id := tenant.NewID()
	if err := tenants.Create(ctx, tenant.Tenant{ID: id, Slug: "acme", DisplayName: "Acme", Status: tenant.StatusActive}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := tenants.SetMetadata(ctx, id, tenant.MetadataKeyPlan, plan); err != nil {
		t.Fatalf("plan: %v", err)
	}
	ledger := packs.NewMemory()
	h := NewOpsHandler([]string{opsSubject}, tenants, usage.NewMemoryStore(), receipt.NewMemoryStore(),
		nil, nil, nil, WithOpsControlPlane(audit, &recordingResetter{}), WithOpsBonusCredits(ledger))
	mux := http.NewServeMux()
	h.Register(mux)
	return mux, ledger, id
}

// An operator grants an account bonus credits: a pack of credits of its
// own that never expires, recorded in the audit log with its reason.
func TestOps_BonusCredits_GrantsAPackAndAudits(t *testing.T) {
	t.Parallel()
	audit := operatoraudit.NewMemoryStore()
	mux, ledger, id := opsBonusFixture(t, audit, "solo")
	rec := doOpsPost(mux, "/v1/ops/tenants/"+id+"/bonus", opsSubject, `{"credits": 500, "reason": "a tester's top-up"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("bonus: %d %s", rec.Code, rec.Body.String())
	}
	got, _ := ledger.CreditPacks(context.Background(), id)
	if len(got) != 1 || got[0].Kind != packs.KindBonus || got[0].Credits != 500 || got[0].CreditsLeft != 500 || got[0].Source != packs.SourceKIFF ||
		got[0].Pack != "bonus" || got[0].RevokedAt != nil {
		t.Fatalf("packs: %+v", got)
	}
	entries, _ := audit.List(context.Background(), id, 10)
	if len(entries) != 1 || entries[0].Action != operatoraudit.ActionBonusCredits || entries[0].OperatorSubject != opsSubject ||
		entries[0].Reason != "a tester's top-up" || entries[0].Metadata["pack"] != got[0].ID {
		t.Fatalf("audit: %+v", entries)
	}
	// No task bonus is asked or given any more: a tasks body is a bonus
	// of no credits, refused.
	if rec := doOpsPost(mux, "/v1/ops/tenants/"+id+"/bonus", opsSubject, `{"tasks": 15, "reason": "x"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("a tasks body: %d, want 400", rec.Code)
	}
}

// A bonus needs credits in range and a reason, on any plan; a
// non-operator never reaches it.
func TestOps_BonusCredits_Refusals(t *testing.T) {
	t.Parallel()
	audit := operatoraudit.NewMemoryStore()
	mux, ledger, id := opsBonusFixture(t, audit, "solo")
	for body, want := range map[string]int{
		`{"credits": 0, "reason": "x"}`:     http.StatusBadRequest,
		`{"credits": 10001, "reason": "x"}`: http.StatusBadRequest,
		`{"credits": -5, "reason": "x"}`:    http.StatusBadRequest,
		`{"credits": 5}`:                    http.StatusBadRequest,
		`{"credits": 5, "reason": "  "}`:    http.StatusBadRequest,
	} {
		if rec := doOpsPost(mux, "/v1/ops/tenants/"+id+"/bonus", opsSubject, body); rec.Code != want {
			t.Errorf("%s: %d, want %d", body, rec.Code, want)
		}
	}
	if rec := doOpsPost(mux, "/v1/ops/tenants/"+id+"/bonus", tenantSubject, `{"credits": 5, "reason": "x"}`); rec.Code != http.StatusNotFound {
		t.Errorf("a non-operator: %d, want 404", rec.Code)
	}
	if got, _ := ledger.CreditPacks(context.Background(), id); len(got) != 0 {
		t.Fatalf("a refused bonus was recorded: %+v", got)
	}
	if rec := doOpsPost(mux, "/v1/ops/tenants/"+id+"/bonus", opsSubject, `{"credits": 10000, "reason": "x"}`); rec.Code != http.StatusOK {
		t.Errorf("the most credits: %d, want 200", rec.Code)
	}
	company, _, cid := opsBonusFixture(t, operatoraudit.NewMemoryStore(), "company")
	if rec := doOpsPost(company, "/v1/ops/tenants/"+cid+"/bonus", opsSubject, `{"credits": 5, "reason": "x"}`); rec.Code != http.StatusOK {
		t.Errorf("a plan with no task limit: %d, want 200", rec.Code)
	}
}

// Invariant: no bonus without its audit entry. If the entry can't be
// written, the bonus is taken back.
// Category: accounting
// Attack: grant bonus credits while the operator audit log is failing
// Refused: grantBonus takes the credits back when the audit write fails
func TestOps_BonusCredits_AuditFailureTakesTheBonusBack(t *testing.T) {
	t.Parallel()
	mux, ledger, id := opsBonusFixture(t, failingAudit{}, "solo")
	if rec := doOpsPost(mux, "/v1/ops/tenants/"+id+"/bonus", opsSubject, `{"credits": 10, "reason": "x"}`); rec.Code != http.StatusInternalServerError {
		t.Fatalf("bonus with a failing audit: %d", rec.Code)
	}
	got, _ := ledger.CreditPacks(context.Background(), id)
	for _, p := range got {
		if p.RevokedAt == nil && p.CreditsLeft > 0 {
			t.Fatalf("a bonus kept without its audit entry: %+v", got)
		}
	}
}
