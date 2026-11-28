package handlers

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"github.com/kiff/cloud/api/internal/auth"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/mandate"
	"github.com/kiff/cloud/api/internal/purchase"
)

type oneCase struct {
	row purchase.Row
	err error
}

func (o oneCase) Latest(context.Context, string, string) (purchase.Row, error) { return o.row, o.err }

func contractRow(t *testing.T, state string, until time.Time) purchase.Row {
	t.Helper()
	raw, err := os.ReadFile("../purchase/testdata/purchase.json")
	if err != nil {
		t.Fatal(err)
	}
	var d struct {
		Case struct {
			Value  json.RawMessage `json:"value"`
			Digest string          `json:"digest"`
		} `json:"case"`
	}
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatal(err)
	}
	var c purchase.Case
	if err := json.Unmarshal(d.Case.Value, &c); err != nil {
		t.Fatal(err)
	}
	return purchase.Row{Case: c, Digest: d.Case.Digest, State: state, LeaseHolder: "exe_1", ValidUntil: until}
}

// issue_card's permit is signed only for the case KIFF approved and is
// executing, with its exact arguments; anything else, or no purchase store,
// is refused before anything is bound.
func TestIssueCardsPermitIsOnlyForTheExecutingCase(t *testing.T) {
	ctx := context.Background()
	until := time.Now().Add(10 * time.Minute)
	row := contractRow(t, purchase.StateExecuting, until)
	args, err := purchase.CardArgs(row, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	h := &ProposalsHandler{purchases: oneCase{row: row}}
	if why := h.purchaseCardRefusal(ctx, "t_acme", args); why != "" {
		t.Fatalf("the executing case: %s", why)
	}
	for name, hh := range map[string]*ProposalsHandler{
		"no purchase store":       {},
		"no such case":            {purchases: oneCase{err: errors.New("none")}},
		"approved, not executing": {purchases: oneCase{row: contractRow(t, purchase.StateApproved, until)}},
		"ended":                   {purchases: oneCase{row: contractRow(t, purchase.StateExecuting, time.Now().Add(-time.Second))}},
	} {
		if why := hh.purchaseCardRefusal(ctx, "t_acme", args); why == "" {
			t.Errorf("%s: allowed", name)
		}
	}
	if why := h.purchaseCardRefusal(ctx, "t_acme", json.RawMessage(`{"case":{}}`)); why == "" {
		t.Error("no case id: allowed")
	}
}

// A purchase's card is money, but its case was the one decision: K4 doesn't
// ask the owner a second time for it. Any other money call still asks.
func TestAPurchasesCardIsNotAskedTwice(t *testing.T) {
	h := &ProposalsHandler{k4: map[string]bool{"t1": true}}
	money := withClass(context.Background(), callClass{class: "money"})
	var plain mandate.Request
	h.classInto(money, "t1", &plain)
	if plain.OwnerAsk == "" {
		t.Fatal("a money call isn't asked")
	}
	var card mandate.Request
	h.classInto(withPurchaseDecided(money), "t1", &card)
	if card.OwnerAsk != "" || card.Class != "money" {
		t.Fatalf("the purchase's card: %+v", card)
	}
}

type fakeHolds struct {
	row      purchase.Row
	decided  *purchase.Decision
	refused  bool
	decideTo error
}

func (f *fakeHolds) ByException(context.Context, string, string) (purchase.Row, error) {
	return f.row, nil
}
func (f *fakeHolds) Decide(_ context.Context, _, _ string, _ int, _ string, d purchase.Decision) error {
	f.decided = &d
	return f.decideTo
}
func (f *fakeHolds) Refuse(context.Context, string, string, int, string) error {
	f.refused = true
	return nil
}

// The owner's answer to a purchase hold decides its case: approve-once
// reserves room with no within-limits ceiling (outside them is what waits
// for the owner), reject refuses it, and a room another
// purchase took approves nothing.
func TestTheOwnersAnswerDecidesThePurchase(t *testing.T) {
	ctx := context.Background()
	row := contractRow(t, purchase.StateHeld, time.Time{})
	store := mandate.NewMemoryStore()
	card := spendMandateForPurchase(row.Case.Card.ID)
	if err := store.Issue(ctx, card, "owner"); err != nil {
		t.Fatal(err)
	}
	answer := func(f *fakeHolds, a string) int {
		h := &MandatesHandler{store: store, purchaseHolds: f}
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/", nil)
		if h.decidePurchase(rec, req, card.TenantID, "exc_1", a) {
			return rec.Code
		}
		return 0
	}
	f := &fakeHolds{row: row}
	if code := answer(f, "approve-once"); code != 0 || f.decided == nil || f.decided.By != purchase.ByOwner || f.decided.Limits.PerDay != 0 {
		t.Fatalf("approve: %d %+v", code, f.decided)
	}
	f = &fakeHolds{row: row}
	if code := answer(f, "reject"); code != 0 || !f.refused || f.decided != nil {
		t.Fatalf("reject: %d", code)
	}
	f = &fakeHolds{row: row, decideTo: purchase.ErrRoomTaken}
	if code := answer(f, "approve-once"); code != http.StatusConflict {
		t.Fatalf("room taken: %d", code)
	}
	other := row
	other.Case.Card.ID = "card_gone"
	if code := answer(&fakeHolds{row: other}, "approve-once"); code != http.StatusConflict {
		t.Fatalf("no Card in force: %d", code)
	}
}

func spendMandateForPurchase(id string) mandate.Mandate {
	return mandate.Mandate{ID: id, TenantID: "t_acme", Subject: "agent://agt_supplies", Domain: "gateway", Environment: "prod",
		Grants: []mandate.Grant{{Action: "issue_card", Opens: []mandate.Opening{{Class: "money"}},
			Purchase: &mandate.PurchaseRule{Mode: mandate.PurchaseAskEachTime, Currency: "EUR", PerDay: 20000,
				Categories: []string{"stationery_office_supplies_printing_and_writing_paper"}}}},
		ValidFrom: time.Now().Add(-time.Hour), ValidUntil: time.Now().Add(time.Hour)}
}

// A purchase is approved only on the phone, its signature over the case's
// purchase line: the dashboard's session and an API key (holds.answer or
// not) are refused before anything is reserved, a phone's signature without
// the line is refused, the right one approves; refusing stays open.
// Category: authority
// Attack: an account API key holding holds.answer approves a purchase with no fingerprint and no case signed
func TestAPurchaseIsApprovedOnlyByThePhonesSignatureOverItsCase(t *testing.T) {
	store := mandate.NewMemoryStore()
	store.SetClock(func() time.Time { return time.Date(2026, 9, 12, 8, 0, 0, 0, time.UTC) })
	row := contractRow(t, purchase.StateHeld, time.Time{})
	holds := &fakeHolds{row: row}
	h := NewMandatesHandler(store, nil, WithMandatesClock(func() time.Time { return time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC) }), WithMandatesPurchaseHolds(holds))
	mux := http.NewServeMux()
	h.Register(mux)
	card := mandate.Mandate{ID: "card-x", TenantID: "t1", Subject: "agent://a", OnExceed: mandate.OnExceedOwner,
		Grants: []mandate.Grant{{Action: "REFUND_INVOICE", PerAction: []mandate.PerActionLimit{{
			Quantity: mandate.Quantity{Kind: mandate.KindSum, Parameter: "amount"}, Max: 100}}}}}
	buying := spendMandateForPurchase(row.Case.Card.ID)
	buying.TenantID = "t1"
	buying.ValidFrom, buying.ValidUntil = time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	for _, m := range []mandate.Mandate{card, buying} {
		if err := store.Issue(context.Background(), m, "owner"); err != nil {
			t.Fatal(err)
		}
	}
	e := heldForDevice(t, store, "p-buy")
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	der, _ := x509.MarshalPKIXPublicKey(&key.PublicKey)
	phone := owner()
	phone.Method, phone.DeviceKey, phone.DeviceSessionID = auth.MethodDeveloper, der, "sess-phone"
	signedAt := time.Date(2026, 9, 12, 11, 59, 30, 0, time.UTC).Format(time.RFC3339)
	sign := func(a auth.Answer) map[string]any {
		digest := sha256.Sum256(a.Message())
		sig, _ := ecdsa.SignASN1(rand.Reader, key, digest[:])
		return map[string]any{"signed_at": a.SignedAt, "value": base64.StdEncoding.EncodeToString(sig)}
	}
	post := func(id auth.Identity, verb string, body any) (int, string) {
		rr := do(t, mux, id, http.MethodPost, "/v1/me/card-exceptions/"+e.ID+"/"+verb, body)
		return rr.Code, rr.Body.String()
	}
	apiKey := owner()
	apiKey.Method, apiKey.APIKey = auth.MethodAPIKey, true
	for name, id := range map[string]auth.Identity{"the dashboard's session": owner(), "an API key": apiKey} {
		if code, body := post(id, "approve-once", map[string]any{}); code != http.StatusForbidden || !strings.Contains(body, "device_required") {
			t.Errorf("%s: %d %s", name, code, body)
		}
	}
	if holds.decided != nil {
		t.Fatalf("a refused approval decided the case: %+v", holds.decided)
	}
	plain := auth.Answer{TenantID: "t1", ExceptionID: e.ID, Answer: "approve-once", AccountingVersion: e.AccountingVersion, SignedAt: signedAt}
	if code, body := post(phone, "approve-once", map[string]any{"signature": sign(plain)}); code != http.StatusForbidden || !strings.Contains(body, "signature_invalid") {
		t.Errorf("a signature without the purchase line: %d %s", code, body)
	}
	if holds.decided != nil {
		t.Fatal("a signature without the purchase line decided the case")
	}
	withLine := plain
	withLine.Purchase = purchase.AnswerLine(row.Case.CaseID, row.Case.Version, row.Digest)
	if code, body := post(phone, "approve-once", map[string]any{"signature": sign(withLine)}); code != http.StatusOK {
		t.Fatalf("the phone's signature over the case: %d %s", code, body)
	}
	if holds.decided == nil || holds.decided.Approver != "alice" || holds.decided.Method != "device_biometric" {
		t.Fatalf("decided %+v", holds.decided)
	}
	// Refusing stays open to anyone who may answer: the dashboard's session refuses it.
	refusing := &fakeHolds{row: row}
	h2 := NewMandatesHandler(store, nil, WithMandatesClock(func() time.Time { return time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC) }), WithMandatesPurchaseHolds(refusing))
	mux2 := http.NewServeMux()
	h2.Register(mux2)
	e2 := heldForDevice(t, store, "p-buy-2")
	if rr := do(t, mux2, owner(), http.MethodPost, "/v1/me/card-exceptions/"+e2.ID+"/reject", map[string]any{}); rr.Code != http.StatusOK || !refusing.refused {
		t.Fatalf("the dashboard refusing: %d %s", rr.Code, rr.Body.String())
	}
}
