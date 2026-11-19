package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/appstore"
	"github.com/kiff/cloud/api/internal/appstore/appstoretest"
	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/billing"
	"github.com/kiff/cloud/api/internal/membership"
	"github.com/kiff/cloud/api/internal/packs"
	"github.com/kiff/cloud/api/internal/tenant"
)

// The app's purchase route and Apple's notifications: only Apple's
// signature is read, only the caller's own account's purchase is
// recorded, and only the billing admin buys.
func TestAppleRoutes(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	tenants := tenant.NewMemoryStore()
	id, other := tenant.NewID(), tenant.NewID()
	for _, a := range []string{id, other} {
		if err := tenants.Create(ctx, tenant.Tenant{ID: a, Slug: "a-" + a[len(a)-10:], Status: tenant.StatusActive,
			Metadata: map[string]any{tenant.MetadataKeyPlan: "solo", tenant.MetadataKeyBillingSource: "stripe"}}); err != nil {
			t.Fatal(err)
		}
	}
	catalog, _ := packs.LoadCatalog()
	accounts, ledger := appstore.NewMemoryAccounts(), packs.NewMemory()
	token, _ := accounts.Token(ctx, id)
	otherToken, _ := accounts.Token(ctx, other)
	s := appstoretest.New(t)
	v := appstore.NewTestVerifier(s.Root, "bot.tafs.app", []string{"Production", "Sandbox"}, time.Now)
	h := NewAppleHandler(v, appstore.NewProcessor(tenants, accounts, ledger, catalog, billing.NewMemoryStore(), nil), nil)
	public, authed := http.NewServeMux(), http.NewServeMux()
	h.RegisterPublic(public)
	h.Register(authed)

	signedIn := func(env, txnID, product, tok string, expires time.Time) string {
		m := map[string]any{"transactionId": txnID, "originalTransactionId": txnID, "bundleId": "bot.tafs.app", "productId": product,
			"purchaseDate": now.UnixMilli(), "appAccountToken": tok, "environment": env, "type": appstore.TypeConsumable}
		if !expires.IsZero() {
			m["type"], m["expiresDate"] = appstore.TypeSubscription, expires.UnixMilli()
		}
		return s.Sign(t, m)
	}
	signed := func(txnID, product, tok string, expires time.Time) string {
		return signedIn("Production", txnID, product, tok, expires)
	}
	post := func(who auth.Identity, txn string) (int, string) {
		body, _ := json.Marshal(map[string]string{"signed_transaction": txn})
		rr := serve(authed, who, http.MethodPost, "/v1/me/billing/apple/transactions", string(body), nil)
		return rr.Code, rr.Body.String()
	}
	admin := auth.Identity{Subject: "alice", Tenant: id, MemberRole: membership.RoleAdmin, Roles: membership.IdentityRoles(membership.RoleAdmin)}
	editor := auth.Identity{Subject: "bob", Tenant: id, MemberRole: membership.RoleEditor, Roles: membership.IdentityRoles(membership.RoleEditor)}

	// A pack, whoever bills the plan.
	if code, out := post(admin, signed("100", "bot.tafs.pack.rayo", token, time.Time{})); code != http.StatusOK || !strings.Contains(out, `"action":"pack"`) {
		t.Fatalf("a pack: %d %s", code, out)
	}
	// A test purchase on a real account: refused with what happened, and
	// nothing recorded.
	if code, out := post(admin, signedIn("Sandbox", "107", "bot.tafs.pack.forte", token, time.Time{})); code != http.StatusForbidden ||
		!strings.Contains(out, `"error":"test_purchase"`) || !strings.Contains(out, "Test purchases don't add tasks to this account.") {
		t.Fatalf("a test purchase: %d %s", code, out)
	}
	if valid, _ := ledger.Valid(ctx, id, now.Add(time.Minute)); len(valid) != 1 {
		t.Fatalf("a test purchase recorded a pack: %+v", valid)
	}
	for name, c := range map[string]struct {
		who  auth.Identity
		txn  string
		want int
	}{
		"a member":                    {editor, signed("101", "bot.tafs.pack.pico", token, time.Time{}), http.StatusForbidden},
		"another account's":           {admin, signed("102", "bot.tafs.pack.pico", otherToken, time.Time{}), http.StatusForbidden},
		"not signed by Apple":         {admin, appstoretest.New(t).Sign(t, map[string]any{"transactionId": "103"}), http.StatusBadRequest},
		"Solo on a kiff.dev plan":     {admin, signed("104", catalog.Solo.AppleProduct, token, now.AddDate(0, 1, 0)), http.StatusConflict},
		"a product KIFF doesn't sell": {admin, signed("105", "bot.tafs.other", token, time.Time{}), http.StatusUnprocessableEntity},
		"a test purchase":             {admin, signedIn("Sandbox", "106", "bot.tafs.pack.pico", token, time.Time{}), http.StatusForbidden},
	} {
		if code, out := post(c.who, c.txn); code != c.want {
			t.Errorf("%s: %d %s", name, code, out)
		}
	}

	// Apple's notification: refused unsigned, applied once signed.
	notify := func(body string) int {
		rr := httptest.NewRecorder()
		public.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/webhooks/apple", strings.NewReader(body)))
		return rr.Code
	}
	if code := notify(`{"signedPayload": "a.b.c"}`); code != http.StatusBadRequest {
		t.Fatalf("unsigned: %d", code)
	}
	n := s.Sign(t, map[string]any{"notificationType": "REFUND", "notificationUUID": "n-1",
		"data": map[string]any{"bundleId": "bot.tafs.app", "environment": "Production", "signedTransactionInfo": signed("100", "bot.tafs.pack.rayo", token, time.Time{})}})
	body, _ := json.Marshal(map[string]string{"signedPayload": n})
	if code := notify(string(body)); code != http.StatusOK {
		t.Fatalf("refund: %d", code)
	}
	if valid, _ := ledger.Valid(ctx, id, now.Add(time.Minute)); len(valid) != 0 {
		t.Fatalf("the refunded pack still counts: %+v", valid)
	}
}

// From the app, the purchase route is a phone's session's, unsigned: the
// transaction is Apple's signed proof.
func TestAppleTransactionsFromThePhone(t *testing.T) {
	r := newUsageRig(t, nil)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/me/billing/apple/transactions", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	policed := auth.DefaultRoutePolicy().Wrap(mux)
	p := newPhoneRig(t)
	if rr := serve(policed, p.on(r.admin()), http.MethodPost, "/v1/me/billing/apple/transactions", `{}`, nil); rr.Code != http.StatusOK {
		t.Fatalf("from the phone: %d %s", rr.Code, rr.Body.String())
	}
}
