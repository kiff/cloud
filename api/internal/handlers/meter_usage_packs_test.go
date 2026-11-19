package handlers

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/appstore"
	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/billing"
	"github.com/kiff/cloud/api/internal/membership"
	"github.com/kiff/cloud/api/internal/packs"
	"github.com/kiff/cloud/api/internal/tenant"
)

// packsRig is the usage read with packs, requests and the app's tokens.
func packsRig(t *testing.T, meta map[string]any) (*usageRig, *packs.Memory) {
	t.Helper()
	r := newUsageRig(t, meta)
	catalog, err := packs.LoadCatalog()
	if err != nil {
		t.Fatal(err)
	}
	r.catalog = catalog
	ledger := packs.NewMemory()
	r.mux = http.NewServeMux()
	NewMeterUsageHandler(r.tenants, r.meter, slog.Default()).WithPacks(ledger, catalog).WithRequests(ledger).
		WithApple(appstore.NewMemoryAccounts()).
		WithClock(func() time.Time { return r.now }).Register(r.mux)
	return r, ledger
}

func (r *usageRig) getAs(who auth.Identity, path string) map[string]any {
	r.t.Helper()
	rr := serve(r.mux, who, http.MethodGet, path, "", nil)
	if rr.Code != http.StatusOK {
		r.t.Fatalf("GET %s: %d %s", path, rr.Code, rr.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		r.t.Fatal(err)
	}
	return out
}

// Tasks are counted no more, and packs of tasks neither held nor sold:
// the read keeps their fields, in the shape apps from before decode, with
// nothing in them. The store sells only packs of credits, and Solo; the
// account's token stays the billing admin's.
func TestUsageReadIsInert(t *testing.T) {
	r, ledger := packsRig(t, map[string]any{tenant.MetadataKeyPlan: "solo", tenant.MetadataKeyBillingSource: "apple"})
	ctx := context.Background()
	r.meter.tasks = 30
	r.meter.byAgent = map[string]int{"taf-refunds": 30}
	r.meter.free = map[string]int{"read": 80}
	if _, _, err := ledger.Record(ctx, packs.Grant{TenantID: r.id, Pack: "rayo", Tasks: 70, Source: packs.SourceApple, SourceTxn: "2000", At: r.now.AddDate(0, 0, -40)}); err != nil {
		t.Fatal(err)
	}
	out := r.get("/v1/me/usage")
	tasks := out["tasks"].(map[string]any)
	if num(tasks["counted"]) != 0 || num(tasks["included"]) != 0 || num(tasks["packs"]) != 0 || num(tasks["used"]) != 0 || tasks["left"] != nil ||
		num(tasks["ceiling"]) != 0 || tasks["ceiling_reached"] != false || len(tasks["by_agent"].([]any)) != 0 || len(tasks["free"].(map[string]any)) != 0 {
		t.Fatalf("tasks: %v", tasks)
	}
	if list := out["pack_list"].([]any); len(list) != 0 || out["can_add_tasks"] != false || out["can_add_packs"] != false || out["trial"] != nil {
		t.Fatalf("packs of tasks: %v %v %v %v", list, out["can_add_tasks"], out["can_add_packs"], out["trial"])
	}
	store := out["store"].(map[string]any)
	if store["app_account_token"] == "" || store["app_account_token"] == nil || len(store["packs"].([]any)) != len(r.catalog.CreditPacks())-len(r.catalog.OncePacks()) || store["solo_product"] == "" {
		t.Fatalf("store: %v", store)
	}
	if again := r.get("/v1/me/usage")["store"].(map[string]any)["app_account_token"]; again != store["app_account_token"] {
		t.Fatalf("the token changed: %v %v", again, store["app_account_token"])
	}
	viewer := auth.Identity{Subject: "vic", Tenant: r.id, MemberRole: membership.RoleViewer, Roles: membership.IdentityRoles(membership.RoleViewer)}
	if out := r.getAs(viewer, "/v1/me/usage"); out["store"].(map[string]any)["app_account_token"] != nil {
		t.Fatalf("a viewer gets a token: %v", out["store"])
	}
	// A trial reads as one that hasn't started, whatever its dates.
	for _, meta := range []map[string]any{
		{tenant.MetadataKeyPlan: "trial", tenant.MetadataKeyTrialEndsAt: "2026-10-25T00:00:00Z"},
		{tenant.MetadataKeyPlan: "trial"},
	} {
		r, _ := packsRig(t, meta)
		trial, ok := r.get("/v1/me/usage")["trial"].(map[string]any)
		if !ok || trial["ends_at"] != nil || num(trial["days_left"]) != 0 || num(trial["tasks_left"]) != 0 || num(trial["actions_left"]) != 0 {
			t.Fatalf("%v: trial %v", meta, trial)
		}
	}
}

// One billing line: Apple with its subscriptions page; kiff.dev with none
// in the read (Manage billing opens the portal when pressed); a contract
// with none; nothing billed with none. Tasks have no limit on any plan.
func TestUsageBillingSource(t *testing.T) {
	for name, c := range map[string]struct {
		meta   map[string]any
		phone  bool
		source string
		manage string
	}{
		"apple":         {map[string]any{tenant.MetadataKeyPlan: "solo", tenant.MetadataKeyBillingSource: "apple"}, true, "apple", AppleManageURL},
		"stripe, web":   {map[string]any{tenant.MetadataKeyPlan: "team", tenant.MetadataKeyBillingSource: "stripe", billing.MetaStripeCustomerID: "cus_9"}, false, "stripe", ""},
		"stripe, phone": {map[string]any{tenant.MetadataKeyPlan: "team", tenant.MetadataKeyBillingSource: "stripe", billing.MetaStripeCustomerID: "cus_9"}, true, "stripe", ""},
		"contract":      {map[string]any{tenant.MetadataKeyPlan: "company"}, false, "contract", ""},
		"trial, none":   {map[string]any{tenant.MetadataKeyPlan: "trial", tenant.MetadataKeyTrialEndsAt: "2026-10-25T00:00:00Z"}, true, "", ""},
	} {
		t.Run(name, func(t *testing.T) {
			r, _ := packsRig(t, c.meta)
			who := r.admin()
			if c.phone {
				who.DeviceKey = []byte("phone")
			}
			out := r.getAs(who, "/v1/me/usage")
			b := out["billing"].(map[string]any)
			manage, _ := b["manage_url"].(string)
			if b["source"] != c.source || manage != c.manage {
				t.Fatalf("billing: %v", b)
			}
			if left := out["tasks"].(map[string]any)["left"]; left != nil || out["can_add_tasks"] != false {
				t.Fatalf("tasks left %v, can add %v", left, out["can_add_tasks"])
			}
		})
	}
}

// Requests in the read: every open one for the billing admin; a member's
// own for the member.
func TestUsageRequests(t *testing.T) {
	r, ledger := packsRig(t, map[string]any{tenant.MetadataKeyPlan: "team"})
	ctx := context.Background()
	end := r.now.AddDate(0, 0, 9)
	for _, who := range []string{"bob", "carol"} {
		if _, err := ledger.Ask(ctx, packs.Request{TenantID: r.id, AskedBy: who, Pack: "pico", CreatedAt: r.now.Add(-time.Hour), ExpiresAt: end}); err != nil {
			t.Fatal(err)
		}
	}
	if reqs := r.get("/v1/me/usage")["requests"].([]any); len(reqs) != 2 {
		t.Fatalf("the admin's: %v", reqs)
	}
	bob := auth.Identity{Subject: "bob", Tenant: r.id, MemberRole: membership.RoleEditor, Roles: membership.IdentityRoles(membership.RoleEditor)}
	reqs := r.getAs(bob, "/v1/me/usage")["requests"].([]any)
	if len(reqs) != 1 || reqs[0].(map[string]any)["asked_by"] != "bob" || reqs[0].(map[string]any)["status"] != "open" {
		t.Fatalf("bob's: %v", reqs)
	}
}
