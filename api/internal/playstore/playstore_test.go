package playstore_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kiff/cloud/api/internal/appstore"
	"github.com/kiff/cloud/api/internal/billing"
	"github.com/kiff/cloud/api/internal/packs"
	"github.com/kiff/cloud/api/internal/playstore"
	"github.com/kiff/cloud/api/internal/playstore/playstoretest"
	"github.com/kiff/cloud/api/internal/tenant"
	"github.com/kiff/cloud/api/internal/testdb"
)

const pkg = "bot.tafs.app"

var now = time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)

type rig struct {
	play      *playstoretest.Play
	tenants   *tenant.MemoryStore
	accounts  *appstore.MemoryAccounts
	ledger    *packs.Memory
	purchases *playstore.MemoryPurchases
	catalog   packs.Catalog
	b         *playstore.Billing
	solo      string
}

func newRig(t *testing.T, testTenants ...string) *rig {
	t.Helper()
	c, err := packs.LoadCatalog()
	if err != nil {
		t.Fatal(err)
	}
	r := &rig{play: playstoretest.NewPlay(t, pkg), tenants: tenant.NewMemoryStore(), accounts: appstore.NewMemoryAccounts(),
		ledger: packs.NewMemory(), purchases: playstore.NewMemoryPurchases(), catalog: c, solo: c.Solo.GoogleProduct}
	p := appstore.NewProcessor(r.tenants, r.accounts, r.ledger, c, billing.NewMemoryStore(), nil).WithSandboxAccounts(testTenants...)
	r.b = playstore.NewBilling(r.play.Client(t), p, r.accounts, r.tenants, r.purchases, c, nil).WithClock(func() time.Time { return now })
	return r
}

