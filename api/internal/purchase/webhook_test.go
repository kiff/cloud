package purchase

import (
	"crypto/hmac"
	"crypto/sha256"
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
