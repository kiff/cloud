package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/appstore"
	"github.com/kiff/cloud/api/internal/billing"
	"github.com/kiff/cloud/api/internal/meter"
	"github.com/kiff/cloud/api/internal/operatoraudit"
	"github.com/kiff/cloud/api/internal/packs"
	"github.com/kiff/cloud/api/internal/playstore"
	"github.com/kiff/cloud/api/internal/receipt"
	"github.com/kiff/cloud/api/internal/tenant"
	"github.com/kiff/cloud/api/internal/usage"
)

// storeResetFixture is opsControlFixture's, with its account one whose store
// test purchases count (App Review's) when testAccount, as Reset store billing
// needs.
func storeResetFixture(t *testing.T, testAccount bool) (*http.ServeMux, *tenant.MemoryStore, *operatoraudit.MemoryStore, *recordingResetter, string) {
	t.Helper()
	ctx := context.Background()
	tenants := tenant.NewMemoryStore()
	id := tenant.NewID()
	if err := tenants.Create(ctx, tenant.Tenant{ID: id, Slug: "review", DisplayName: "Review", Status: tenant.StatusActive}); err != nil {
		t.Fatal(err)
	}
	audit := operatoraudit.NewMemoryStore()
	resetter := &recordingResetter{}
	var named []string
	if testAccount {
		named = []string{" ", id}
	}
	h := NewOpsHandler([]string{opsSubject}, tenants, usage.NewMemoryStore(), receipt.NewMemoryStore(),
		nil, nil, nil, WithOpsControlPlane(audit, resetter), WithOpsStoreTestAccounts(named...))
	mux := http.NewServeMux()
	h.Register(mux)
	return mux, tenants, audit, resetter, id
}

// soloFromAStore is an account a store bills Solo for, as App Review's is
// after a TestFlight purchase.
func soloFromAStore() map[string]any {
	return map[string]any{tenant.MetadataKeyPlan: "solo", tenant.MetadataKeyBillingSource: "apple",
		billing.MetaBillingStatus: billing.StatusActive, appstore.MetaOriginalTransaction: "orig-review",
		appstore.MetaExpiresAt: "2026-12-26T00:00:00Z", appstore.MetaAutoRenew: "on", appstore.MetaAutoRenewAt: "2026-11-26T00:00:00Z",
		playstore.MetaPurchaseToken: "tok-review", playstore.MetaExpiresAt: "2026-12-26T00:00:00Z"}
}

