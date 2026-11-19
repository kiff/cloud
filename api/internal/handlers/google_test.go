package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/appstore"
	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/billing"
	"github.com/kiff/cloud/api/internal/membership"
	"github.com/kiff/cloud/api/internal/packs"
	"github.com/kiff/cloud/api/internal/playstore"
	"github.com/kiff/cloud/api/internal/playstore/playstoretest"
	"github.com/kiff/cloud/api/internal/tenant"
)

const (
	googleAud   = "https://api.kiff.dev/webhooks/google"
	googleEmail = "play-rtdn@example.com"
)

// The app's Google Play route and Play's notifications: each purchase is
// read back from Play, only the caller's own account's is recorded, only
// the billing admin buys, and a push without Google's token is refused.
func TestGoogleRoutes(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	tenants := tenant.NewMemoryStore()
	id, other, stripe := tenant.NewID(), tenant.NewID(), tenant.NewID()
	for a, meta := range map[string]map[string]any{id: nil, other: nil,
		stripe: {tenant.MetadataKeyPlan: "solo", tenant.MetadataKeyBillingSource: "stripe"}} {
		if err := tenants.Create(ctx, tenant.Tenant{ID: a, Slug: "a-" + a[len(a)-10:], Status: tenant.StatusActive, Metadata: meta}); err != nil {
			t.Fatal(err)
		}
	}
	catalog, _ := packs.LoadCatalog()
	solo := catalog.Solo.GoogleProduct
	accounts, ledger := appstore.NewMemoryAccounts(), packs.NewMemory()
	token, _ := accounts.Token(ctx, id)
	otherToken, _ := accounts.Token(ctx, other)
	stripeToken, _ := accounts.Token(ctx, stripe)
	play, google := playstoretest.NewPlay(t, "bot.tafs.app"), playstoretest.NewGoogle(t)
	b := playstore.NewBilling(play.Client(t), appstore.NewProcessor(tenants, accounts, ledger, catalog, billing.NewMemoryStore(), nil),
		accounts, tenants, playstore.NewMemoryPurchases(), catalog, nil)
	h := NewGoogleHandler(b, playstore.NewPushVerifier(googleAud, googleEmail).WithJWKS(google.JWKSURL()), nil)
	public, authed := http.NewServeMux(), http.NewServeMux()
	h.RegisterPublic(public)
	h.Register(authed)

	post := func(who auth.Identity, product, purchase string) (int, string) {
		body, _ := json.Marshal(map[string]string{"product": product, "purchase_token": purchase})
		rr := serve(authed, who, http.MethodPost, "/v1/me/billing/google/purchases", string(body), nil)
		return rr.Code, rr.Body.String()
	}
	admin := auth.Identity{Subject: "alice", Tenant: id, MemberRole: membership.RoleAdmin, Roles: membership.IdentityRoles(membership.RoleAdmin)}
	editor := auth.Identity{Subject: "bob", Tenant: id, MemberRole: membership.RoleEditor, Roles: membership.IdentityRoles(membership.RoleEditor)}
	stripeAdmin := auth.Identity{Subject: "sam", Tenant: stripe, MemberRole: membership.RoleAdmin, Roles: membership.IdentityRoles(membership.RoleAdmin)}

	// Solo: the plan, billed by Google, acknowledged once.
	play.SetSubscription("sub-1", playstoretest.Subscription(solo, playstore.StateActive, token, now, now.AddDate(0, 1, 0)))
	if code, out := post(admin, solo, "sub-1"); code != http.StatusOK || !strings.Contains(out, `"action":"solo"`) || !strings.Contains(out, `"acknowledged":true`) {
		t.Fatalf("Solo: %d %s", code, out)
	}
	if code, out := post(admin, solo, "sub-1"); code != http.StatusOK || !strings.Contains(out, `"duplicate":true`) || play.Acks("sub-1") != 1 {
		t.Fatalf("Solo again: %d %s (acks %d)", code, out, play.Acks("sub-1"))
	}
	got, _ := tenants.Get(ctx, id)
	if tenant.BillingSourceOf(got) != tenant.BillingGoogle {
		t.Fatalf("source %q", tenant.BillingSourceOf(got))
	}
	// A pack.
	play.SetProduct("bot.tafs.pack.rayo", "p-1", playstoretest.Product(token, now))
	if code, out := post(admin, "bot.tafs.pack.rayo", "p-1"); code != http.StatusOK || !strings.Contains(out, `"action":"pack"`) {
		t.Fatalf("a pack: %d %s", code, out)
	}
	play.SetProduct("bot.tafs.pack.pico", "p-other", playstoretest.Product(otherToken, now))
	testPack := playstoretest.Product(token, now)
	testPack["purchaseType"] = 0
	play.SetProduct("bot.tafs.pack.pico", "p-test", testPack)
	play.SetSubscription("sub-stripe", playstoretest.Subscription(solo, playstore.StateActive, stripeToken, now, now.AddDate(0, 1, 0)))
	for name, c := range map[string]struct {
		who            auth.Identity
		product, token string
		want           int
		code           string
	}{
		"a member":                    {editor, "bot.tafs.pack.pico", "p-other", http.StatusForbidden, ""},
		"another account's":           {admin, "bot.tafs.pack.pico", "p-other", http.StatusForbidden, "not_this_account"},
		"a test purchase":             {admin, "bot.tafs.pack.pico", "p-test", http.StatusForbidden, "test_purchase"},
		"a product KIFF doesn't sell": {admin, "bot.tafs.other", "p-1", http.StatusUnprocessableEntity, "unknown_product"},
		"Solo on a kiff.dev plan":     {stripeAdmin, solo, "sub-stripe", http.StatusConflict, "billed_elsewhere"},
		"a token Play doesn't know":   {admin, "bot.tafs.pack.pico", "nope", http.StatusBadRequest, ""},
	} {
		code, out := post(c.who, c.product, c.token)
		if code != c.want || (c.code != "" && !strings.Contains(out, `"error":"`+c.code+`"`)) {
			t.Errorf("%s: %d %s", name, code, out)
		}
	}
	if play.Acks("sub-stripe") != 0 {
		t.Fatal("a refused Solo was acknowledged")
	}

	// Play's notification: refused without Google's token, applied with it.
	notify := func(bearer string, body []byte) int {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/webhooks/google", bytes.NewReader(body))
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		public.ServeHTTP(rr, req)
		return rr.Code
	}
	voided := playstoretest.Push("m-1", map[string]any{"version": "1.0", "packageName": "bot.tafs.app",
		"voidedPurchaseNotification": map[string]any{"purchaseToken": "p-1", "orderId": "GPA.1", "productType": 2, "refundType": 1}})
	for name, bearer := range map[string]string{
		"no token":         "",
		"another audience": google.PushToken("https://evil.example/hook", googleEmail, now),
		"another account":  google.PushToken(googleAud, "someone@example.com", now),
		"forged":           playstoretest.NewGoogle(t).PushToken(googleAud, googleEmail, now),
	} {
		if code := notify(bearer, voided); code != http.StatusUnauthorized {
			t.Errorf("%s: %d", name, code)
		}
	}
	if valid, _ := ledger.Valid(ctx, id, now.Add(time.Minute)); len(valid) != 1 {
		t.Fatalf("a refused push changed the packs: %+v", valid)
	}
	if code := notify(google.PushToken(googleAud, googleEmail, now), voided); code != http.StatusOK {
		t.Fatalf("voided: %d", code)
	}
	if valid, _ := ledger.Valid(ctx, id, now.Add(time.Minute)); len(valid) != 0 {
		t.Fatalf("the refunded pack still counts: %+v", valid)
	}
	// Renewed: read back from Play.
	renewed := playstoretest.Subscription(solo, playstore.StateActive, token, now, now.AddDate(0, 2, 0))
	renewed["acknowledgementState"] = playstore.AckDone
	play.SetSubscription("sub-1", renewed)
	push := playstoretest.Push("m-2", map[string]any{"packageName": "bot.tafs.app",
		"subscriptionNotification": map[string]any{"notificationType": playstore.SubRenewed, "purchaseToken": "sub-1", "subscriptionId": solo}})
	if code := notify(google.PushToken(googleAud, googleEmail, now), push); code != http.StatusOK {
		t.Fatalf("renewed: %d", code)
	}
	if got, _ := tenants.Get(ctx, id); got.Metadata[playstore.MetaExpiresAt] != now.AddDate(0, 2, 0).Format(time.RFC3339) {
		t.Fatalf("renewal: %v", got.Metadata[playstore.MetaExpiresAt])
	}
}

