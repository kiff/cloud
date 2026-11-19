package appstore_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kiff/cloud/api/internal/alert"
	"github.com/kiff/cloud/api/internal/appstore"
	"github.com/kiff/cloud/api/internal/appstore/appstoretest"
	"github.com/kiff/cloud/api/internal/billing"
	"github.com/kiff/cloud/api/internal/packs"
	"github.com/kiff/cloud/api/internal/tenant"
	"github.com/kiff/cloud/api/internal/testdb"
)

const bundle = "bot.tafs.app"

var now = time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)

func txn(id, product, token string, purchased, expires time.Time) map[string]any {
	m := map[string]any{"transactionId": id, "originalTransactionId": "orig-1", "bundleId": bundle, "productId": product,
		"purchaseDate": purchased.UnixMilli(), "appAccountToken": token, "environment": "Production", "quantity": 1,
		"signedDate": now.UnixMilli()}
	if !expires.IsZero() {
		m["type"], m["expiresDate"] = appstore.TypeSubscription, expires.UnixMilli()
	} else {
		m["type"] = appstore.TypeConsumable
	}
	return m
}

// The pinned root is Apple Root CA - G3, and a payload signed under any
// other root is refused by the production verifier.
func TestPinnedAppleRoot(t *testing.T) {
	root, err := appstore.AppleRoot()
	if err != nil || root.Subject.CommonName != "Apple Root CA - G3" {
		t.Fatalf("root: %v %v", root, err)
	}
	v, err := appstore.NewVerifier(bundle, []string{"Production", "Sandbox"})
	if err != nil {
		t.Fatal(err)
	}
	s := appstoretest.New(t)
	if _, err := v.DecodeTransaction(s.Sign(t, txn("1", "p", "tok", now, time.Time{}))); !errors.Is(err, appstore.ErrInvalid) {
		t.Fatalf("a payload not signed under Apple's root: %v", err)
	}
}