// Reset store billing (POST /v1/ops/tenants/{id}/store-billing/reset): with
// the account's id typed back and a reason, the account is back to no plan,
// both stores' subscriptions cleared and set aside, audited, the runtime's
// cache evicted, and the answer reads the account back.
func TestOps_ResetStoreBilling(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	mux, tenants, audit, resetter, id := storeResetFixture(t, true)
	for k, v := range soloFromAStore() {
		if err := tenants.SetMetadata(ctx, id, k, v); err != nil {
			t.Fatal(err)
		}
	}
	path := "/v1/ops/tenants/" + id + "/store-billing/reset"
	// Refused, changing nothing: no confirm, a confirm that isn't this
	// account's id, no reason, and someone who isn't an operator.
	for name, body := range map[string]string{
		"no confirm":      `{"reason":"App Review"}`,
		"another account": `{"confirm":"` + tenant.NewID() + `","reason":"App Review"}`,
		"no reason":       `{"confirm":"` + id + `"}`,
	} {
		if rec := doOpsPost(mux, path, opsSubject, body); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: %d %s", name, rec.Code, rec.Body.String())
		}
	}
	if rec := doOpsPost(mux, path, tenantSubject, `{"confirm":"`+id+`","reason":"x"}`); rec.Code != http.StatusNotFound {
		t.Fatalf("not an operator: %d", rec.Code)
	}
	if got, _ := tenants.Get(ctx, id); tenant.PlanFor(got).Canonical != tenant.PlanSolo {
		t.Fatal("a refused reset changed the plan")
	}
	if entries, _ := audit.List(ctx, id, 10); len(entries) != 0 {
		t.Fatalf("a refused reset was audited: %+v", entries)
	}

	rec := doOpsPost(mux, path, opsSubject, `{"confirm":"`+id+`","reason":"App Review account back to no plan after the TestFlight Solo"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("reset: %d %s", rec.Code, rec.Body.String())
	}
	var back storeResetView
	if err := json.Unmarshal(rec.Body.Bytes(), &back); err != nil {
		t.Fatal(err)
	}
	if back.Plan != "trial" || back.BillingSource != "" || back.BillingStatus != billing.StatusCanceled || back.From != "solo" ||
		back.AppleSubscription || back.GoogleSubscription || !back.AppleReset || !back.GoogleReset || back.ResetAt == "" {
		t.Fatalf("read-back: %+v", back)
	}
	got, _ := tenants.Get(ctx, id)
	for _, k := range []string{appstore.MetaOriginalTransaction, appstore.MetaExpiresAt, appstore.MetaAutoRenew, appstore.MetaAutoRenewAt,
		playstore.MetaPurchaseToken, playstore.MetaExpiresAt} {
		if v, _ := got.Metadata[k].(string); v != "" {
			t.Errorf("%s still %q", k, v)
		}
	}
	apple, _, okA := appstore.AppStore(packs.Catalog{}).WasReset(got)
	google, _, okG := playstore.GooglePlay(packs.Catalog{}).WasReset(got)
	if !okA || !okG || len(apple) != 1 || apple[0] != "orig-review" || len(google) != 1 || google[0] != "tok-review" {
		t.Fatalf("the old subscriptions weren't set aside: %v %v", apple, google)
	}
	if calls := resetter.Calls(); len(calls) != 1 || calls[0] != id {
		t.Fatalf("ResetTenant calls = %v", calls)
	}
	entries, _ := audit.List(ctx, id, 10)
	if len(entries) != 1 || entries[0].Action != operatoraudit.ActionStoreBillingReset || entries[0].OperatorSubject != opsSubject ||
		entries[0].Metadata["from_plan"] != "solo" || entries[0].Metadata["from_billing_source"] != "apple" {
		t.Fatalf("audit: %+v", entries)
	}
}

// An account Stripe or a contract bills, or on a plan no store bills, is
// refused: the reset is for store billing alone.
func TestOps_ResetStoreBillingRefusesOtherBilling(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for name, meta := range map[string]map[string]any{
		"stripe":   {tenant.MetadataKeyPlan: "solo", tenant.MetadataKeyBillingSource: "stripe"},
		"team":     {tenant.MetadataKeyPlan: "team"},
		"internal": {tenant.MetadataKeyPlan: "internal"},
	} {
		mux, tenants, audit, _, id := storeResetFixture(t, true)
		for k, v := range meta {
			if err := tenants.SetMetadata(ctx, id, k, v); err != nil {
				t.Fatal(err)
			}
		}
		if rec := doOpsPost(mux, "/v1/ops/tenants/"+id+"/store-billing/reset", opsSubject, `{"confirm":"`+id+`","reason":"x"}`); rec.Code != http.StatusConflict {
			t.Fatalf("%s: %d %s", name, rec.Code, rec.Body.String())
		}
		if entries, _ := audit.List(ctx, id, 10); len(entries) != 0 {
			t.Fatalf("%s: audited", name)
		}
	}
}

// A paying customer's store Solo, on an account that isn't App Review's or a
// store test account, is refused: its subscription would be ignored while
// the store kept charging. Nothing changes and nothing is audited.
func TestOps_ResetStoreBillingOnlyResetsStoreTestAccounts(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	mux, tenants, audit, resetter, id := storeResetFixture(t, false)
	for k, v := range soloFromAStore() {
		if err := tenants.SetMetadata(ctx, id, k, v); err != nil {
			t.Fatal(err)
		}
	}
	rec := doOpsPost(mux, "/v1/ops/tenants/"+id+"/store-billing/reset", opsSubject, `{"confirm":"`+id+`","reason":"x"}`)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "store test account") {
		t.Fatalf("a paying customer's account: %d %s", rec.Code, rec.Body.String())
	}
	got, _ := tenants.Get(ctx, id)
	if tenant.PlanFor(got).Canonical != tenant.PlanSolo || got.Metadata[appstore.MetaOriginalTransaction] != "orig-review" {
		t.Fatal("the refused reset changed the account")
	}
	if entries, _ := audit.List(ctx, id, 10); len(entries) != 0 || len(resetter.Calls()) != 0 {
		t.Fatalf("a refused reset was audited or evicted: %+v %v", entries, resetter.Calls())
	}
}

// After the reset, Plan & usage reads no plan, and offers Solo to whoever
// may buy it.
func TestPlanAndUsageAfterAStoreBillingReset(t *testing.T) {
	meta := soloFromAStore()
	for k, v := range appstore.ResetBilling(tenant.Tenant{Metadata: meta}, time.Date(2026, 11, 26, 17, 0, 0, 0, time.UTC),
		appstore.Store{MetaSubscription: appstore.MetaOriginalTransaction, MetaExpiresAt: appstore.MetaExpiresAt},
		appstore.Store{MetaSubscription: playstore.MetaPurchaseToken, MetaExpiresAt: playstore.MetaExpiresAt}) {
		meta[k] = v
	}
	r := newCreditsRigOn(t, meta)
	v := r.balance(r.admin(), "/v1/me/usage/credits")
	if v.Kind != meter.BalanceNone || v.Why != meter.WhyNoPlan || !v.CanBuy.Solo || v.CanBuy.Packs {
		t.Fatalf("after the reset: %+v", v)
	}
}
