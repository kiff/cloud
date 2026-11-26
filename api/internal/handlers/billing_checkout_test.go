package handlers

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/appstore"
	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/billing"
	"github.com/kiff/cloud/api/internal/membership"
	"github.com/kiff/cloud/api/internal/packs"
	"github.com/kiff/cloud/api/internal/tenant"
)

type fakeCheckout struct {
	got  billing.Checkout
	pack billing.PackCheckout
}

func (f *fakeCheckout) CreatePackCheckout(_ context.Context, c billing.PackCheckout) (string, error) {
	f.pack = c
	return "https://checkout.stripe.com/c/pay/cs_pack", nil
}

func (f *fakeCheckout) CreateCheckout(_ context.Context, c billing.Checkout) (string, error) {
	f.got = c
	return "https://checkout.stripe.com/c/pay/cs_test", nil
}

type fakeMembers []membership.Member

func (f fakeMembers) List(context.Context, string) ([]membership.Member, error) { return f, nil }

// The v1 checkout: the trial starts, or goes on, at
// checkout with a card; Team's seats are its people; a subscribed,
// internal or company account doesn't check out again.
func TestBillingCheckout(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	run := func(meta map[string]any, role membership.Role, body string, members fakeMembers) (int, *fakeCheckout) {
		t.Helper()
		tenants := tenant.NewMemoryStore()
		id := tenant.NewID()
		if err := tenants.Create(ctx, tenant.Tenant{ID: id, Slug: "acme", Status: tenant.StatusActive, Metadata: meta}); err != nil {
			t.Fatal(err)
		}
		f := &fakeCheckout{}
		mux := http.NewServeMux()
		NewBillingCheckoutHandler(tenants, f, members, "https://app.kiff.dev", nil).WithClock(func() time.Time { return now }).Register(mux)
		who := auth.Identity{Subject: "alice", Tenant: id, MemberRole: role, Roles: membership.IdentityRoles(role)}
		return serve(mux, who, http.MethodPost, "/v1/me/billing/checkout", body, nil).Code, f
	}
	company := `{"plan": "team"}`

	// Solo is sold in the app stores: no checkout here, nothing opened.
	if code, f := run(nil, membership.RoleAdmin, `{"plan": "solo"}`, nil); code != http.StatusBadRequest || f.got.Plan != "" {
		t.Fatalf("solo on the web: %d %+v", code, f.got)
	}
	// No free trial: a plan is billed from its first day, whatever a trial
	// once said.
	code, f := run(nil, membership.RoleAdmin, company, nil)
	if code != http.StatusOK || !f.got.TrialEnd.IsZero() || f.got.Plan != "team" || f.got.SuccessURL != "https://app.kiff.dev/dashboard/settings?billing=done" {
		t.Fatalf("no plan: %d %+v", code, f.got)
	}
	end := now.AddDate(0, 0, 3)
	if code, f := run(map[string]any{tenant.MetadataKeyPlan: "trial", tenant.MetadataKeyTrialEndsAt: end.Format(time.RFC3339)}, membership.RoleAdmin, company, nil); code != http.StatusOK || !f.got.TrialEnd.IsZero() {
		t.Fatalf("a trial running: %d %+v", code, f.got)
	}
	if code, f := run(map[string]any{tenant.MetadataKeyPlan: "trial", tenant.MetadataKeyTrialEndsAt: now.AddDate(0, 0, -1).Format(time.RFC3339)}, membership.RoleAdmin, company, nil); code != http.StatusOK || !f.got.TrialEnd.IsZero() {
		t.Fatalf("ended: %d %+v", code, f.got)
	}
	// Team: its admins and editors, at least 1; approvers are free.
	people := fakeMembers{{Role: membership.RoleAdmin}, {Role: membership.RoleEditor}, {Role: membership.RoleEditor}, {Role: membership.RoleApprover}}
	if code, f := run(nil, membership.RoleAdmin, `{"plan": "team"}`, people); code != http.StatusOK || f.got.Seats != 3 || f.got.Plan != "team" {
		t.Fatalf("team: %d %+v", code, f.got)
	}
	if code, f := run(nil, membership.RoleAdmin, `{"plan": "team"}`, nil); code != http.StatusOK || f.got.Seats != 1 {
		t.Fatalf("team of one: %d %+v", code, f.got)
	}
	for name, c := range map[string]struct {
		meta map[string]any
		role membership.Role
		body string
		want int
	}{
		"already subscribed": {map[string]any{tenant.MetadataKeyPlan: "team", billing.MetaBillingStatus: "active"}, membership.RoleAdmin, company, http.StatusConflict},
		"internal":           {map[string]any{tenant.MetadataKeyPlan: "internal"}, membership.RoleAdmin, company, http.StatusConflict},
		"a viewer":           {nil, membership.RoleViewer, company, http.StatusForbidden},
		"no such plan":       {nil, membership.RoleAdmin, `{"plan": "gold"}`, http.StatusBadRequest},
		"yearly":             {nil, membership.RoleAdmin, `{"plan": "team", "yearly": true}`, http.StatusBadRequest},
		// Google Play's Solo is managed in Google Play: no
		// Stripe plan beside it.
		"billed by google": {map[string]any{tenant.MetadataKeyPlan: "solo", tenant.MetadataKeyBillingSource: "google"}, membership.RoleAdmin, company, http.StatusConflict},
	} {
		if code, _ := run(c.meta, c.role, c.body, nil); code != c.want {
			t.Errorf("%s: %d, want %d", name, code, c.want)
		}
	}

	// Solo → Team on an account Apple bills (the shared-tafs design, option a): Team
	// starts now, and the Solo days Apple was paid for are Team days,
	// unbilled until they end, whatever billing status Apple set.
	paidUntil := now.AddDate(0, 0, 12)
	appleSolo := map[string]any{tenant.MetadataKeyPlan: "solo", tenant.MetadataKeyBillingSource: "apple",
		billing.MetaBillingStatus: "active", appstore.MetaExpiresAt: paidUntil.Format(time.RFC3339)}
	if code, f := run(appleSolo, membership.RoleAdmin, `{"plan": "team"}`, people); code != http.StatusOK || !f.got.TrialEnd.Equal(paidUntil) || !f.got.AppleCredit || f.got.Seats != 3 {
		t.Fatalf("apple solo to team: %d %+v", code, f.got)
	}
	// Solo days that end within the hour: nothing left to credit.
	appleSolo[appstore.MetaExpiresAt] = now.Add(30 * time.Minute).Format(time.RFC3339)
	if code, f := run(appleSolo, membership.RoleAdmin, `{"plan": "team"}`, nil); code != http.StatusOK || !f.got.TrialEnd.IsZero() || f.got.AppleCredit {
		t.Fatalf("apple solo ending: %d %+v", code, f.got)
	}
	// A store free trial (none is offered on Solo today) isn't a paid Solo: Team isn't
	// sold beside it, since the trial would become paid Solo and the person would pay twice.
	appleTrial := map[string]any{tenant.MetadataKeyPlan: "trial", tenant.MetadataKeyBillingSource: "apple",
		tenant.MetadataKeyTrialEndsAt: end.Format(time.RFC3339), appstore.MetaExpiresAt: end.Format(time.RFC3339)}
	if code, f := run(appleTrial, membership.RoleAdmin, `{"plan": "team"}`, nil); code != http.StatusConflict || f.got.Plan != "" {
		t.Fatalf("apple trial to team: %d %+v", code, f.got)
	}
}

