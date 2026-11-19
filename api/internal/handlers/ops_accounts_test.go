package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/meter"
	"github.com/kiff/cloud/api/internal/operatoraudit"
	"github.com/kiff/cloud/api/internal/receipt"
	"github.com/kiff/cloud/api/internal/tenant"
	"github.com/kiff/cloud/api/internal/usage"
)

// fakeRunDays answers RunDays from a fixed list, filtered as the ledger
// filters: by account when one is named, and by [from, to) on Last.
type fakeRunDays struct{ days []meter.RunDay }

func (f fakeRunDays) RunDays(_ context.Context, tenantID string, from, to time.Time) ([]meter.RunDay, error) {
	out := []meter.RunDay{}
	for _, d := range f.days {
		if (tenantID == "" || d.Tenant == tenantID) && !d.Last.Before(from) && d.Last.Before(to) {
			out = append(out, d)
		}
	}
	return out, nil
}

// opsAccountsFixture: two accounts, one labelled "eval" and billed by
// Stripe, with one key and runs on two days; the other with none.
func opsAccountsFixture(t *testing.T) (mux *http.ServeMux, tenants *tenant.MemoryStore, audit *operatoraudit.MemoryStore, idA, idB, plaintext string) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	tenants = tenant.NewMemoryStore()
	idA, idB = tenant.NewID(), tenant.NewID()
	if err := tenants.Create(ctx, tenant.Tenant{ID: idA, Slug: "alpha", Status: tenant.StatusActive, CreatedAt: now.Add(-time.Hour),
		Metadata: map[string]any{tenant.MetadataKeyPlan: string(tenant.PlanSolo), tenant.MetadataKeyBillingSource: "stripe", opsLabelKey: "eval"}}); err != nil {
		t.Fatal(err)
	}
	if err := tenants.Create(ctx, tenant.Tenant{ID: idB, Slug: "beta", Status: tenant.StatusActive, CreatedAt: now.Add(-48 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	keys := auth.NewMemoryAPIKeyStore()
	k, err := auth.NewAPIKey(idA, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := keys.Insert(ctx, auth.APIKeyRecord{ID: "key-1", Tenant: idA, Subject: "owner-a", Label: "the Eval's key",
		AgentID: "agt-refunds", Roles: []string{"agent"}, KeyHash: k.Hash, CreatedAt: now.Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	day := func(d int) time.Time {
		return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, -d)
	}
	runs := fakeRunDays{days: []meter.RunDay{
		{Tenant: idA, Agent: "agt-refunds", RuntimeAgent: "taf-refunds", Day: day(2), Runs: 3, Counted: 2, Credits: 40, CostMicroUSD: 200_000, Last: day(2).Add(time.Hour)},
		{Tenant: idA, Agent: "agt-coach", RuntimeAgent: "taf-coach", Setup: true, Day: day(1), Runs: 1, CostMicroUSD: 50_000, Last: day(1).Add(time.Hour)},
		{Tenant: idA, Agent: "agt-refunds", RuntimeAgent: "taf-refunds", Day: day(1), Runs: 1, Counted: 1, Credits: 10, Over: 4, CostMicroUSD: 60_000, Last: day(1).Add(2 * time.Hour)},
		// Older than the 30 days read.
		{Tenant: idA, Agent: "agt-refunds", RuntimeAgent: "taf-refunds", Day: day(40), Runs: 9, Counted: 9, Credits: 900, Last: day(40)},
	}}
	audit = operatoraudit.NewMemoryStore()
	h := NewOpsHandler([]string{opsSubject}, tenants, usage.NewMemoryStore(), receipt.NewMemoryStore(), nil, nil, nil,
		WithOpsControlPlane(audit, nil), WithOpsAccounts(keys, runs))
	mux = http.NewServeMux()
	h.Register(mux)
	return mux, tenants, audit, idA, idB, k.Plaintext
}

func TestOps_AccountList_LabelBillingKeyPrefixAndRuns(t *testing.T) {
	t.Parallel()
	mux, _, _, idA, idB, plaintext := opsAccountsFixture(t)
	rec := doOps(mux, "/v1/ops/tenants", opsSubject)
	if rec.Code != http.StatusOK {
		t.Fatalf("tenants: %d %s", rec.Code, rec.Body)
	}
	var got struct {
		Tenants []OpsTenantRow `json:"tenants"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || len(got.Tenants) != 2 {
		t.Fatalf("decode: %v, %+v", err, got)
	}
	a, b := got.Tenants[0], got.Tenants[1]
	if a.ID != idA || a.Label != "eval" || a.BilledBy != "stripe" || a.Plan != "solo" {
		t.Errorf("alpha: %+v", a)
	}
	// A key of the account's starts with its prefix: that is how a key is
	// traced to its account.
	if !strings.HasPrefix(plaintext, a.KeyPrefix) || a.KeyPrefix != auth.APIKeyPrefix+idA+"_" {
		t.Errorf("key prefix %q, key %q", a.KeyPrefix, plaintext[:len(a.KeyPrefix)])
	}
	if a.Runs != 5 || a.Credits != 50 || a.LastRunAt == nil || a.LastRunAt.Hour() != 2 {
		t.Errorf("alpha's last 30 days: runs %d, credits %d, last %v", a.Runs, a.Credits, a.LastRunAt)
	}
	if b.ID != idB || b.Label != "" || b.Runs != 0 || b.LastRunAt != nil || b.KeyPrefix != auth.APIKeyPrefix+idB+"_" {
		t.Errorf("beta: %+v", b)
	}
}

// The console never shows an API key or its hash, only the prefix every
// key of the account shares, and what the key was made for.
func TestOps_AccountDetail_KeysByPrefixOnly(t *testing.T) {
	t.Parallel()
	mux, _, _, idA, _, plaintext := opsAccountsFixture(t)
	rec := doOps(mux, "/v1/ops/tenants/"+idA, opsSubject)
	if rec.Code != http.StatusOK {
		t.Fatalf("detail: %d %s", rec.Code, rec.Body)
	}
	body := rec.Body.String()
	secret := plaintext[len(auth.APIKeyPrefix+idA+"_"):]
	parsed, err := auth.ParseAPIKey(plaintext)
	if err != nil {
		t.Fatal(err)
	}
	rec2 := auth.APIKeyRecord{KeyHash: parsed.Hash}
	if strings.Contains(body, secret) || strings.Contains(body, rec2.HexHash()) {
		t.Fatal("the detail holds the key's secret part or its hash")
	}
	var got OpsTenantDetail
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Keys) != 1 || got.Keys[0].ID != "key-1" || got.Keys[0].Prefix != auth.APIKeyPrefix+idA+"_" ||
		got.Keys[0].Agent != "agt-refunds" || got.Keys[0].Label != "the Eval's key" {
		t.Errorf("keys: %+v", got.Keys)
	}
	// Per agent, most credits first; Setup's runs apart.
	if len(got.Agents) != 2 || got.Agents[0].RuntimeAgent != "taf-refunds" || got.Agents[0].Runs != 4 || got.Agents[0].Credits != 50 ||
		got.Agents[0].CostMicroUSD != 260_000 || !got.Agents[1].Setup || got.Agents[1].Runs != 1 {
		t.Errorf("agents: %+v", got.Agents)
	}
	if got.Tenant.Runs != 5 || got.Tenant.Credits != 50 {
		t.Errorf("header: %+v", got.Tenant)
	}
}

// An operator's label on an account is one of the known labels, and is
// written with its audit entry or not at all.
func TestOps_Label_AuditedAndBounded(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	mux, tenants, audit, _, idB, _ := opsAccountsFixture(t)

	if rec := doOpsPost(mux, "/v1/ops/tenants/"+idB+"/label", "someone", `{"label":"internal"}`); rec.Code != http.StatusNotFound {
		t.Fatalf("non-operator: %d", rec.Code)
	}
	if rec := doOpsPost(mux, "/v1/ops/tenants/"+idB+"/label", opsSubject, `{"label":"vip"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown label: %d", rec.Code)
	}
	if rec := doOpsPost(mux, "/v1/ops/tenants/nope/label", opsSubject, `{"label":"internal"}`); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown account: %d", rec.Code)
	}
	rec := doOpsPost(mux, "/v1/ops/tenants/"+idB+"/label", opsSubject, `{"label":"Internal","reason":"the owner's test account"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("label: %d %s", rec.Code, rec.Body)
	}
	if b, _ := tenants.Get(ctx, idB); opsLabel(b) != "internal" {
		t.Fatalf("label stored: %q", opsLabel(b))
	}
	// The label is display only: the plan doesn't move.
	if b, _ := tenants.Get(ctx, idB); tenant.PlanFor(b).Plan != tenant.PlanFor(tenant.Tenant{}).Plan {
		t.Errorf("plan moved: %s", tenant.PlanFor(b).Plan)
	}
	entries, _ := audit.List(ctx, idB, 10)
	if len(entries) != 1 || entries[0].Action != operatoraudit.ActionLabel || entries[0].OperatorSubject != opsSubject ||
		entries[0].Reason != "the owner's test account" || entries[0].Metadata["from"] != "" || entries[0].Metadata["to"] != "internal" {
		t.Fatalf("audit: %+v", entries)
	}
	// The same label again changes nothing and writes nothing.
	if rec := doOpsPost(mux, "/v1/ops/tenants/"+idB+"/label", opsSubject, `{"label":"internal"}`); rec.Code != http.StatusOK {
		t.Fatalf("again: %d", rec.Code)
	}
	// None takes it off.
	if rec := doOpsPost(mux, "/v1/ops/tenants/"+idB+"/label", opsSubject, `{"label":""}`); rec.Code != http.StatusOK {
		t.Fatalf("none: %d", rec.Code)
	}
	if b, _ := tenants.Get(ctx, idB); opsLabel(b) != "" {
		t.Fatalf("label after none: %q", opsLabel(b))
	}
	if entries, _ := audit.List(ctx, idB, 10); len(entries) != 2 {
		t.Fatalf("audit entries: %d, want 2", len(entries))
	}

	// With the audit failing, the label stays as it was.
	fmux, ftenants, _, _, id := opsFailFixture(t)
	if rec := doOpsPost(fmux, "/v1/ops/tenants/"+id+"/label", opsSubject, `{"label":"demo"}`); rec.Code != http.StatusInternalServerError {
		t.Fatalf("audit failing: %d", rec.Code)
	}
	if a, _ := ftenants.Get(ctx, id); opsLabel(a) != "" {
		t.Fatalf("label written without its audit entry: %q", opsLabel(a))
	}
}

func TestOps_Consumption(t *testing.T) {
	t.Parallel()
	mux, _, _, idA, _, _ := opsAccountsFixture(t)
	if rec := doOps(mux, "/v1/ops/consumption", "someone"); rec.Code != http.StatusNotFound {
		t.Fatalf("non-operator: %d", rec.Code)
	}
	rec := doOps(mux, "/v1/ops/consumption", opsSubject)
	if rec.Code != http.StatusOK {
		t.Fatalf("consumption: %d %s", rec.Code, rec.Body)
	}
	var got OpsConsumption
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Rows) != 3 || !got.Rows[0].Day.After(got.Rows[2].Day) || got.Rows[0].Slug != "alpha" || got.Rows[0].Label != "eval" ||
		got.Rows[0].TenantID != idA {
		t.Fatalf("rows: %+v", got.Rows)
	}
	if got.Total.Runs != 5 || got.Total.Counted != 3 || got.Total.Credits != 50 || got.Total.Over != 4 || got.Total.CostMicroUSD != 310_000 {
		t.Fatalf("total: %+v", got.Total)
	}

	// Without the ledger, there's no table.
	h := NewOpsHandler([]string{opsSubject}, tenant.NewMemoryStore(), usage.NewMemoryStore(), receipt.NewMemoryStore(), nil, nil, nil)
	plain := http.NewServeMux()
	h.Register(plain)
	req := httptest.NewRequest(http.MethodGet, "/v1/ops/consumption", nil)
	req = req.WithContext(auth.WithIdentity(req.Context(), auth.Identity{Subject: opsSubject, Method: auth.MethodSession}))
	w := httptest.NewRecorder()
	plain.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("no ledger: %d", w.Code)
	}
}
