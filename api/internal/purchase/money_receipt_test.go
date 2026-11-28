package purchase

import (
	"encoding/base64"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/permit"
)

// Reconciliation's Record lines: captured as authorized, less, split, above
// (an exception), a force capture (an exception), reversed, refunded,
// lapsed.
func TestSettlementGivesTheDefinedMoneyStatus(t *testing.T) {
	ok := map[string]bool{"a1": true}
	auth := CardEvent{Kind: "authorization", Authorization: "a1", Amount: 8570}
	for name, tc := range map[string]struct {
		events    []CardEvent
		want      string
		exception bool
	}{
		"authorized":     {[]CardEvent{auth}, MoneyAuthorized, false},
		"captured":       {[]CardEvent{auth, {Kind: "capture", Authorization: "a1", Amount: 8570}}, MoneyCaptured, false},
		"captured less":  {[]CardEvent{auth, {Kind: "capture", Authorization: "a1", Amount: 7990}}, MoneyCapturedLess, false},
		"split":          {[]CardEvent{auth, {Kind: "capture", Authorization: "a1", Amount: 5000}, {Kind: "capture", Authorization: "a1", Amount: 3570}}, MoneySplit, false},
		"over capture":   {[]CardEvent{auth, {Kind: "capture", Authorization: "a1", Amount: 9170}}, MoneyOverCapture, true},
		"force capture":  {[]CardEvent{{Kind: "capture", Amount: 8420}}, MoneyForceCapture, true},
		"another's auth": {[]CardEvent{{Kind: "capture", Authorization: "a9", Amount: 100}}, MoneyForceCapture, true},
		"reversed":       {[]CardEvent{auth, {Kind: "reversal", Authorization: "a1", Amount: 8570}}, MoneyReversed, false},
		"refunded":       {[]CardEvent{auth, {Kind: "capture", Authorization: "a1", Amount: 8570}, {Kind: "refund", Amount: 8570}}, MoneyRefunded, false},
		"lapsed":         {[]CardEvent{auth, {Kind: "reversal", Authorization: "a1", Amount: 1}, {Kind: "lapse"}}, MoneyLapsed, false},
		"none":           {nil, MoneyNone, false},
	} {
		got := Settle(tc.events, ok, 8620)
		if got.Money != tc.want || got.Exception != tc.exception {
			t.Errorf("%s: %+v, want %s (exception %v)", name, got, tc.want, tc.exception)
		}
	}
}

func receiptKey(t *testing.T) permit.Key {
	t.Helper()
	seed := make([]byte, 32)
	for i := range seed {
		seed[i] = byte(i + 7)
	}
	k, err := permit.ParseKey("kiff-receipts-1:" + base64.RawURLEncoding.EncodeToString(seed))
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// The receipt verifies with KIFF's key, and any changed field fails it.
func TestTheReceiptIsSignedOverEveryField(t *testing.T) {
	c, digest := contractCase(t)
	k := receiptKey(t)
	r, err := SignReceipt(k, Receipt{ReceiptID: "rc_1", CaseID: c.CaseID, Version: c.Version, Digest: digest, Path: c.Path, DecidedBy: ByOwner,
		Approver: Approver{Role: "owner", Self: true, Method: "device_biometric"},
		Merchant: ReceiptMerchant{Case: NameCategory{c.Merchant.Name, c.Merchant.Category}, Issuer: NameCategory{"PAPETERIE LUMIERE LYON", "5943"}},
		Amounts:  Amounts{ApprovedMax: c.Price.Max, Authorized: 8570, Captured: 8570, Currency: c.Currency},
		States:   States{Case: StateDone, Money: MoneyCaptured, Order: "confirmed"}, Verification: "exempted",
		Words: map[string]string{"state": "Bought"}}, time.Date(2026, 11, 28, 9, 16, 52, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyReceipt(k.Public(), r); err != nil {
		t.Fatal(err)
	}
	r.Amounts.Captured++
	if VerifyReceipt(k.Public(), r) == nil {
		t.Fatal("a changed amount verified")
	}
}