// A pack is a one-time checkout for the billing admin, whoever bills the
// plan; it carries the account, the pack's price and the open request it
// answers.
func TestBillingCheckoutPacks(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	// The checkout still sells a pack with a Stripe price; production's
	// table has none now, so the test brings its own. vonk has none: it is
	// no longer sold.
	catalog, err := packs.ParseCatalog([]byte(`{"packs": [
		{"id": "pico", "name": "Pico", "tasks": 12, "stripe_price": "kiff_pack_pico", "apple_product": "bot.tafs.pack.pico"},
		{"id": "vonk", "name": "Vonk", "tasks": 30, "apple_product": "bot.tafs.pack.vonk"},
		{"id": "rayo", "name": "Rayo", "tasks": 70, "stripe_price": "kiff_pack_rayo", "apple_product": "bot.tafs.pack.rayo"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	requests := packs.NewMemory()
	tenants := tenant.NewMemoryStore()
	ids := map[string]string{}
	for name, meta := range map[string]map[string]any{
		"apple":    {tenant.MetadataKeyPlan: "solo", tenant.MetadataKeyBillingSource: "apple"},
		"stripe":   {tenant.MetadataKeyPlan: "team", tenant.MetadataKeyBillingSource: "stripe", billing.MetaStripeCustomerID: "cus_1"},
		"internal": {tenant.MetadataKeyPlan: "internal"},
	} {
		ids[name] = tenant.NewID()
		if err := tenants.Create(ctx, tenant.Tenant{ID: ids[name], Slug: name, Status: tenant.StatusActive, Metadata: meta}); err != nil {
			t.Fatal(err)
		}
	}
	open, _ := requests.Ask(ctx, packs.Request{TenantID: ids["stripe"], AskedBy: "bob", Pack: "rayo", CreatedAt: now, ExpiresAt: now.AddDate(0, 0, 5)})
	declined, _ := requests.Ask(ctx, packs.Request{TenantID: ids["stripe"], AskedBy: "carol", Pack: "pico", CreatedAt: now, ExpiresAt: now.AddDate(0, 0, 5)})
	if _, err := requests.Decline(ctx, ids["stripe"], declined.ID, "alice", "", now); err != nil {
		t.Fatal(err)
	}
	f := &fakeCheckout{}
	mux := http.NewServeMux()
	NewBillingCheckoutHandler(tenants, f, nil, "https://app.kiff.dev", nil).WithPacks(catalog, requests).WithClock(func() time.Time { return now }).Register(mux)
	post := func(account string, role membership.Role, body string) int {
		who := auth.Identity{Subject: "alice", Tenant: ids[account], MemberRole: role, Roles: membership.IdentityRoles(role)}
		return serve(mux, who, http.MethodPost, "/v1/me/billing/checkout", body, nil).Code
	}
	// Apple bills the plan; a pack is still sold here.
	if code := post("apple", membership.RoleAdmin, `{"pack": "pico"}`); code != http.StatusOK || f.pack.Pack != "pico" || f.pack.PriceKey != "kiff_pack_pico" || f.pack.TenantID != ids["apple"] {
		t.Fatalf("apple-billed: %d %+v", code, f.pack)
	}
	if code := post("stripe", membership.RoleAdmin, `{"pack": "rayo", "request_id": "`+open.ID+`"}`); code != http.StatusOK ||
		f.pack.RequestID != open.ID || f.pack.AnsweredBy != "alice" || f.pack.CustomerID != "cus_1" {
		t.Fatalf("answering a request: %d %+v", code, f.pack)
	}
	for name, c := range map[string]struct {
		account string
		role    membership.Role
		body    string
		want    int
	}{
		"a member":            {"stripe", membership.RoleEditor, `{"pack": "rayo"}`, http.StatusForbidden},
		"no such pack":        {"stripe", membership.RoleAdmin, `{"pack": "giga"}`, http.StatusBadRequest},
		"a pack not sold":     {"stripe", membership.RoleAdmin, `{"pack": "vonk"}`, http.StatusConflict},
		"no limit":            {"internal", membership.RoleAdmin, `{"pack": "rayo"}`, http.StatusConflict},
		"an answered request": {"stripe", membership.RoleAdmin, `{"pack": "pico", "request_id": "` + declined.ID + `"}`, http.StatusConflict},
		"another's request":   {"apple", membership.RoleAdmin, `{"pack": "rayo", "request_id": "` + open.ID + `"}`, http.StatusNotFound},
	} {
		if code := post(c.account, c.role, c.body); code != c.want {
			t.Errorf("%s: %d, want %d", name, code, c.want)
		}
	}
	// A pack no longer sold, asked for before it stopped: the admin's
	// approval opens nothing at Stripe.
	old, _ := requests.Ask(ctx, packs.Request{TenantID: ids["stripe"], AskedBy: "dave", Pack: "vonk", CreatedAt: now, ExpiresAt: now.AddDate(0, 0, 5)})
	f.pack = billing.PackCheckout{}
	if code := post("stripe", membership.RoleAdmin, `{"pack": "vonk", "request_id": "`+old.ID+`"}`); code != http.StatusConflict || f.pack.Pack != "" {
		t.Fatalf("approving a request for a pack not sold: %d %+v", code, f.pack)
	}
}

// A pack of credits is Solo's, bought in the app: kiff.dev's checkout
// refuses it and opens nothing.
func TestBillingCheckoutRefusesCreditPacks(t *testing.T) {
	ctx := context.Background()
	catalog, err := packs.ParseCatalog([]byte(`{"packs": [
		{"id": "pico", "name": "Pico", "tasks": 12, "stripe_price": "kiff_pack_pico", "apple_product": "bot.tafs.pack.pico"},
		{"id": "c-test", "name": "Test", "credits": 500, "apple_product": "bot.tafs.credits.test"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	tenants := tenant.NewMemoryStore()
	id := tenant.NewID()
	if err := tenants.Create(ctx, tenant.Tenant{ID: id, Slug: "solo", Status: tenant.StatusActive,
		Metadata: map[string]any{tenant.MetadataKeyPlan: "solo", tenant.MetadataKeyBillingSource: "apple"}}); err != nil {
		t.Fatal(err)
	}
	f := &fakeCheckout{}
	mux := http.NewServeMux()
	NewBillingCheckoutHandler(tenants, f, nil, "https://app.kiff.dev", nil).WithPacks(catalog, packs.NewMemory()).Register(mux)
	who := auth.Identity{Subject: "alice", Tenant: id, MemberRole: membership.RoleAdmin, Roles: membership.IdentityRoles(membership.RoleAdmin)}
	rec := serve(mux, who, http.MethodPost, "/v1/me/billing/checkout", `{"pack": "c-test"}`, nil)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "credit_packs_in_the_app") || f.pack.Pack != "" {
		t.Fatalf("a pack of credits: %d %s %+v", rec.Code, rec.Body.String(), f.pack)
	}
	if rec := serve(mux, who, http.MethodPost, "/v1/me/billing/checkout", `{"pack": "pico"}`, nil); rec.Code != http.StatusOK || f.pack.Pack != "pico" {
		t.Fatalf("a pack of tasks: %d %+v", rec.Code, f.pack)
	}
}
