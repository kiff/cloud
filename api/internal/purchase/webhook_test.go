package purchase

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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
