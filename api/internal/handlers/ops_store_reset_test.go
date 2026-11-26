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
	if back.Plan != "trial" || back.BillingSource != "" || back.BillingStatus != "" || back.From != "solo" ||
		back.Trial != "not_started" || back.NoPlanAllowanceFrom == "" ||
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

// After the reset the account starts again as a new sign-up does: no plan,
// its trial not started, and Coach answering within the no-plan allowance,
// counted from the reset. What Coach spent on no plan before the reset no
// longer counts against it; what it spends after does.
func TestCoachAnswersAfterAStoreBillingReset(t *testing.T) {
	meta := soloFromAStore()
	resetAt := time.Date(2026, 11, 7, 11, 0, 0, 0, time.UTC)
	for k, v := range appstore.ResetBilling(tenant.Tenant{Metadata: meta}, resetAt,
		appstore.Store{MetaSubscription: appstore.MetaOriginalTransaction, MetaExpiresAt: appstore.MetaExpiresAt},
		appstore.Store{MetaSubscription: playstore.MetaPurchaseToken, MetaExpiresAt: playstore.MetaExpiresAt}) {
		meta[k] = v
	}
	got := tenant.Tenant{Metadata: meta}
	if k, _ := meter.KindOf(got); k != meter.BalanceNone || tenant.PlanFor(got).Canonical != tenant.PlanTrial ||
		tenant.TrialStatusAt(got, resetAt) != tenant.TrialNotStarted || tenant.BillingSourceOf(got) != tenant.BillingNone {
		t.Fatalf("after the reset: kind %s, plan %s, trial %v, source %q", k, tenant.PlanFor(got).Plan, tenant.TrialStatusAt(got, resetAt), tenant.BillingSourceOf(got))
	}
	r := newCreditsRigOn(t, meta)
	// Before the reset, Coach spent the whole no-plan allowance; since, nothing.
	r.ledger.noPlanSpent, r.ledger.noPlanSpentSince = meter.NoPlanSetupMicroUSD*3, 0
	if code, out := r.allowance(r.setup()); code != http.StatusOK || out["allowed"] != true {
		t.Fatalf("Coach after the reset: %d %v", code, out)
	}
	if !r.ledger.noPlanAsked.Equal(resetAt) {
		t.Fatalf("the allowance was counted from %v, want the reset's %v", r.ledger.noPlanAsked, resetAt)
	}
	// Spent again since the reset: refused, as a new sign-up's would be.
	r.ledger.noPlanSpentSince = meter.NoPlanSetupMicroUSD
	if _, out := r.allowance(r.setup()); out["allowed"] != false || out["why"] != meter.WhyNoPlan {
		t.Fatalf("Coach past the allowance since the reset: %v", out)
	}
	// A taf doesn't run on no plan, reset or not.
	if _, out := r.allowance(r.taf("agt-refunds")); out["allowed"] != false {
		t.Fatalf("a taf after the reset: %v", out)
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

// The reset also sets aside the account's App Store and Google Play packs
// (its store test purchases), with the change and its audit entry: a Solo
// bought after it shows Solo's own credits, not the test packs bought before.
// A bonus an operator granted stays.
func TestAStoreBillingResetSetsAsideTheStorePacks(t *testing.T) {
	ctx := context.Background()
	r := newCreditsRigOn(t, soloFromAStore())
	for _, g := range []packs.CreditGrant{
		{Pack: "credits-500", Credits: 500, Source: packs.SourceApple, SourceTxn: "sandbox-500"},
		{Pack: "credits-5000", Credits: 5000, Source: packs.SourceApple, SourceTxn: "sandbox-5000"},
		{Pack: "bonus", Credits: 40, Kind: packs.KindBonus, Source: packs.SourceKIFF, SourceTxn: "bonus-1"},
	} {
		g.TenantID, g.At = r.id, r.now.Add(-time.Hour)
		if _, _, err := r.packs.RecordCredits(ctx, g); err != nil {
			t.Fatal(err)
		}
	}
	if v := r.balance(r.admin(), "/v1/me/usage/credits"); v.PacksLeft != 5540 {
		t.Fatalf("before the reset: %d pack credits", v.PacksLeft)
	}
	audit := operatoraudit.NewMemoryStore()
	h := NewOpsHandler([]string{opsSubject}, r.tenants, usage.NewMemoryStore(), receipt.NewMemoryStore(), nil, nil, nil,
		WithOpsControlPlane(audit, &recordingResetter{}), WithOpsStoreTestAccounts(r.id), WithOpsBonusCredits(r.packs))
	mux := http.NewServeMux()
	h.Register(mux)
	rec := doOpsPost(mux, "/v1/ops/tenants/"+r.id+"/store-billing/reset", opsSubject, `{"confirm":"`+r.id+`","reason":"App Review"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("reset: %d %s", rec.Code, rec.Body.String())
	}
	var back storeResetView
	if err := json.Unmarshal(rec.Body.Bytes(), &back); err != nil {
		t.Fatal(err)
	}
	if back.PacksSetAside == nil || *back.PacksSetAside != (packs.StorePacksSetAside{Packs: 2, Credits: 5500}) || back.Plan != "trial" {
		t.Fatalf("read-back: %+v %+v", back, back.PacksSetAside)
	}
	entries, _ := audit.List(ctx, r.id, 10)
	if len(entries) != 1 || entries[0].Metadata["store_packs_set_aside"] != 2 || entries[0].Metadata["store_pack_credits_set_aside"] != int64(5500) {
		t.Fatalf("audit: %+v", entries)
	}
	// On no plan, as before: Solo offered, no packs.
	if v := r.balance(r.admin(), "/v1/me/usage/credits"); v.Kind != meter.BalanceNone || !v.CanBuy.Solo || v.CanBuy.Packs {
		t.Fatalf("after the reset: %+v", v)
	}
	// The reviewer buys Solo: Solo's 500 and the bonus, not the 5,500 test credits.
	for k, v := range soloFromAStore() {
		if err := r.tenants.SetMetadata(ctx, r.id, k, v); err != nil {
			t.Fatal(err)
		}
	}
	if v := r.balance(r.admin(), "/v1/me/usage/credits"); v.Kind != meter.BalanceSolo || v.PacksLeft != 40 || v.Left == nil || *v.Left != meter.SoloIncludedCredits+40 {
		t.Fatalf("Solo after the reset: %+v", v)
	}
	// The app sending a test pack's transaction again doesn't bring it back.
	if p, created, err := r.packs.RecordCredits(ctx, packs.CreditGrant{TenantID: r.id, Pack: "credits-5000", Credits: 5000,
		Source: packs.SourceApple, SourceTxn: "sandbox-5000", At: r.now}); err != nil || created || p.CreditsLeft != 0 {
		t.Fatalf("a set-aside pack sent again: %+v %v %v", p, created, err)
	}
	if v := r.balance(r.admin(), "/v1/me/usage/credits"); v.PacksLeft != 40 {
		t.Fatalf("after the app sent it again: %d pack credits", v.PacksLeft)
	}
}

// The pack set-aside takes every store pack on the account, so it may run
// only past the reset's guard: a reset refused for an account that isn't a
// review or test account (409) leaves its packs as they were.
func TestARefusedStoreBillingResetLeavesThePacks(t *testing.T) {
	ctx := context.Background()
	r := newCreditsRigOn(t, soloFromAStore())
	if _, _, err := r.packs.RecordCredits(ctx, packs.CreditGrant{TenantID: r.id, Pack: "credits-5000", Credits: 5000,
		Source: packs.SourceApple, SourceTxn: "paid-5000", At: r.now.Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	audit := operatoraudit.NewMemoryStore()
	resetter := &recordingResetter{}
	// The store test accounts are another account's, not this one's.
	h := NewOpsHandler([]string{opsSubject}, r.tenants, usage.NewMemoryStore(), receipt.NewMemoryStore(), nil, nil, nil,
		WithOpsControlPlane(audit, resetter), WithOpsStoreTestAccounts(tenant.NewID()), WithOpsBonusCredits(r.packs))
	mux := http.NewServeMux()
	h.Register(mux)
	rec := doOpsPost(mux, "/v1/ops/tenants/"+r.id+"/store-billing/reset", opsSubject, `{"confirm":"`+r.id+`","reason":"App Review"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("a reset of a paying account: %d %s", rec.Code, rec.Body.String())
	}
	left, err := r.packs.CreditPacks(ctx, r.id)
	if err != nil || len(left) != 1 || left[0].CreditsLeft != 5000 || left[0].RevokedAt != nil {
		t.Fatalf("its packs after the refused reset: %+v %v", left, err)
	}
	if v := r.balance(r.admin(), "/v1/me/usage/credits"); v.Kind != meter.BalanceSolo || v.PacksLeft != 5000 {
		t.Fatalf("its balance after the refused reset: %+v", v)
	}
	if entries, _ := audit.List(ctx, r.id, 10); len(entries) != 0 || len(resetter.Calls()) != 0 {
		t.Fatalf("a refused reset was audited or evicted: %+v %v", entries, resetter.Calls())
	}
}
