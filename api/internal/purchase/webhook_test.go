package purchase

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func sign(secret string, at time.Time, body string) string {
	ts := fmt.Sprint(at.Unix())
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts + "." + body))
	return "t=" + ts + ",v1=" + hex.EncodeToString(mac.Sum(nil))
}

func TestOnlyASignedFreshEventIsRead(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	body := []byte(`{"id":"evt_1"}`)
	if !ValidSignature("whsec_x", sign("whsec_x", now, string(body)), body, now) {
		t.Fatal("a good signature")
	}
	for name, h := range map[string]string{
		"another secret": sign("whsec_y", now, string(body)),
		"another body":   sign("whsec_x", now, `{"id":"evt_2"}`),
		"too old":        sign("whsec_x", now.Add(-6*time.Minute), string(body)),
		"no v1":          "t=" + fmt.Sprint(now.Unix()),
		"empty":          "",
	} {
		if ValidSignature("whsec_x", h, body, now) {
			t.Errorf("%s: read", name)
		}
	}
	if ValidSignature("", sign("", now, string(body)), body, now) {
		t.Error("no secret: read")
	}
}

func authorizationEvent(id, authID, card, account string, amount int64, history int) string {
	hist := make([]string, history)
	for i := range hist {
		hist[i] = "{}"
	}
	return fmt.Sprintf(`{"id":%q,"type":"issuing_authorization.request","account":%q,"data":{"object":{"id":%q,"card":{"id":%q},
		"pending_request":{"amount":%d,"currency":"eur"},"merchant_data":{"name":"PAPETERIE LUMIERE LYON","network_id":"","country":"FR",
		"category":"stationery_office_supplies_printing_and_writing_paper"},"request_history":[%s],"verification_data":{"three_d_secure":{"result":"exempted"}}}}}`,
		id, account, authID, card, amount, strings.Join(hist, ","))
}

