package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/permit"
	"github.com/kiff/cloud/api/internal/purchase"
)

// PurchaseReceiptPath is where KIFF publishes its purchase receipt keys:
// apart from the permit keys, since a receipt key never authorizes a call.
// The apps pin the key; this set is the record for any other reader.
const PurchaseReceiptPath = "/.well-known/kiff-receipt-keys.json"

// PurchaseReceiptReader builds a case's receipt, unsigned.
type PurchaseReceiptReader interface {
	ReceiptOf(ctx context.Context, tenant, caseID, reader string) (purchase.Receipt, error)
}

// PurchaseReceipts serves a purchase's signed receipt and the receipt keys.
// Without a key (KIFF_PURCHASE_RECEIPT_KEY unset), the receipt answers 503
// and no key is published.
type PurchaseReceipts struct {
	Store  PurchaseReceiptReader
	Key    *permit.Key
	Others []purchase.ReceiptKey
	Now    func() time.Time
}

// Register attaches the routes.
func (h *PurchaseReceipts) Register(authed, public *http.ServeMux) {
	authed.HandleFunc("GET /v1/me/purchases/{case_id}/receipt", h.receipt)
	if h.Key != nil {
		body, _ := json.Marshal(purchase.ReceiptJWKS(h.Key.Public(), h.Others))
		public.HandleFunc("GET "+PurchaseReceiptPath, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/jwk-set+json")
			w.Header().Set("Cache-Control", "public, max-age=300")
			w.Header().Set("Access-Control-Allow-Origin", "*")
			_, _ = w.Write(body)
		})
	}
}

// receipt answers the case's latest version's receipt, signed: for a person
// on the account (or its read-only key), never an agent's or a runtime's
// key; 404 until the case has ended.
func (h *PurchaseReceipts) receipt(w http.ResponseWriter, r *http.Request) {
	id, err := auth.IdentityFrom(r.Context())
	if err != nil || strings.TrimSpace(id.Tenant) == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "a session is required"})
		return
	}
	if ((id.APIKey || id.Method == auth.MethodAPIKey) && !id.ReadOnlyAccountKey()) || id.DomainID != "" {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "human session required"})
		return
	}
	if h.Key == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "purchase receipts aren't signed on this KIFF yet"})
		return
	}
	rc, err := h.Store.ReceiptOf(r.Context(), id.Tenant, r.PathValue("case_id"), id.Subject)
	switch {
	case errors.Is(err, purchase.ErrNoCase), errors.Is(err, purchase.ErrNotEnded):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no receipt for this purchase yet"})
		return
	case err != nil:
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "read the receipt failed"})
		return
	}
	now := time.Now()
	if h.Now != nil {
		now = h.Now()
	}
	signed, err := purchase.SignReceipt(*h.Key, rc, now)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "sign the receipt failed"})
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, signed)
}