// Without Google Play's config, both routes say it isn't set up.
func TestGoogleRoutesNotSetUp(t *testing.T) {
	h := NewGoogleHandler(nil, nil, nil)
	public, authed := http.NewServeMux(), http.NewServeMux()
	h.RegisterPublic(public)
	h.Register(authed)
	admin := auth.Identity{Subject: "alice", Tenant: tenant.NewID(), MemberRole: membership.RoleAdmin, Roles: membership.IdentityRoles(membership.RoleAdmin)}
	if rr := serve(authed, admin, http.MethodPost, "/v1/me/billing/google/purchases", `{"product":"a","purchase_token":"b"}`, nil); rr.Code != http.StatusServiceUnavailable ||
		!strings.Contains(rr.Body.String(), "google_play_not_set_up") {
		t.Fatalf("purchase: %d %s", rr.Code, rr.Body.String())
	}
	rr := httptest.NewRecorder()
	public.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/webhooks/google", strings.NewReader(`{}`)))
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("webhook: %d", rr.Code)
	}
}

// From the app, the purchase route is a phone's session's, unsigned: Play's
// purchase, read back, is the proof.
func TestGooglePurchasesFromThePhone(t *testing.T) {
	r := newUsageRig(t, nil)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/me/billing/google/purchases", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	policed := auth.DefaultRoutePolicy().Wrap(mux)
	p := newPhoneRig(t)
	if rr := serve(policed, p.on(r.admin()), http.MethodPost, "/v1/me/billing/google/purchases", `{}`, nil); rr.Code != http.StatusOK {
		t.Fatalf("from the phone: %d %s", rr.Code, rr.Body.String())
	}
}

