package handlers

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/permit"
	"github.com/kiff/cloud/api/internal/purchase"
)

type receiptReader map[string]purchase.Receipt

func (r receiptReader) ReceiptOf(_ context.Context, _, caseID, _ string) (purchase.Receipt, error) {
	if rc, ok := r[caseID]; ok {
		return rc, nil
	}
	if caseID == "pc_held" {
		return purchase.Receipt{}, purchase.ErrNotEnded
	}
	return purchase.Receipt{}, purchase.ErrNoCase
}

// A purchase's receipt is signed with the receipt key and checks with it;
// 404 until the case has ended; 503, and no key set, without a key.
func TestAPurchaseReceiptIsSignedWithTheReceiptKey(t *testing.T) {
	seed := make([]byte, 32)
	seed[0] = 9
	k, err := permit.ParseKey("kiff-receipts-1:" + base64.RawURLEncoding.EncodeToString(seed))
	if err != nil {
		t.Fatal(err)
	}
	store := receiptReader{"pc_done": {CaseID: "pc_done", Version: 1, States: purchase.States{Case: "done"}}}
	off, offPublic := http.NewServeMux(), http.NewServeMux()
	(&PurchaseReceipts{Store: store}).Register(off, offPublic)
	if rr := do(t, off, admin(), http.MethodGet, "/v1/me/purchases/pc_done/receipt", nil); rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("without a key: %d", rr.Code)
	}
	if rr := do(t, offPublic, admin(), http.MethodGet, PurchaseReceiptPath, nil); rr.Code != http.StatusNotFound {
		t.Fatalf("a key set without a key: %d", rr.Code)
	}
	authed, public := http.NewServeMux(), http.NewServeMux()
	(&PurchaseReceipts{Store: store, Key: &k, Now: func() time.Time { return time.Date(2026, 11, 28, 9, 17, 0, 0, time.UTC) }}).Register(authed, public)
	for _, id := range []string{"pc_held", "pc_none"} {
		if rr := do(t, authed, admin(), http.MethodGet, "/v1/me/purchases/"+id+"/receipt", nil); rr.Code != http.StatusNotFound {
			t.Fatalf("%s: %d", id, rr.Code)
		}
	}
	rr := do(t, authed, admin(), http.MethodGet, "/v1/me/purchases/pc_done/receipt", nil)
	var rc purchase.Receipt
	if rr.Code != http.StatusOK || json.Unmarshal(rr.Body.Bytes(), &rc) != nil || rc.Kid != "kiff-receipts-1" || purchase.VerifyReceipt(k.Public(), rc) != nil {
		t.Fatalf("receipt: %d %s", rr.Code, rr.Body.String())
	}
	if rr := do(t, public, admin(), http.MethodGet, PurchaseReceiptPath, nil); rr.Code != http.StatusOK || rr.Header().Get("Content-Type") != "application/jwk-set+json" {
		t.Fatalf("key set: %d %s", rr.Code, rr.Body.String())
	}
}