// The webhook answers the matching case's authorization once, declines a
// replayed event, an unsigned one, and another account's.
func TestTheWebhookAnswersTheIssuerInItsBody(t *testing.T) {
	s, now := testStore(t)
	executing(t, s, "pc_hook", "ic_hook")
	h := &Webhook{Store: s, Secret: "whsec_test", Now: func() time.Time { return *now }}
	call := func(body string, signed bool) (int, bool) {
		req := httptest.NewRequest(http.MethodPost, "/v1/issuing/authorizations", strings.NewReader(body))
		if signed {
			req.Header.Set("Stripe-Signature", sign("whsec_test", *now, body))
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		var out struct {
			Approved bool `json:"approved"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out.Approved
	}
	if code, _ := call(authorizationEvent("evt_0", "iauth_h", "ic_hook", "acct_acme", 8570, 0), false); code != http.StatusBadRequest {
		t.Fatalf("unsigned: %d", code)
	}
	if code, ok := call(authorizationEvent("evt_x", "iauth_x", "ic_hook", "acct_other", 8570, 0), true); code != 200 || ok {
		t.Fatalf("another account: %d %v", code, ok)
	}
	if _, ok := call(authorizationEvent("evt_1", "iauth_h", "ic_hook", "acct_acme", 8570, 0), true); !ok {
		t.Fatal("the matching authorization")
	}
	if _, ok := call(authorizationEvent("evt_1", "iauth_h", "ic_hook", "acct_acme", 8570, 0), true); ok {
		t.Fatal("the same event replayed")
	}
	if _, ok := call(authorizationEvent("evt_2", "iauth_h2", "ic_hook", "acct_acme", 8570, 0), true); ok {
		t.Fatal("a second authorization")
	}
	if _, ok := call(authorizationEvent("evt_3", "iauth_h", "ic_hook", "acct_acme", 100, 1), true); ok {
		t.Fatal("an incremental past the maximum")
	}
}

func transactionEvent(id, txn, kind, card, account string, amount int64, auth string) string {
	a := "null"
	if auth != "" {
		a = fmt.Sprintf("%q", auth)
	}
	return fmt.Sprintf(`{"id":%q,"type":"issuing_transaction.created","account":%q,"data":{"object":{"id":%q,"type":%q,"amount":%d,"card":%q,"authorization":%s}}}`,
		id, account, txn, kind, amount, card, a)
}

// The issuer's captures and refunds settle the case's money, once each; the
// authorization keeps the issuer's merchant and verification for the
// receipt. A force capture pauses the Card's within-limits buying until the
// owner resumes it; money on a card no case issued, or from another
// account, isn't counted.
func TestMoneyEventsSettleTheCaseAndAForceCapturePausesTheCard(t *testing.T) {
	s, now := testStore(t)
	ctx := t.Context()
	c := executing(t, s, "pc_money", "ic_money")
	h := &Webhook{Store: s, Secret: "whsec_test", Now: func() time.Time { return *now }}
	call := func(body string) int {
		req := httptest.NewRequest(http.MethodPost, "/v1/issuing/authorizations", strings.NewReader(body))
		req.Header.Set("Stripe-Signature", sign("whsec_test", *now, body))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	call(authorizationEvent("evt_a", "iauth_m", "ic_money", "acct_acme", 8570, 0))
	var name, category, verification string
	if err := s.Pool.QueryRow(ctx, `SELECT merchant_name, merchant_category, verification FROM purchase.authorizations WHERE authorization_id = 'iauth_m#0'`).Scan(&name, &category, &verification); err != nil ||
		name != "PAPETERIE LUMIERE LYON" || category != "stationery_office_supplies_printing_and_writing_paper" || verification != "exempted" {
		t.Fatalf("the authorization's merchant: %q %q %q %v", name, category, verification, err)
	}
	// Stripe writes a capture as a negative amount.
	for range 2 {
		if code := call(transactionEvent("evt_c", "ipi_1", "capture", "ic_money", "acct_acme", -8570, "iauth_m")); code != http.StatusOK {
			t.Fatalf("capture: %d", code)
		}
	}
	got, err := s.Settlement(ctx, c.Tenant, c.CaseID, 1)
	if err != nil || got.Money != MoneyCaptured || got.Captured != 8570 || got.Authorized != 8570 || got.Exception {
		t.Fatalf("captured once: %+v %v", got, err)
	}
	if p, _ := s.PausedCard(ctx, c.Tenant, c.Card.ID); p.Reason != "" {
		t.Fatalf("paused after a matching capture: %+v", p)
	}
	// Another account's, and a card no case issued: kept apart, not counted.
	call(transactionEvent("evt_o", "ipi_o", "capture", "ic_money", "acct_other", -100, ""))
	call(transactionEvent("evt_n", "ipi_n", "capture", "ic_nobody", "acct_acme", -100, ""))
	if got, _ := s.Settlement(ctx, c.Tenant, c.CaseID, 1); got.Captured != 8570 {
		t.Fatalf("counted another's: %+v", got)
	}
	var orphan int
	if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM purchase.card_events WHERE card = 'ic_nobody' AND case_id = ''`).Scan(&orphan); err != nil || orphan != 1 {
		t.Fatalf("the card in no case's event: %d %v", orphan, err)
	}
	// A capture no authorization covers: a force capture, and the Card pauses.
	call(transactionEvent("evt_f", "ipi_f", "capture", "ic_money", "acct_acme", -500, ""))
	if got, _ := s.Settlement(ctx, c.Tenant, c.CaseID, 1); got.Money != MoneyForceCapture || !got.Exception {
		t.Fatalf("a force capture: %+v", got)
	}
	if p, _ := s.PausedCard(ctx, c.Tenant, c.Card.ID); p.Reason != MoneyForceCapture || p.CaseID != c.CaseID {
		t.Fatalf("the Card's pause: %+v", p)
	}
	within := Decision{By: ByCardLimits, Card: c.Card, Limits: Limits{PerPurchase: 10000, PerDay: 100000, PerHourCount: 5, Categories: []string{"5943"}}}
	next := newCase(t, "pc_after")
	digest := open(t, s, next)
	if err := s.Decide(ctx, next.Tenant, next.CaseID, 1, digest, within); !errors.Is(err, ErrOutsideLimits) {
		t.Fatalf("within limits while paused: %v", err)
	}
	if err := s.Resume(ctx, c.Tenant, c.Card.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Decide(ctx, next.Tenant, next.CaseID, 1, digest, within); err != nil {
		t.Fatalf("within limits once resumed: %v", err)
	}
}

// A receipt is given once the case has ended, read from what KIFF kept: the
// owner who decided and how, the issuer's merchant beside the case's, the
// money as captured, the order the shop confirmed. Signed, it checks with
// the receipt key; a case still deciding has none.
func TestTheReceiptSaysWhatWasDecidedAndWhatHappened(t *testing.T) {
	s, now := testStore(t)
	ctx := t.Context()
	c := newCase(t, "pc_rc")
	d := open(t, s, c)
	if _, err := s.ReceiptOf(ctx, c.Tenant, c.CaseID, "u_ana"); !errors.Is(err, ErrNotEnded) {
		t.Fatalf("a held case's receipt: %v", err)
	}
	dec := owner
	dec.Approver, dec.Method = "u_ana", "device_biometric"
	if err := s.Decide(ctx, c.Tenant, c.CaseID, 1, d, dec); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.Lease(ctx, c.Tenant, c.CaseID, 1, "exe_rc"); !ok || err != nil {
		t.Fatalf("lease: %v %v", ok, err)
	}
	if err := s.AttachCard(ctx, c.Tenant, c.CaseID, 1, "ic_rc"); err != nil {
		t.Fatal(err)
	}
	if a := s.Authorize(ctx, auth("iauth_rc", "ic_rc", 8570)); !a.Approved {
		t.Fatalf("authorization: %+v", a)
	}
	if _, err := s.RecordMoney(ctx, MoneyEvent{EventID: "evt_rc", Account: "acct_acme", Card: "ic_rc", Kind: EventCapture, Authorization: "iauth_rc", Amount: -8570}); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishOrdered(ctx, c.Tenant, c.CaseID, 1, "exe_rc", "PL-2026-118204", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	rc, err := s.ReceiptOf(ctx, c.Tenant, c.CaseID, "u_ana")
	if err != nil {
		t.Fatal(err)
	}
	if rc.States != (States{Case: StateDone, Money: MoneyCaptured, Order: "confirmed"}) || rc.Amounts.Captured != 8570 || rc.Amounts.ApprovedMax != c.Price.Max ||
		rc.Merchant.Issuer.Name != "PAPETERIE LUMIERE LYON" || rc.Merchant.Case.StripeProfile != c.Merchant.StripeProfile || rc.Verification != "exempted" ||
		rc.Approver != (Approver{Role: "owner", Self: true, Method: "device_biometric"}) || rc.Order == nil || rc.Order.Reference != "PL-2026-118204" || rc.DecidedBy != ByOwner {
		t.Fatalf("receipt %+v", rc)
	}
	if rc.Words["state"] != "Bought" || rc.Words["line"] != "Bought from Papeterie Lumière · €85.70 (€1.50 more for shipping, inside what you approved)" || rc.Words["shipping"] != "Order PL-2026-118204 confirmed by the shop." ||
		rc.Words["allowed"] != "You approved up to €86.20 at this shop, once." || rc.Words["refunds"] != "No refunds." {
		t.Fatalf("words %v", rc.Words)
	}
	// Another reader on the account isn't the approver; the receipt is the same receipt.
	other, _ := s.ReceiptOf(ctx, c.Tenant, c.CaseID, "u_vic")
	if other.Approver.Self || other.ReceiptID != rc.ReceiptID || other.IssuedAt != rc.IssuedAt {
		t.Fatalf("another reader's: %+v", other)
	}
	k := receiptKey(t)
	signed, err := SignReceipt(k, rc, *now)
	if err != nil || VerifyReceipt(k.Public(), signed) != nil {
		t.Fatalf("signed: %v", err)
	}
}

// The receipt keys are listed apart from the permit keys, a revoked one
// with when it was revoked.
func TestReceiptKeysListRevokedOnesWithWhen(t *testing.T) {
	k := receiptKey(t)
	pub := base64.RawURLEncoding.EncodeToString(k.Public().Pub)
	keys, err := ParseReceiptKeys("kiff-receipts-0:" + pub + ":2026-11-01T00:00:00Z, kiff-receipts-2:" + pub)
	if err != nil || len(keys) != 2 || keys[0].RevokedAt == nil || keys[1].RevokedAt != nil {
		t.Fatalf("parsed %+v %v", keys, err)
	}
	set := ReceiptJWKS(k.Public(), keys)["keys"]
	if len(set) != 3 || set[0].Kid != "kiff-receipts-1" || set[0].Crv != "Ed25519" || set[1].RevokedAt != "2026-11-01T00:00:00Z" || set[2].RevokedAt != "" {
		t.Fatalf("set %+v", set)
	}
	for _, bad := range []string{"nokey", "k:" + pub + ":yesterday", "k:short"} {
		if _, err := ParseReceiptKeys(bad); err == nil {
			t.Errorf("%q: read", bad)
		}
	}
}