func (r *rig) account(t *testing.T, meta map[string]any) (id, token string) {
	t.Helper()
	id = tenant.NewID()
	if err := r.tenants.Create(context.Background(), tenant.Tenant{ID: id, Slug: "a-" + id[len(id)-10:], Status: tenant.StatusActive, Metadata: meta}); err != nil {
		t.Fatal(err)
	}
	token, err := r.accounts.Token(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return id, token
}

func (r *rig) get(t *testing.T, id string) tenant.Tenant {
	t.Helper()
	got, err := r.tenants.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func subNote(token string, kind int) playstore.DeveloperNotification {
	n := playstore.DeveloperNotification{PackageName: pkg, MessageID: "m-" + token}
	n.SubscriptionNotification = &struct {
		NotificationType int    `json:"notificationType"`
		PurchaseToken    string `json:"purchaseToken"`
		SubscriptionID   string `json:"subscriptionId"`
	}{NotificationType: kind, PurchaseToken: token}
	return n
}

func voidedNote(token string, productType int) playstore.DeveloperNotification {
	n := playstore.DeveloperNotification{PackageName: pkg, MessageID: "v-" + token}
	n.VoidedPurchaseNotification = &struct {
		PurchaseToken string `json:"purchaseToken"`
		OrderID       string `json:"orderId"`
		ProductType   int    `json:"productType"`
		RefundType    int    `json:"refundType"`
	}{PurchaseToken: token, OrderID: "GPA.1", ProductType: productType, RefundType: 1}
	return n
}

// Solo bought on Google Play: read back from Play, the plan is Solo with
// Google as its billing source and Play's period, the purchase recorded and
// acknowledged once; sent again, nothing changes and Play isn't asked to
// acknowledge it twice.
func TestSoloThroughGooglePlay(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	id, token := r.account(t, nil)
	end := now.AddDate(0, 1, 0)
	r.play.SetSubscription("sub-1", playstoretest.Subscription(r.solo, playstore.StateActive, token, now.Add(-time.Minute), end))

	out, err := r.b.Purchased(ctx, r.solo, "sub-1", appstore.Caller{TenantID: id})
	if err != nil || out.Action != "solo" || !out.Acknowledged || out.Duplicate {
		t.Fatalf("purchase: %+v %v", out, err)
	}
	got := r.get(t, id)
	if tenant.PlanFor(got).Canonical != tenant.PlanSolo || tenant.BillingSourceOf(got) != tenant.BillingGoogle ||
		got.Metadata[playstore.MetaPurchaseToken] != "sub-1" || got.Metadata[playstore.MetaExpiresAt] != end.Format(time.RFC3339) {
		t.Fatalf("plan: %s %q %v", tenant.PlanFor(got).Canonical, tenant.BillingSourceOf(got), got.Metadata)
	}
	if from, to := tenant.Period(got, now); !to.Equal(end) || from.After(now) {
		t.Fatalf("period [%v, %v)", from, to)
	}
	if p, ok, _ := r.purchases.Get(ctx, "sub-1"); !ok || p.TenantID != id || p.AcknowledgedAt == nil || p.Kind != playstore.KindSubscription {
		t.Fatalf("recorded: %+v %v", p, ok)
	}
	// Sent again: the same, recorded once, acknowledged once.
	out, err = r.b.Purchased(ctx, r.solo, "sub-1", appstore.Caller{TenantID: id})
	if err != nil || out.Action != "solo" || out.Acknowledged || !out.Duplicate {
		t.Fatalf("sent again: %+v %v", out, err)
	}
	if n := r.play.Acks("sub-1"); n != 1 {
		t.Fatalf("acknowledged %d times", n)
	}

	// Renewed: Play's notification, read back, carries the new period.
	next := end.AddDate(0, 1, 0)
	renewed := playstoretest.Subscription(r.solo, playstore.StateActive, token, now.Add(-time.Minute), next)
	renewed["acknowledgementState"] = playstore.AckDone
	r.play.SetSubscription("sub-1", renewed)
	if out, err := r.b.Notify(ctx, subNote("sub-1", playstore.SubRenewed)); err != nil || out.Action != "solo" || out.TenantID != id {
		t.Fatalf("renewed: %+v %v", out, err)
	}
	if got := r.get(t, id); got.Metadata[playstore.MetaExpiresAt] != next.Format(time.RFC3339) {
		t.Fatalf("renewal's period: %v", got.Metadata[playstore.MetaExpiresAt])
	}
	// In the grace period: the plan stays, past due.
	grace := playstoretest.Subscription(r.solo, playstore.StateInGrace, token, now.Add(-time.Minute), next)
	grace["acknowledgementState"] = playstore.AckDone
	r.play.SetSubscription("sub-1", grace)
	if out, err := r.b.Notify(ctx, subNote("sub-1", playstore.SubInGracePeriod)); err != nil || out.Action != "grace" {
		t.Fatalf("grace: %+v %v", out, err)
	}
	if got := r.get(t, id); tenant.PlanFor(got).Canonical != tenant.PlanSolo || got.Metadata[billing.MetaBillingStatus] != billing.StatusPastDue {
		t.Fatalf("grace: %v", got.Metadata)
	}
	// Revoked: Play's subscription expired, and the plan is taken back.
	revoked := playstoretest.Subscription(r.solo, playstore.StateExpired, token, now.Add(-time.Minute), now)
	revoked["acknowledgementState"] = playstore.AckDone
	r.play.SetSubscription("sub-1", revoked)
	if out, err := r.b.Notify(ctx, subNote("sub-1", playstore.SubRevoked)); err != nil || out.Action != "ended" {
		t.Fatalf("revoked: %+v %v", out, err)
	}
	if got := r.get(t, id); tenant.PlanFor(got).Canonical != tenant.PlanTrial || tenant.BillingSourceOf(got) != tenant.BillingNone {
		t.Fatalf("revoked: %s %q", tenant.PlanFor(got).Canonical, tenant.BillingSourceOf(got))
	}
}

// Google Play's free trial is KIFF's trial; a resubscription names the
// subscription it replaces, and takes its place.
func TestFreeTrialAndALinkedResubscription(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	id, token := r.account(t, nil)
	trial := playstoretest.Subscription(r.solo, playstore.StateActive, token, now, now.AddDate(0, 0, 7))
	trial["lineItems"].([]any)[0].(map[string]any)["offerPhase"] = map[string]any{"freeTrial": map[string]any{}}
	r.play.SetSubscription("sub-t", trial)
	if out, err := r.b.Purchased(ctx, r.solo, "sub-t", appstore.Caller{TenantID: id}); err != nil || out.Action != "trialing" {
		t.Fatalf("trial: %+v %v", out, err)
	}
	if end, ok := tenant.TrialEndsAt(r.get(t, id)); !ok || !end.Equal(now.AddDate(0, 0, 7)) {
		t.Fatalf("trial ends %v", end)
	}
	next := playstoretest.Subscription(r.solo, playstore.StateActive, token, now, now.AddDate(0, 1, 0))
	next["linkedPurchaseToken"] = "sub-t"
	r.play.SetSubscription("sub-u", next)
	if out, err := r.b.Purchased(ctx, r.solo, "sub-u", appstore.Caller{TenantID: id}); err != nil || out.Action != "solo" {
		t.Fatalf("linked: %+v %v", out, err)
	}
	if got := r.get(t, id); got.Metadata[playstore.MetaPurchaseToken] != "sub-u" {
		t.Fatalf("token: %v", got.Metadata[playstore.MetaPurchaseToken])
	}
	// A second, unlinked subscription (another Google account) is refused.
	r.play.SetSubscription("sub-v", playstoretest.Subscription(r.solo, playstore.StateActive, token, now, now.AddDate(0, 1, 0)))
	if _, err := r.b.Purchased(ctx, r.solo, "sub-v", appstore.Caller{TenantID: id}); !errors.Is(err, appstore.ErrBilledElsewhere) {
		t.Fatalf("a second subscription: %v", err)
	}
	if r.play.Acks("sub-v") != 0 {
		t.Fatal("a refused subscription was acknowledged")
	}
}

// A pack: recorded once per purchase token, not acknowledged (the app
// consumes it); a refund's voided notification takes it back.
func TestPackThroughGooglePlay(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	id, token := r.account(t, map[string]any{tenant.MetadataKeyPlan: "team", tenant.MetadataKeyBillingSource: "stripe"})
	r.play.SetProduct("bot.tafs.pack.rayo", "p-1", playstoretest.Product(token, now))
	out, err := r.b.Purchased(ctx, "bot.tafs.pack.rayo", "p-1", appstore.Caller{TenantID: id})
	if err != nil || out.Action != "pack" || out.Duplicate || out.Acknowledged {
		t.Fatalf("pack: %+v %v", out, err)
	}
	if out, err := r.b.Purchased(ctx, "bot.tafs.pack.rayo", "p-1", appstore.Caller{TenantID: id}); err != nil || !out.Duplicate {
		t.Fatalf("sent again: %+v %v", out, err)
	}
	valid, _ := r.ledger.Valid(ctx, id, now)
	if len(valid) != 1 || valid[0].Tasks != 70 || valid[0].Source != packs.SourceGoogle || valid[0].SourceTxn != "p-1" {
		t.Fatalf("recorded: %+v", valid)
	}
	if got := r.get(t, id); tenant.BillingSourceOf(got) != tenant.BillingStripe {
		t.Fatal("a pack changed who bills the plan")
	}
	if out, err := r.b.Notify(ctx, voidedNote("p-1", playstore.VoidedOneTime)); err != nil || out.Action != "pack refunded" {
		t.Fatalf("voided: %+v %v", out, err)
	}
	if valid, _ := r.ledger.Valid(ctx, id, now); len(valid) != 0 {
		t.Fatalf("a refunded pack still counts: %+v", valid)
	}
	// A pack bought but never sent by the app: Play's notification adds it.
	r.play.SetProduct("bot.tafs.pack.pico", "p-2", playstoretest.Product(token, now))
	n := playstore.DeveloperNotification{PackageName: pkg, MessageID: "m-2"}
	n.OneTimeProductNotification = &struct {
		NotificationType int    `json:"notificationType"`
		PurchaseToken    string `json:"purchaseToken"`
		Sku              string `json:"sku"`
	}{NotificationType: playstore.OneTimePurchased, PurchaseToken: "p-2", Sku: "bot.tafs.pack.pico"}
	if out, err := r.b.Notify(ctx, n); err != nil || out.Action != "pack" {
		t.Fatalf("one-time notification: %+v %v", out, err)
	}
	// A notification for another app is ignored.
	n.PackageName = "com.example.other"
	if out, err := r.b.Notify(ctx, n); err != nil || !out.Ignored {
		t.Fatalf("another app's: %+v %v", out, err)
	}
}

// A pack of credits through Google Play: nothing while Play's payment is
// pending; once paid, its credits times the purchase's quantity, once per
// token however often it's sent; voided, what's left is taken back.
func TestCreditPackThroughGooglePlay(t *testing.T) {
	ctx := context.Background()
	c, err := packs.ParseCatalog([]byte(`{"solo": {"apple_product": "bot.tafs.solo.monthly", "google_product": "bot.tafs.solo.monthly"}, "packs": [
		{"id": "c-test", "name": "Test", "credits": 500, "apple_product": "bot.tafs.credits.test", "google_product": "bot.tafs.credits.test"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	r := newRig(t)
	p := appstore.NewProcessor(r.tenants, r.accounts, r.ledger, c, billing.NewMemoryStore(), nil).WithCredits(r.ledger)
	r.b = playstore.NewBilling(r.play.Client(t), p, r.accounts, r.tenants, r.purchases, c, nil).WithClock(func() time.Time { return now })
	id, token := r.account(t, map[string]any{tenant.MetadataKeyPlan: "solo", tenant.MetadataKeyBillingSource: "google"})
	credits := func() []packs.CreditPack {
		t.Helper()
		list, err := r.ledger.CreditPacks(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		return list
	}

	pending := playstoretest.Product(token, now)
	pending["purchaseState"], pending["quantity"] = playstore.ProductPending, 2
	r.play.SetProduct("bot.tafs.credits.test", "c-1", pending)
	if _, err := r.b.Purchased(ctx, "bot.tafs.credits.test", "c-1", appstore.Caller{TenantID: id}); !errors.Is(err, playstore.ErrPending) {
		t.Fatalf("pending: %v", err)
	}
	if got := credits(); len(got) != 0 {
		t.Fatalf("credited while pending: %+v", got)
	}
	paid := playstoretest.Product(token, now)
	paid["quantity"] = 2
	r.play.SetProduct("bot.tafs.credits.test", "c-1", paid)
	if out, err := r.b.Purchased(ctx, "bot.tafs.credits.test", "c-1", appstore.Caller{TenantID: id}); err != nil || out.Action != "pack" || out.Duplicate {
		t.Fatalf("paid: %+v %v", out, err)
	}
	if out, err := r.b.Purchased(ctx, "bot.tafs.credits.test", "c-1", appstore.Caller{TenantID: id}); err != nil || !out.Duplicate {
		t.Fatalf("sent again: %+v %v", out, err)
	}
	if got := credits(); len(got) != 1 || got[0].Credits != 1000 || got[0].Source != packs.SourceGoogle || got[0].SourceTxn != "c-1" {
		t.Fatalf("credited once, 2 × 500: %+v", got)
	}
	if out, err := r.b.Notify(ctx, voidedNote("c-1", playstore.VoidedOneTime)); err != nil || out.Action != "pack refunded" {
		t.Fatalf("voided: %+v %v", out, err)
	}
	if got := credits(); len(got) != 0 {
		t.Fatalf("a voided pack still has credits: %+v", got)
	}
}

// Refusals: another account's purchase, a test purchase on a real account,
// a product KIFF doesn't sell, Solo on a plan billed elsewhere, a token
// Play doesn't know, and one not paid yet. Nothing is recorded, granted or
// acknowledged.
func TestRefusedGooglePlayPurchases(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	id, token := r.account(t, nil)
	_, otherToken := r.account(t, nil)
	stripe, stripeToken := r.account(t, map[string]any{tenant.MetadataKeyPlan: "solo", tenant.MetadataKeyBillingSource: "stripe"})
	apple, appleToken := r.account(t, map[string]any{tenant.MetadataKeyPlan: "solo", tenant.MetadataKeyBillingSource: "apple"})

	r.play.SetProduct("bot.tafs.pack.pico", "other", playstoretest.Product(otherToken, now))
	if _, err := r.b.Purchased(ctx, "bot.tafs.pack.pico", "other", appstore.Caller{TenantID: id}); !errors.Is(err, appstore.ErrNotYours) {
		t.Fatalf("another account's: %v", err)
	}
	r.play.SetProduct("bot.tafs.pack.pico", "unbound", playstoretest.Product("", now))
	if _, err := r.b.Purchased(ctx, "bot.tafs.pack.pico", "unbound", appstore.Caller{TenantID: id}); !errors.Is(err, appstore.ErrNotYours) {
		t.Fatalf("no account token: %v", err)
	}
	testPack := playstoretest.Product(token, now)
	testPack["purchaseType"] = 0
	r.play.SetProduct("bot.tafs.pack.forte", "test-1", testPack)
	if _, err := r.b.Purchased(ctx, "bot.tafs.pack.forte", "test-1", appstore.Caller{TenantID: id}); !errors.Is(err, appstore.ErrSandbox) {
		t.Fatalf("a test pack: %v", err)
	}
	testSub := playstoretest.Subscription(r.solo, playstore.StateActive, token, now, now.AddDate(0, 1, 0))
	testSub["testPurchase"] = map[string]any{}
	r.play.SetSubscription("test-2", testSub)
	if _, err := r.b.Purchased(ctx, r.solo, "test-2", appstore.Caller{TenantID: id}); !errors.Is(err, appstore.ErrSandbox) {
		t.Fatalf("a test Solo: %v", err)
	}
	if _, err := r.b.Purchased(ctx, "bot.tafs.other", "x", appstore.Caller{TenantID: id}); !errors.Is(err, appstore.ErrUnknownProduct) {
		t.Fatalf("an unknown product: %v", err)
	}
	for name, a := range map[string]struct{ id, token string }{"kiff.dev": {stripe, stripeToken}, "Apple": {apple, appleToken}} {
		r.play.SetSubscription("sub-"+a.id, playstoretest.Subscription(r.solo, playstore.StateActive, a.token, now, now.AddDate(0, 1, 0)))
		if _, err := r.b.Purchased(ctx, r.solo, "sub-"+a.id, appstore.Caller{TenantID: a.id}); !errors.Is(err, appstore.ErrBilledElsewhere) {
			t.Errorf("Solo billed by %s: %v", name, err)
		}
		// Play's notification of it: recorded for support, not retried, not acknowledged.
		if out, err := r.b.Notify(ctx, subNote("sub-"+a.id, playstore.SubPurchased)); err != nil || !out.Unmapped {
			t.Errorf("notified, billed by %s: %+v %v", name, out, err)
		}
		if r.play.Acks("sub-"+a.id) != 0 {
			t.Errorf("billed by %s: acknowledged", name)
		}
		if got := r.get(t, a.id); tenant.BillingSourceOf(got) == tenant.BillingGoogle {
			t.Errorf("billed by %s: Google became the source", name)
		}
	}
	if _, err := r.b.Purchased(ctx, r.solo, "nobody-knows", appstore.Caller{TenantID: id}); !errors.Is(err, playstore.ErrNoPurchase) {
		t.Fatalf("an unknown token: %v", err)
	}
	r.play.SetSubscription("pending", playstoretest.Subscription(r.solo, playstore.StatePending, token, now, now.AddDate(0, 1, 0)))
	if _, err := r.b.Purchased(ctx, r.solo, "pending", appstore.Caller{TenantID: id}); !errors.Is(err, playstore.ErrPending) {
		t.Fatalf("pending: %v", err)
	}
	if valid, _ := r.ledger.Valid(ctx, id, now); len(valid) != 0 {
		t.Fatalf("a refused purchase granted tasks: %+v", valid)
	}
	if got := r.get(t, id); tenant.BillingSourceOf(got) != tenant.BillingNone {
		t.Fatalf("a refused purchase changed the plan: %v", got.Metadata)
	}
	for _, tok := range []string{"other", "unbound", "test-1", "test-2", "pending"} {
		if _, ok, _ := r.purchases.Get(ctx, tok); ok {
			t.Errorf("%s: recorded", tok)
		}
		if r.play.Acks(tok) != 0 {
			t.Errorf("%s: acknowledged", tok)
		}
	}
}

// Licence testers' purchases count on the internal plan's accounts and the
// named ones: the Play review account's.
func TestTestPurchasesOnTestAccounts(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	id, token := r.account(t, map[string]any{tenant.MetadataKeyPlan: string(tenant.PlanTrial)})
	r = &rig{play: r.play, tenants: r.tenants, accounts: r.accounts, ledger: r.ledger, purchases: r.purchases, catalog: r.catalog, solo: r.solo}
	p := appstore.NewProcessor(r.tenants, r.accounts, r.ledger, r.catalog, billing.NewMemoryStore(), nil).WithSandboxAccounts(id)
	r.b = playstore.NewBilling(r.play.Client(t), p, r.accounts, r.tenants, r.purchases, r.catalog, nil).WithClock(func() time.Time { return now })
	testPack := playstoretest.Product(token, now)
	testPack["purchaseType"] = 0
	r.play.SetProduct("bot.tafs.pack.pico", "t-1", testPack)
	if out, err := r.b.Purchased(ctx, "bot.tafs.pack.pico", "t-1", appstore.Caller{TenantID: id}); err != nil || out.Action != "pack" {
		t.Fatalf("a review account's test pack: %+v %v", out, err)
	}
}

// Each purchase is kept once per token; an update keeps the account, the
// product and the acknowledgement.
func TestPostgresPurchases(t *testing.T) {
	url := testdb.URL(t)
	if url == "" {
		t.Skip("KIFF_TEST_POSTGRES_URL not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := pool.Exec(ctx, `CREATE SCHEMA IF NOT EXISTS cloud_meta; CREATE TABLE IF NOT EXISTS cloud_meta.tenants (id text PRIMARY KEY);
		INSERT INTO cloud_meta.tenants VALUES ('acct-g1') ON CONFLICT DO NOTHING`); err != nil {
		t.Fatal(err)
	}
	if err := playstore.ApplySchema(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if err := playstore.ApplySchema(ctx, pool); err != nil {
		t.Fatal("not idempotent:", err)
	}
	s := playstore.NewPostgresPurchases(pool)
	tok := "tok-" + rand.Text()
	p := playstore.Purchase{Token: tok, TenantID: "acct-g1", Product: "bot.tafs.solo.monthly", Kind: playstore.KindSubscription, OrderID: "GPA.1", State: playstore.StateActive}
	if created, err := s.Record(ctx, p); err != nil || !created {
		t.Fatalf("record: %v %v", created, err)
	}
	if err := s.Acknowledged(ctx, tok, now); err != nil {
		t.Fatal(err)
	}
	p.State, p.OrderID = playstore.StateCanceled, ""
	if created, err := s.Record(ctx, p); err != nil || created {
		t.Fatalf("again: %v %v", created, err)
	}
	got, ok, err := s.Get(ctx, tok)
	if err != nil || !ok || got.State != playstore.StateCanceled || got.OrderID != "GPA.1" || got.AcknowledgedAt == nil || !got.AcknowledgedAt.Equal(now) {
		t.Fatalf("get: %+v %v %v", got, ok, err)
	}
	if _, ok, _ := s.Get(ctx, "nope"); ok {
		t.Fatal("found a token never recorded")
	}
}

// A push token is accepted only when Google's key signed it, for this
// audience, as this service account, unexpired.
func TestPushVerifier(t *testing.T) {
	ctx := context.Background()
	g := playstoretest.NewGoogle(t)
	const aud, email = "https://api.kiff.dev/webhooks/google", "play-rtdn@example.com"
	v := playstore.NewPushVerifier(aud, email).WithJWKS(g.JWKSURL()).WithClock(func() time.Time { return now })
	if err := v.VerifyToken(ctx, g.PushToken(aud, email, now)); err != nil {
		t.Fatalf("Google's token: %v", err)
	}
	forger, _ := rsa.GenerateKey(rand.Reader, 2048)
	claims := func(mod func(map[string]any)) map[string]any {
		c := map[string]any{"iss": "https://accounts.google.com", "aud": aud, "email": email, "email_verified": true,
			"iat": now.Unix(), "exp": now.Add(time.Hour).Unix()}
		mod(c)
		return c
	}
	for name, tok := range map[string]string{
		"forged":                 playstoretest.SignWith(forger, g.Kid, claims(func(map[string]any) {})),
		"another audience":       g.PushToken("https://evil.example/webhooks/google", email, now),
		"another service acct":   g.PushToken(aud, "someone@example.org", now),
		"expired":                g.PushToken(aud, email, now.Add(-3*time.Hour)),
		"another issuer":         g.Sign(claims(func(c map[string]any) { c["iss"] = "https://evil.example" })),
		"email not verified":     g.Sign(claims(func(c map[string]any) { c["email_verified"] = false })),
		"not a JWT":              "abc",
		"alg none":               "eyJhbGciOiJub25lIiwia2lkIjoieCJ9.e30.",
		"an unknown key":         playstoretest.SignWith(forger, "other-kid", claims(func(map[string]any) {})),
		"issued in the future":   g.PushToken(aud, email, now.Add(2*time.Hour)),
		"no audience configured": "",
	} {
		if name == "no audience configured" {
			if err := playstore.NewPushVerifier("", email).WithJWKS(g.JWKSURL()).VerifyToken(ctx, g.PushToken("", email, time.Now())); !errors.Is(err, playstore.ErrPushUnauthorized) {
				t.Errorf("%s: %v", name, err)
			}
			continue
		}
		if err := v.VerifyToken(ctx, tok); !errors.Is(err, playstore.ErrPushUnauthorized) {
			t.Errorf("%s: %v", name, err)
		}
	}
	req := httptest.NewRequest("POST", "/webhooks/google", nil)
	if err := v.Verify(ctx, req); !errors.Is(err, playstore.ErrPushUnauthorized) {
		t.Fatalf("no token: %v", err)
	}
}

// The service account's key is read from its JSON, or base64 of it.
func TestParseServiceAccount(t *testing.T) {
	p := playstoretest.NewPlay(t, pkg)
	if _, err := playstore.ParseServiceAccount(p.ServiceAccountJSON); err != nil {
		t.Fatal(err)
	}
	if _, err := playstore.ParseServiceAccount(b64(p.ServiceAccountJSON)); err != nil {
		t.Fatal("base64:", err)
	}
	for _, bad := range []string{"", "{}", `{"client_email":"a","private_key":"nope"}`, "%%%"} {
		if _, err := playstore.ParseServiceAccount(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

// A pack refunded after part of it was used: what's left is
// taken back, and the tasks already used stay used; the account never goes
// below nothing, and the pack no longer counts.
func TestAPackRefundedAfterPartUse(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	id, token := r.account(t, map[string]any{tenant.MetadataKeyPlan: "team", tenant.MetadataKeyBillingSource: "stripe"})
	r.play.SetProduct("bot.tafs.pack.rayo", "p-used", playstoretest.Product(token, now))
	if _, err := r.b.Purchased(ctx, "bot.tafs.pack.rayo", "p-used", appstore.Caller{TenantID: id}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 30; i++ {
		if err := r.ledger.Draw(ctx, id, fmt.Sprintf("task-%d", i), now); err != nil {
			t.Fatalf("draw %d: %v", i, err)
		}
	}
	if out, err := r.b.Notify(ctx, voidedNote("p-used", playstore.VoidedOneTime)); err != nil || out.Action != "pack refunded" {
		t.Fatalf("voided: %+v %v", out, err)
	}
	if valid, _ := r.ledger.Valid(ctx, id, now); len(valid) != 0 {
		t.Fatalf("a refunded pack still counts: %+v", valid)
	}
	if err := r.ledger.Draw(ctx, id, "task-after", now); err == nil {
		t.Fatal("a task drew on a refunded pack")
	}
	// The same void again changes nothing.
	if _, err := r.b.Notify(ctx, voidedNote("p-used", playstore.VoidedOneTime)); err != nil {
		t.Fatal(err)
	}
}

// A resubscription replaces the token that bills Solo: the
// account is credited once, and the replaced token, retired, can't end the
// plan when Play later reports it expired.
func TestAReplacedSubscriptionCantEndSolo(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	id, token := r.account(t, nil)
	r.play.SetSubscription("old", playstoretest.Subscription(r.solo, playstore.StateActive, token, now, now.AddDate(0, 1, 0)))
	if _, err := r.b.Purchased(ctx, r.solo, "old", appstore.Caller{TenantID: id}); err != nil {
		t.Fatal(err)
	}
	next := playstoretest.Subscription(r.solo, playstore.StateActive, token, now, now.AddDate(0, 2, 0))
	next["linkedPurchaseToken"] = "old"
	r.play.SetSubscription("new", next)
	if _, err := r.b.Purchased(ctx, r.solo, "new", appstore.Caller{TenantID: id}); err != nil {
		t.Fatal(err)
	}
	r.play.SetSubscription("old", playstoretest.Subscription(r.solo, playstore.StateExpired, token, now.AddDate(0, -1, 0), now))
	if _, err := r.b.Notify(ctx, subNote("old", playstore.SubExpired)); err != nil {
		t.Fatal(err)
	}
	got := r.get(t, id)
	if got.Metadata[playstore.MetaPurchaseToken] != "new" || string(tenant.PlanFor(got).Canonical) != "solo" {
		t.Fatalf("after the replaced token expired: token %v, plan %v", got.Metadata[playstore.MetaPurchaseToken], tenant.PlanFor(got).Canonical)
	}
}

// A licence tester's purchase on a real account is refused when Play
// notifies it too: a tester can't give themselves Solo.
func TestATesterCantGrantSoloByNotification(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	id, token := r.account(t, nil)
	testSub := playstoretest.Subscription(r.solo, playstore.StateActive, token, now, now.AddDate(0, 1, 0))
	testSub["testPurchase"] = map[string]any{}
	r.play.SetSubscription("tester", testSub)
	if _, err := r.b.Notify(ctx, subNote("tester", playstore.SubPurchased)); err != nil {
		t.Fatal(err)
	}
	if got := r.get(t, id); tenant.BillingSourceOf(got) == tenant.BillingGoogle || string(tenant.PlanFor(got).Canonical) == "solo" {
		t.Fatalf("a tester's notification granted Solo: %v", got.Metadata)
	}
	if r.play.Acks("tester") != 0 {
		t.Fatal("a tester's subscription was acknowledged")
	}
}

// The First month pack through Google Play, on the production table: its
// 7,000 credits are recorded, and a second purchase of it, which KIFF no
// longer offers, is recorded too with its credits: the customer paid.
func TestASecondFirstMonthPackIsStillCredited(t *testing.T) {
	ctx := context.Background()
	c, err := packs.LoadCatalog()
	if err != nil {
		t.Fatal(err)
	}
	const product = "bot.tafs.credits.firstmonth"
	r := newRig(t)
	p := appstore.NewProcessor(r.tenants, r.accounts, r.ledger, c, billing.NewMemoryStore(), nil).WithCredits(r.ledger)
	r.b = playstore.NewBilling(r.play.Client(t), p, r.accounts, r.tenants, r.purchases, c, nil).WithClock(func() time.Time { return now })
	id, token := r.account(t, map[string]any{tenant.MetadataKeyPlan: "solo", tenant.MetadataKeyBillingSource: "google"})
	for i, purchase := range []string{"fm-1", "fm-2"} {
		r.play.SetProduct(product, purchase, playstoretest.Product(token, now))
		if out, err := r.b.Purchased(ctx, product, purchase, appstore.Caller{TenantID: id}); err != nil || out.Action != "pack" || out.Duplicate {
			t.Fatalf("purchase %d: %+v %v", i+1, out, err)
		}
		list, err := r.ledger.CreditPacks(ctx, id)
		if err != nil || len(list) != i+1 || list[i].Pack != "credits-firstmonth" || list[i].Credits != 7000 {
			t.Fatalf("after purchase %d: %+v %v", i+1, list, err)
		}
		// Bought, so no longer offered, from the first purchase on.
		if got, err := r.ledger.BoughtPacks(ctx, id); err != nil || !got["credits-firstmonth"] {
			t.Fatalf("after purchase %d, bought: %v %v", i+1, got, err)
		}
	}
	if _, left, err := r.ledger.Credits(ctx, id, now.AddDate(0, -1, 0), now.Add(time.Hour)); err != nil || left != 14000 {
		t.Fatalf("both purchases' credits: %d %v", left, err)
	}
}