// A signed transaction is read only when its chain ends at the trusted
// root, its signature is the leaf's, and it is this app's, in an accepted
// environment.
func TestVerifier(t *testing.T) {
	s := appstoretest.New(t)
	v := appstore.NewTestVerifier(s.Root, bundle, []string{"Sandbox"}, func() time.Time { return now })
	sandbox := func(m map[string]any) map[string]any { m["environment"] = "Sandbox"; return m }
	good := s.Sign(t, sandbox(txn("1", "bot.tafs.pack.pico", "tok", now, time.Time{})))
	got, err := v.DecodeTransaction(good)
	if err != nil || got.TransactionID != "1" || got.ProductID != "bot.tafs.pack.pico" || !got.Purchased().Equal(now) {
		t.Fatalf("good: %+v %v", got, err)
	}
	other := appstoretest.New(t)
	parts := strings.Split(good, ".")
	tampered := parts[0] + "." + base64.RawURLEncoding.EncodeToString([]byte(`{"transactionId":"2","bundleId":"bot.tafs.app","productId":"x","environment":"Sandbox"}`)) + "." + parts[2]
	wrongApp := sandbox(txn("1", "p", "tok", now, time.Time{}))
	wrongApp["bundleId"] = "com.example.other"
	prod := txn("1", "p", "tok", now, time.Time{})
	prod["environment"] = "Production"
	for name, token := range map[string]string{
		"signed under another root": other.Sign(t, txn("1", "p", "tok", now, time.Time{})),
		"a changed payload":         tampered,
		"another app":               s.Sign(t, wrongApp),
		"an environment not taken":  s.Sign(t, prod),
		"not a JWS":                 "abc",
		"another algorithm":         base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","x5c":["a","b"]}`)) + "." + parts[1] + ".",
	} {
		if _, err := v.DecodeTransaction(token); !errors.Is(err, appstore.ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// A notification and the transaction it carries.
	n := map[string]any{"notificationType": "DID_RENEW", "notificationUUID": "n-1", "signedDate": now.UnixMilli(),
		"data": map[string]any{"bundleId": bundle, "environment": "Sandbox", "signedTransactionInfo": good}}
	note, tx, err := v.DecodeNotification(s.Sign(t, n))
	if err != nil || note.NotificationType != "DID_RENEW" || tx == nil || tx.TransactionID != "1" {
		t.Fatalf("notification: %+v %+v %v", note, tx, err)
	}
	n["data"].(map[string]any)["signedTransactionInfo"] = other.Sign(t, txn("1", "p", "tok", now, time.Time{}))
	if _, _, err := v.DecodeNotification(s.Sign(t, n)); !errors.Is(err, appstore.ErrInvalid) {
		t.Fatalf("a notification carrying a forged transaction: %v", err)
	}
}

type rig struct {
	tenants  *tenant.MemoryStore
	accounts *appstore.MemoryAccounts
	ledger   *packs.Memory
	seen     *billing.MemoryStore
	p        *appstore.Processor
	catalog  packs.Catalog
}

func newRig(t *testing.T) *rig {
	t.Helper()
	c, err := packs.LoadCatalog()
	if err != nil {
		t.Fatal(err)
	}
	r := &rig{tenants: tenant.NewMemoryStore(), accounts: appstore.NewMemoryAccounts(), ledger: packs.NewMemory(), seen: billing.NewMemoryStore(), catalog: c}
	r.p = appstore.NewProcessor(r.tenants, r.accounts, r.ledger, c, r.seen, nil).WithClock(func() time.Time { return now })
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

func (r *rig) meta(t *testing.T, id, key string) any {
	t.Helper()
	got, err := r.tenants.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return got.Metadata[key]
}

func decoded(t *testing.T, m map[string]any) appstore.Transaction {
	t.Helper()
	s := appstoretest.New(t)
	v := appstore.NewTestVerifier(s.Root, bundle, []string{"Production", "Sandbox"}, func() time.Time { return now })
	tx, err := v.DecodeTransaction(s.Sign(t, m))
	if err != nil {
		t.Fatal(err)
	}
	return tx
}

// heard records the alerts the processor sends.
type heard struct {
	mu     sync.Mutex
	events []alert.Event
}

func (h *heard) Notify(ev alert.Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.events = append(h.events, ev)
}

func (h *heard) all() []alert.Event {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]alert.Event(nil), h.events...)
}

// A pack bought in the app is recorded once per transaction, for the
// account whose token it carries, answering the request sent with it and
// telling the member once; a purchase carrying another account's token is
// refused.
func TestPackPurchases(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	told := &heard{}
	r.p.WithRequestAlerts(r.ledger, told)
	id, token := r.account(t, map[string]any{tenant.MetadataKeyPlan: "team"})
	other, otherToken := r.account(t, map[string]any{tenant.MetadataKeyPlan: "solo"})
	req, _ := r.ledger.Ask(ctx, packs.Request{TenantID: id, AskedBy: "bob", Pack: "forte", CreatedAt: now.Add(-time.Hour), ExpiresAt: now.AddDate(0, 0, 3)})

	forte := decoded(t, txn("t-1", "bot.tafs.pack.forte", strings.ToUpper(token), now, time.Time{}))
	res, err := r.p.Purchased(ctx, forte, appstore.Caller{TenantID: id, Subject: "alice", RequestID: req.ID})
	if err != nil || res.Action != "pack" || res.Duplicate {
		t.Fatalf("purchase: %+v %v", res, err)
	}
	if res, err := r.p.Purchased(ctx, forte, appstore.Caller{TenantID: id, Subject: "alice"}); err != nil || !res.Duplicate {
		t.Fatalf("sent again: %+v %v", res, err)
	}
	valid, _ := r.ledger.Valid(ctx, id, now)
	if len(valid) != 1 || valid[0].Tasks != 200 || valid[0].Source != packs.SourceApple {
		t.Fatalf("recorded: %+v", valid)
	}
	if got, _ := r.ledger.Get(ctx, id, req.ID, now); got.Status != packs.RequestApproved || got.AnsweredBy != "alice" {
		t.Fatalf("request: %+v", got)
	}
	if ev := told.all(); len(ev) != 1 || ev[0].Outcome != alert.PackRequestAnswered || ev[0].EntityID != req.ID || ev[0].TenantID != id ||
		len(ev[0].Reasons) != 2 || ev[0].Reasons[1] != packs.RequestApproved {
		t.Fatalf("the member is told once: %+v", ev)
	}
	// Another account's token, or none.
	if _, err := r.p.Purchased(ctx, decoded(t, txn("t-2", "bot.tafs.pack.pico", otherToken, now, time.Time{})), appstore.Caller{TenantID: id}); !errors.Is(err, appstore.ErrNotYours) {
		t.Fatalf("another's: %v", err)
	}
	if _, err := r.p.Purchased(ctx, decoded(t, txn("t-3", "bot.tafs.pack.pico", "", now, time.Time{})), appstore.Caller{TenantID: other}); !errors.Is(err, appstore.ErrNotYours) {
		t.Fatalf("no token: %v", err)
	}
	if _, err := r.p.Purchased(ctx, decoded(t, txn("t-4", "bot.tafs.something", token, now, time.Time{})), appstore.Caller{TenantID: id}); !errors.Is(err, appstore.ErrUnknownProduct) {
		t.Fatalf("an unknown product: %v", err)
	}

	// Refunded: Apple's notification takes the pack back, once.
	refunded := txn("t-1", "bot.tafs.pack.forte", token, now, time.Time{})
	refunded["revocationDate"] = now.Add(time.Hour).UnixMilli()
	tx := decoded(t, refunded)
	n := appstore.Notification{NotificationType: appstore.Refund, NotificationUUID: "n-refund"}
	if res, err := r.p.Notify(ctx, n, &tx); err != nil || res.Action != "pack refunded" {
		t.Fatalf("refund: %+v %v", res, err)
	}
	if valid, _ := r.ledger.Valid(ctx, id, now); len(valid) != 0 {
		t.Fatalf("a refunded pack still counts: %+v", valid)
	}
	if res, err := r.p.Notify(ctx, n, &tx); err != nil || !res.Duplicate {
		t.Fatalf("redelivered: %+v %v", res, err)
	}
	// A one-time charge Apple reports itself records the pack.
	tx = decoded(t, txn("t-5", "bot.tafs.pack.vonk", token, now, time.Time{}))
	if res, err := r.p.Notify(ctx, appstore.Notification{NotificationType: appstore.OneTimeCharge, NotificationUUID: "n-otc"}, &tx); err != nil || res.Action != "pack" {
		t.Fatalf("one-time charge: %+v %v", res, err)
	}
}

// creditCatalog is a test table with a pack of tasks and a pack of credits.
// The production table gets packs of credits with the owner's price list.
const creditCatalog = `{"solo": {"apple_product": "bot.tafs.solo.monthly"}, "packs": [
	{"id": "pico", "name": "Pico", "tasks": 12, "stripe_price": "kiff_pack_pico", "apple_product": "bot.tafs.pack.pico"},
	{"id": "c-test", "name": "Test", "credits": 500, "apple_product": "bot.tafs.credits.test", "google_product": "bot.tafs.credits.test"}]}`

// A pack of credits bought through the store gives its credits times the
// receipt's quantity, once per transaction however often it arrives, to
// the account the store's token names, never on a test purchase; a
// refund takes back what's left, what was spent stays spent, and the
// take-back is logged with both. A pack of tasks stays tasks.
func TestCreditPackPurchases(t *testing.T) {
	ctx := context.Background()
	c, err := packs.ParseCatalog([]byte(creditCatalog))
	if err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	r := newRig(t)
	r.catalog = c
	r.p = appstore.NewProcessor(r.tenants, r.accounts, r.ledger, c, r.seen, slog.New(slog.NewJSONHandler(&logs, nil))).
		WithClock(func() time.Time { return now }).WithCredits(r.ledger)
	solo := map[string]any{tenant.MetadataKeyPlan: string(tenant.PlanSolo)}
	id, token := r.account(t, solo)
	other, otherToken := r.account(t, solo)
	credits := func(tenantID string) []packs.CreditPack {
		t.Helper()
		list, err := r.ledger.CreditPacks(ctx, tenantID)
		if err != nil {
			t.Fatal(err)
		}
		return list
	}

	// Three of them on one receipt, sent by the app twice and by Apple once.
	three := txn("c-1", "bot.tafs.credits.test", token, now, time.Time{})
	three["quantity"] = 3
	tx := decoded(t, three)
	if res, err := r.p.Purchased(ctx, tx, appstore.Caller{TenantID: id}); err != nil || res.Action != "pack" || res.Duplicate {
		t.Fatalf("purchase: %+v %v", res, err)
	}
	if res, err := r.p.Purchased(ctx, tx, appstore.Caller{TenantID: id}); err != nil || !res.Duplicate {
		t.Fatalf("sent again: %+v %v", res, err)
	}
	if res, err := r.p.Notify(ctx, appstore.Notification{NotificationType: appstore.OneTimeCharge, NotificationUUID: "n-c1"}, &tx); err != nil || !res.Duplicate {
		t.Fatalf("Apple's notification of it: %+v %v", res, err)
	}
	if got := credits(id); len(got) != 1 || got[0].Credits != 1500 || got[0].Source != packs.SourceApple {
		t.Fatalf("credited once, 3 × 500: %+v", got)
	}
	if valid, _ := r.ledger.Valid(ctx, id, now); len(valid) != 0 {
		t.Fatalf("a pack of credits recorded tasks: %+v", valid)
	}

	// Another account's token: nothing on either account.
	if _, err := r.p.Purchased(ctx, decoded(t, txn("c-2", "bot.tafs.credits.test", otherToken, now, time.Time{})), appstore.Caller{TenantID: id}); !errors.Is(err, appstore.ErrNotYours) {
		t.Fatalf("another's: %v", err)
	}
	// A test purchase on a real account: nothing.
	sandbox := txn("c-3", "bot.tafs.credits.test", otherToken, now, time.Time{})
	sandbox["environment"] = appstore.EnvSandbox
	if _, err := r.p.Purchased(ctx, decoded(t, sandbox), appstore.Caller{TenantID: other}); !errors.Is(err, appstore.ErrSandbox) {
		t.Fatalf("a test purchase: %v", err)
	}
	if got := credits(other); len(got) != 0 || len(credits(id)) != 1 {
		t.Fatalf("credited: %+v %+v", got, credits(id))
	}
	// A pack of tasks' product stays tasks.
	if res, err := r.p.Purchased(ctx, decoded(t, txn("c-4", "bot.tafs.pack.pico", otherToken, now, time.Time{})), appstore.Caller{TenantID: other}); err != nil || res.Action != "pack" {
		t.Fatalf("a pack of tasks: %+v %v", res, err)
	}
	if valid, _ := r.ledger.Valid(ctx, other, now); len(valid) != 1 || valid[0].Tasks != 12 || len(credits(other)) != 0 {
		t.Fatalf("a pack of tasks became credits: %+v %+v", valid, credits(other))
	}

	// 1200 of the 1500 spent, then refunded: the 300 left are taken back,
	// the 1200 stay spent, and nothing goes below zero.
	if drawn := r.ledger.DrawCredits(id, 1200, now); drawn != 1200 {
		t.Fatalf("drawn: %d", drawn)
	}
	refunded := txn("c-1", "bot.tafs.credits.test", token, now, time.Time{})
	refunded["quantity"], refunded["revocationDate"] = 3, now.Add(time.Hour).UnixMilli()
	rtx := decoded(t, refunded)
	if res, err := r.p.Notify(ctx, appstore.Notification{NotificationType: appstore.Refund, NotificationUUID: "n-refund"}, &rtx); err != nil || res.Action != "pack refunded" {
		t.Fatalf("refund: %+v %v", res, err)
	}
	if got := credits(id); len(got) != 0 {
		t.Fatalf("a refunded pack still has credits: %+v", got)
	}
	if _, left, _ := r.ledger.Credits(ctx, id, now.AddDate(0, -1, 0), now.Add(2*time.Hour)); left != 0 {
		t.Fatalf("left after the refund: %d", left)
	}
	var line struct {
		Msg       string `json:"msg"`
		Credits   int64  `json:"credits"`
		TakenBack int64  `json:"taken_back"`
	}
	for _, l := range bytes.Split(logs.Bytes(), []byte("\n")) {
		if json.Unmarshal(l, &line) == nil && line.Msg == "appstore: pack of credits taken back" {
			break
		}
		line.Msg = ""
	}
	if line.Msg == "" || line.Credits != 1500 || line.TakenBack != 300 {
		t.Fatalf("the take-back's log: %+v\n%s", line, logs.String())
	}

	// Refunded before it was recorded: recorded already taken back, with
	// nothing to spend.
	early := txn("c-5", "bot.tafs.credits.test", otherToken, now, time.Time{})
	early["revocationDate"] = now.Add(time.Hour).UnixMilli()
	etx := decoded(t, early)
	if res, err := r.p.Notify(ctx, appstore.Notification{NotificationType: appstore.Refund, NotificationUUID: "n-early"}, &etx); err != nil || res.Action != "pack refunded" {
		t.Fatalf("an early refund: %+v %v", res, err)
	}
	if res, err := r.p.Purchased(ctx, decoded(t, txn("c-5", "bot.tafs.credits.test", otherToken, now, time.Time{})), appstore.Caller{TenantID: other}); err != nil || !res.Duplicate {
		t.Fatalf("its purchase arriving after: %+v %v", res, err)
	}
	if got := credits(other); len(got) != 0 {
		t.Fatalf("an early refund left credits: %+v", got)
	}

	// No credits ledger: a pack of credits is refused, unrecorded.
	bare := appstore.NewProcessor(r.tenants, r.accounts, r.ledger, c, r.seen, nil).WithClock(func() time.Time { return now })
	if _, err := bare.Purchased(ctx, decoded(t, txn("c-6", "bot.tafs.credits.test", token, now, time.Time{})), appstore.Caller{TenantID: id}); err == nil {
		t.Fatal("recorded with no credits ledger")
	}
}

// Solo through Apple: the one-week free trial is the trial; each renewal
// is Solo with Apple's period; a failed renewal keeps the plan; expiry or
// a refund ends it. Apple is the billing source throughout, and an older
// event never overwrites a newer one.
func TestSoloThroughApple(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	solo := r.catalog.Solo.AppleProduct
	id, token := r.account(t, nil)
	notify := func(uuid, kind string, m map[string]any) billing.Result {
		t.Helper()
		tx := decoded(t, m)
		res, err := r.p.Notify(ctx, appstore.Notification{NotificationType: kind, NotificationUUID: uuid}, &tx)
		if err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
		return res
	}
	trial := txn("s-1", solo, token, now.Add(-time.Hour), now.AddDate(0, 0, 7))
	trial["offerType"], trial["offerDiscountType"] = 1, "FREE_TRIAL"
	if res := notify("n1", appstore.Subscribed, trial); res.Action != "trialing" {
		t.Fatalf("subscribed: %+v", res)
	}
	got, _ := r.tenants.Get(ctx, id)
	if l := tenant.PlanFor(got); l.Canonical != tenant.PlanTrial || tenant.BillingSourceOf(got) != tenant.BillingApple {
		t.Fatalf("trial: %+v %q", l, tenant.BillingSourceOf(got))
	}
	if end, ok := tenant.TrialEndsAt(got); !ok || !end.Equal(now.AddDate(0, 0, 7)) {
		t.Fatalf("trial ends: %v", end)
	}

	start, end := now.AddDate(0, 0, 7), now.AddDate(0, 1, 7)
	if res := notify("n2", appstore.DidRenew, txn("s-2", solo, token, start, end)); res.Action != "solo" {
		t.Fatalf("renewed: %+v", res)
	}
	got, _ = r.tenants.Get(ctx, id)
	if from, to := tenant.Period(got, start.Add(time.Hour)); tenant.PlanFor(got).Canonical != tenant.PlanSolo || !from.Equal(start) || !to.Equal(end) {
		t.Fatalf("solo: %s [%v, %v)", tenant.PlanFor(got).Canonical, from, to)
	}
	// The trial's transaction arrives late: older, so ignored.
	if res := notify("n-late", appstore.Subscribed, trial); !res.Ignored {
		t.Fatalf("a late, older event: %+v", res)
	}
	if res := notify("n3", appstore.DidFailToRenew, txn("s-2", solo, token, start, end)); res.Action != "grace" || r.meta(t, id, billing.MetaBillingStatus) != billing.StatusPastDue {
		t.Fatalf("failed to renew: %+v", res)
	}
	if got, _ = r.tenants.Get(ctx, id); tenant.PlanFor(got).Canonical != tenant.PlanSolo {
		t.Fatal("a failed renewal ended the plan")
	}
	if res := notify("n4", appstore.Expired, txn("s-2", solo, token, start, end)); res.Action != "ended" {
		t.Fatalf("expired: %+v", res)
	}
	got, _ = r.tenants.Get(ctx, id)
	if tenant.PlanFor(got).Canonical != tenant.PlanTrial || tenant.BillingSourceOf(got) != tenant.BillingNone {
		t.Fatalf("ended: %s %q", tenant.PlanFor(got).Canonical, tenant.BillingSourceOf(got))
	}

	// Resubscribed, then refunded: the plan is taken back.
	if res := notify("n5", appstore.Subscribed, txn("s-3", solo, token, now, now.AddDate(0, 1, 0))); res.Action != "solo" {
		t.Fatalf("resubscribed: %+v", res)
	}
	refund := txn("s-3", solo, token, now, now.AddDate(0, 1, 0))
	refund["revocationDate"] = now.Add(time.Hour).UnixMilli()
	if res := notify("n6", appstore.Refund, refund); res.Action != "ended" {
		t.Fatalf("refunded: %+v", res)
	}
}

// One billing source per account: Apple never bills a plan billed on
// kiff.dev or a contract, nor any plan but Solo. From the app it's
// refused; from Apple's notification it's recorded for support, and
// Apple's retry stops.
func TestAppleBillsSoloOnly(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	solo := r.catalog.Solo.AppleProduct
	for name, meta := range map[string]map[string]any{
		"billed on kiff.dev": {tenant.MetadataKeyPlan: "solo", tenant.MetadataKeyBillingSource: "stripe"},
		"a contract":         {tenant.MetadataKeyPlan: "company"},
		"team":               {tenant.MetadataKeyPlan: "team"},
	} {
		id, token := r.account(t, meta)
		tx := decoded(t, txn("x-"+id, solo, token, now, now.AddDate(0, 1, 0)))
		if _, err := r.p.Purchased(ctx, tx, appstore.Caller{TenantID: id}); !errors.Is(err, appstore.ErrBilledElsewhere) && !errors.Is(err, appstore.ErrNotSolo) {
			t.Errorf("%s, from the app: %v", name, err)
		}
		res, err := r.p.Notify(ctx, appstore.Notification{NotificationType: appstore.Subscribed, NotificationUUID: "n-" + id}, &tx)
		if err != nil || !res.Unmapped {
			t.Errorf("%s, notified: %+v %v", name, res, err)
		}
		if seen, _ := r.seen.Seen(ctx, "apple:n-"+id); !seen {
			t.Errorf("%s: not marked seen, so Apple retries forever", name)
		}
		got, _ := r.tenants.Get(ctx, id)
		if tenant.BillingSourceOf(got) == tenant.BillingApple {
			t.Errorf("%s: Apple became the source", name)
		}
		// Apple's subscription expiring ends nothing here.
		if res, err := r.p.Notify(ctx, appstore.Notification{NotificationType: appstore.Expired, NotificationUUID: "e-" + id}, &tx); err != nil || !res.Ignored {
			t.Errorf("%s, expired: %+v %v", name, res, err)
		}
	}
	// A notification for no known account is recorded, not retried.
	tx := decoded(t, txn("y", solo, "00000000-0000-4000-8000-0000000000aa", now, now.AddDate(0, 1, 0)))
	if res, err := r.p.Notify(ctx, appstore.Notification{NotificationType: appstore.Subscribed, NotificationUUID: "n-nobody"}, &tx); err != nil || !res.Unmapped {
		t.Fatalf("nobody's: %+v %v", res, err)
	}
}

// Each account's token is made once and stays; it names its account.
func TestPostgresAccounts(t *testing.T) {
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
		INSERT INTO cloud_meta.tenants VALUES ('acct-1'), ('acct-2') ON CONFLICT DO NOTHING`); err != nil {
		t.Fatal(err)
	}
	if err := appstore.ApplySchema(ctx, pool); err != nil {
		t.Fatal(err)
	}
	a := appstore.NewPostgresAccounts(pool)
	tokens := make(chan string, 8)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tok, err := a.Token(ctx, "acct-1")
			if err != nil {
				t.Error(err)
			}
			tokens <- tok
		}()
	}
	wg.Wait()
	close(tokens)
	first := ""
	for tok := range tokens {
		if first == "" {
			first = tok
		}
		if tok != first || len(tok) != 36 {
			t.Fatalf("tokens differ: %q %q", tok, first)
		}
	}
	if other, _ := a.Token(ctx, "acct-2"); other == first {
		t.Fatal("two accounts share a token")
	}
	if got, err := a.TenantFor(ctx, strings.ToUpper(first)); err != nil || got != "acct-1" {
		t.Fatalf("the token's account: %q %v", got, err)
	}
	if got, _ := a.TenantFor(ctx, "not-a-uuid"); got != "" {
		t.Fatalf("a bad token named %q", got)
	}
}

// Test purchases (Apple's Sandbox: TestFlight, App Review) count only on
// an account on the internal plan and on App Review's account. On any
// other account they are refused, from the app or in Apple's notification,
// and nothing is recorded or granted: no pack, no plan.
func TestTestPurchases(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	internal, internalToken := r.account(t, map[string]any{tenant.MetadataKeyPlan: string(tenant.PlanInternal)})
	review, reviewToken := r.account(t, map[string]any{tenant.MetadataKeyPlan: string(tenant.PlanTrial)})
	paying, payingToken := r.account(t, map[string]any{tenant.MetadataKeyPlan: string(tenant.PlanTrial)})
	r.p.WithSandboxAccounts(" "+review+" ", "")
	sandbox := func(m map[string]any) appstore.Transaction {
		m["environment"] = appstore.EnvSandbox
		return decoded(t, m)
	}

	// The internal plan's account and App Review's: a test pack counts.
	for name, a := range map[string]struct{ id, token string }{"internal": {internal, internalToken}, "App Review": {review, reviewToken}} {
		res, err := r.p.Purchased(ctx, sandbox(txn("s-"+a.id, "bot.tafs.pack.pico", a.token, now, time.Time{})), appstore.Caller{TenantID: a.id})
		if err != nil || res.Action != "pack" {
			t.Fatalf("%s: %+v %v", name, res, err)
		}
		if valid, _ := r.ledger.Valid(ctx, a.id, now); len(valid) != 1 {
			t.Fatalf("%s: %+v", name, valid)
		}
	}
	// App Review's Solo, through Apple's free trial.
	trial := txn("s-solo-review", r.catalog.Solo.AppleProduct, reviewToken, now, now.AddDate(0, 0, 7))
	trial["offerType"], trial["offerDiscountType"] = 1, "FREE_TRIAL"
	if res, err := r.p.Purchased(ctx, sandbox(trial), appstore.Caller{TenantID: review}); err != nil || res.Action != "trialing" {
		t.Fatalf("App Review's Solo: %+v %v", res, err)
	}

	// Any other account: a test pack, from the app, is refused.
	if _, err := r.p.Purchased(ctx, sandbox(txn("s-real-1", "bot.tafs.pack.forte", payingToken, now, time.Time{})), appstore.Caller{TenantID: paying}); !errors.Is(err, appstore.ErrSandbox) {
		t.Fatalf("a test pack on a real account: %v", err)
	}
	// A test Solo too: the plan stays as it was.
	if _, err := r.p.Purchased(ctx, sandbox(txn("s-real-2", r.catalog.Solo.AppleProduct, payingToken, now, now.AddDate(0, 1, 0))), appstore.Caller{TenantID: paying}); !errors.Is(err, appstore.ErrSandbox) {
		t.Fatalf("a test Solo on a real account: %v", err)
	}
	// Apple's notification of one: recorded as seen, so Apple stops
	// sending it, and nothing applied.
	tx := sandbox(txn("s-real-3", "bot.tafs.pack.vonk", payingToken, now, time.Time{}))
	if res, err := r.p.Notify(ctx, appstore.Notification{NotificationType: appstore.OneTimeCharge, NotificationUUID: "n-sandbox"}, &tx); err != nil || !res.Unmapped || res.Action != "" {
		t.Fatalf("a test purchase's notification: %+v %v", res, err)
	}
	if valid, _ := r.ledger.Valid(ctx, paying, now); len(valid) != 0 {
		t.Fatalf("a test purchase granted tasks: %+v", valid)
	}
	if plan, src := r.meta(t, paying, tenant.MetadataKeyPlan), r.meta(t, paying, tenant.MetadataKeyBillingSource); plan != string(tenant.PlanTrial) || src != nil {
		t.Fatalf("a test purchase changed the plan: %v %v", plan, src)
	}
	// The same purchase made for real counts.
	if res, err := r.p.Purchased(ctx, decoded(t, txn("p-real-1", "bot.tafs.pack.forte", payingToken, now, time.Time{})), appstore.Caller{TenantID: paying}); err != nil || res.Action != "pack" {
		t.Fatalf("a real pack: %+v %v", res, err)
	}
}

// Turning Solo's renewal off or on with Apple is recorded whoever bills the
// plan now: after a move to Team, Solo is said to renew only while it does
// (the move to Team). Another subtype, or no transaction, changes nothing.
func TestRenewalStatusIsRecorded(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	solo := r.catalog.Solo.AppleProduct
	id, token := r.account(t, map[string]any{tenant.MetadataKeyPlan: "team", tenant.MetadataKeyBillingSource: "stripe",
		appstore.MetaOriginalTransaction: "orig-1"})
	status := func(uuid, subtype, original string, signed time.Time) billing.Result {
		t.Helper()
		m := txn("s-r", solo, token, now, now.AddDate(0, 0, 12))
		m["originalTransactionId"] = original
		tx := decoded(t, m)
		res, err := r.p.Notify(ctx, appstore.Notification{NotificationType: appstore.DidChangeRenewalStatus, Subtype: subtype,
			NotificationUUID: uuid, SignedDate: signed.UnixMilli()}, &tx)
		if err != nil {
			t.Fatalf("%s: %v", subtype, err)
		}
		return res
	}
	if res := status("r1", "AUTO_RENEW_DISABLED", "orig-1", now); res.Action != "auto_renew_off" || r.meta(t, id, appstore.MetaAutoRenew) != "off" {
		t.Fatalf("off: %+v", res)
	}
	if got, _ := r.tenants.Get(ctx, id); tenant.PlanFor(got).Canonical != tenant.PlanTeam || tenant.BillingSourceOf(got) != tenant.BillingStripe {
		t.Fatal("recording the renewal changed the plan")
	}
	if res := status("r2", "AUTO_RENEW_ENABLED", "orig-1", now.Add(time.Hour)); res.Action != "auto_renew_on" || r.meta(t, id, appstore.MetaAutoRenew) != "on" {
		t.Fatalf("on: %+v", res)
	}
	// Apple doesn't promise the order: an older status arriving late is ignored.
	if res := status("r3", "AUTO_RENEW_DISABLED", "orig-1", now.Add(time.Minute)); !res.Ignored || r.meta(t, id, appstore.MetaAutoRenew) != "on" {
		t.Fatalf("an older status: %+v", res)
	}
	// Another Apple ID's subscription on the same account says nothing about this one.
	if res := status("r4", "AUTO_RENEW_DISABLED", "orig-2", now.Add(2*time.Hour)); !res.Ignored || r.meta(t, id, appstore.MetaAutoRenew) != "on" {
		t.Fatalf("another subscription: %+v", res)
	}
	if res := status("r5", "SOMETHING_ELSE", "orig-1", now.Add(3*time.Hour)); !res.Ignored || r.meta(t, id, appstore.MetaAutoRenew) != "on" {
		t.Fatalf("another subtype: %+v", res)
	}
	// An account with no App Store subscription recorded keeps nothing.
	other, otherToken := r.account(t, map[string]any{tenant.MetadataKeyPlan: "team", tenant.MetadataKeyBillingSource: "stripe"})
	token = otherToken
	if res := status("r6", "AUTO_RENEW_DISABLED", "orig-1", now.Add(4*time.Hour)); !res.Ignored || r.meta(t, other, appstore.MetaAutoRenew) != nil {
		t.Fatalf("no subscription: %+v", res)
	}
}

// After a move to Team, Apple renewing the Solo it replaced
// keeps Solo's new date and nothing else, so the line that says it still
// renews stays true; an older renewal is ignored; the plan stays Team.
// SoloRenewsUntil says it renews only while its renewal is on and the date
// is ahead.
func TestSoloRenewingAfterTheMoveToTeamKeepsItsDate(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	solo := r.catalog.Solo.AppleProduct
	first := now.AddDate(0, 0, 12)
	id, token := r.account(t, map[string]any{tenant.MetadataKeyPlan: "team", tenant.MetadataKeyBillingSource: "stripe",
		appstore.MetaOriginalTransaction: "orig-1", appstore.MetaExpiresAt: first.Format(time.RFC3339)})
	renew := func(uuid string, expires time.Time) billing.Result {
		t.Helper()
		tx := decoded(t, txn("s-"+uuid, solo, token, now, expires))
		res, err := r.p.Notify(ctx, appstore.Notification{NotificationType: appstore.DidRenew, NotificationUUID: uuid}, &tx)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	next := first.AddDate(0, 1, 0)
	if res := renew("d1", next); res.Action != "solo_renewed_after_team" || r.meta(t, id, appstore.MetaExpiresAt) != next.Format(time.RFC3339) {
		t.Fatalf("renewed after the move: %+v", res)
	}
	got, _ := r.tenants.Get(ctx, id)
	if tenant.PlanFor(got).Canonical != tenant.PlanTeam || tenant.BillingSourceOf(got) != tenant.BillingStripe {
		t.Fatal("Solo's renewal changed the plan")
	}
	if until, ok := appstore.SoloRenewsUntil(got, now); !ok || !until.Equal(next) {
		t.Fatalf("renews until %v %v", until, ok)
	}
	if res := renew("d0", first); !res.Ignored {
		t.Fatalf("an older renewal: %+v", res)
	}
	if err := r.tenants.SetMetadata(ctx, id, appstore.MetaAutoRenew, "off"); err != nil {
		t.Fatal(err)
	}
	got, _ = r.tenants.Get(ctx, id)
	if _, ok := appstore.SoloRenewsUntil(got, now); ok {
		t.Fatal("renewal off: it doesn't renew")
	}
	// A renewal Apple charged before renewal was turned off arrives late: it
	// keeps the date, not "on".
	if err := r.tenants.SetMetadata(ctx, id, appstore.MetaAutoRenewAt, now.Add(time.Hour).Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	later := next.AddDate(0, 1, 0)
	if res := renew("d2", later); res.Action != "solo_renewed_after_team" || r.meta(t, id, appstore.MetaAutoRenew) != "off" ||
		r.meta(t, id, appstore.MetaExpiresAt) != later.Format(time.RFC3339) {
		t.Fatalf("a renewal charged before renewal was turned off: %+v", res)
	}
}