// Usage lists Google Play's products only when it's set up, and says Google
// bills a Play-billed account, with Play's subscriptions page. Only packs of
// credits are sold.
func TestUsageGooglePlayStore(t *testing.T) {
	catalog, err := packs.ParseCatalog([]byte(`{"solo": {"apple_product": "bot.tafs.solo.monthly", "google_product": "bot.tafs.solo.monthly"},
		"packs": [{"id": "c-small", "name": "Small", "credits": 100, "apple_product": "bot.tafs.c.small", "google_product": "bot.tafs.c.small"},
		{"id": "pico", "name": "Pico", "tasks": 12, "stripe_price": "kiff_pack_pico", "apple_product": "bot.tafs.pack.pico", "google_product": "bot.tafs.pack.pico"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, google := range []bool{false, true} {
		r := newUsageRig(t, map[string]any{tenant.MetadataKeyPlan: "solo", tenant.MetadataKeyBillingSource: "google"})
		r.mux = http.NewServeMux()
		h := NewMeterUsageHandler(r.tenants, r.meter, slog.Default()).WithPacks(packs.NewMemory(), catalog).
			WithApple(appstore.NewMemoryAccounts()).WithClock(func() time.Time { return r.now })
		if google {
			h.WithGoogle()
		}
		h.Register(r.mux)
		out := r.get("/v1/me/usage")
		store := out["store"].(map[string]any)
		if len(store["packs"].([]any)) != 1 {
			t.Fatalf("a pack of tasks is sold: %v", store["packs"])
		}
		pack := store["packs"].([]any)[0].(map[string]any)
		if num(pack["credits"]) != 100 || num(pack["tasks"]) != 0 || num(pack["months"]) != 0 {
			t.Fatalf("a pack of credits: %v", pack)
		}
		if google && (store["google_solo_product"] != catalog.Solo.GoogleProduct || pack["google_product"] != catalog.Packs[0].GoogleProduct || pack["apple_product"] == nil) {
			t.Fatalf("set up: %v", store)
		}
		if _, ok := store["google_solo_product"]; !google && ok {
			t.Fatalf("not set up, yet listed: %v", store)
		}
		if _, ok := pack["google_product"]; !google && ok {
			t.Fatalf("not set up, yet a pack lists it: %v", pack)
		}
		bill := out["billing"].(map[string]any)
		if bill["source"] != "google" || bill["manage_url"] != GoogleManageURL {
			t.Fatalf("billing: %v", bill)
		}
	}
}
