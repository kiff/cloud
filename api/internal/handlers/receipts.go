package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/receipt"
	"github.com/kiff/cloud/api/internal/words"
)

// ReceiptsHandler exposes the public receipt-read surface:
//
//	GET /v1/receipts/{trace_id} — return the receipt for an action
//	                              execution scoped to the caller's
//	                              tenant.
//
// The handler reads Identity from the request context that the
// auth middleware attached. Tenant scoping is enforced at the
// store layer: a tenant cannot read another tenant's receipt by
// guessing a trace id, because Get keys on (tenant_id, trace_id).
//
// What the handler does *not* do (yet):
//
//   - List receipts for a tenant. Adds /v1/receipts later, alongside
//     pagination — keeping v0 single-receipt-only matches the receipts design's
//     "smallest experiment" framing.
//   - Re-attest on read. The receipt is whatever the Store has;
//     anchor-then-update happens on the write path (slice 3) and
//     in the future hourly anchoring batch (the anchoring design production).
//   - Filter by entity. The dashboard will want this; v0 doesn't.
type ReceiptsHandler struct {
	store receipt.Store
}

// NewReceiptsHandler returns a handler backed by the given store.
func NewReceiptsHandler(store receipt.Store) *ReceiptsHandler {
	return &ReceiptsHandler{store: store}
}

// Register attaches the handler's routes to mux. Uses the Go
// 1.22+ ServeMux pattern syntax; idempotent if mux has not seen
// the patterns before.
func (h *ReceiptsHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/receipts", h.list)
	mux.HandleFunc("GET /v1/receipts/{trace_id}", h.get)
}

// receiptListResponse is the body shape returned by GET /v1/receipts.
// The receipts slice is empty (not nil) when no receipts exist
// so the dashboard's table renderer doesn't have to nil-check.
type receiptListResponse struct {
	Receipts []worded `json:"receipts"`
	Limit    int      `json:"limit"`
}

// worded is a receipt as stored, and its steps' words in the reader's
// language (Accept-Language) beside it, outside the signature. The receipt's
// own fields are encoded as they always were, so its causal_chain is byte
// for byte what KIFF signed.
type worded struct {
	receipt.Receipt
	StepWords []receipt.StepWord `json:"step_words"`
}

func withWords(rec receipt.Receipt, r *http.Request) worded {
	return worded{Receipt: rec, StepWords: receipt.StepWords(rec.CausalChain, words.Of(words.FromHeader(r.Header.Get("Accept-Language"))))}
}

// list handles GET /v1/receipts. Returns up to 50 of the caller
// tenant's most recent receipts in descending issued_at order.
// Pagination + filtering land if/when a real prospect asks; the
// dashboard's first cut is "show me what just happened."
//
// The response intentionally returns the same per-receipt shape
// as GET /v1/receipts/{trace_id} so the dashboard can render
// rows without re-fetching for the row's basics.
func (h *ReceiptsHandler) list(w http.ResponseWriter, r *http.Request) {
	id, err := auth.IdentityFrom(r.Context())
	if err != nil {
		writeReceiptError(w, http.StatusUnauthorized, err)
		return
	}
	if id.Tenant == "" {
		writeReceiptError(w, http.StatusUnauthorized, errors.New("identity has no tenant"))
		return
	}
	limit := parseLimit(r.URL.Query().Get("limit"), 50, 200)
	storeLimit := limit
	if id.DomainID != "" {
		// Fetch the bounded maximum before filtering so another domain's
		// recent rows do not trivially crowd this domain out of a small page.
		storeLimit = 200
	}
	rows, err := h.store.List(r.Context(), id.Tenant, storeLimit)
	if err != nil {
		writeReceiptError(w, http.StatusInternalServerError, err)
		return
	}
	if id.DomainID != "" {
		filtered := make([]receipt.Receipt, 0, len(rows))
		for _, row := range rows {
			if row.DomainID == id.DomainID {
				filtered = append(filtered, row)
			}
		}
		rows = filtered
		if len(rows) > limit {
			rows = rows[:limit]
		}
	}
	out := make([]worded, 0, len(rows))
	for _, row := range rows {
		out = append(out, withWords(row, r))
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(receiptListResponse{Receipts: out, Limit: limit})
}

// parseLimit returns a clamped limit value: defaults when empty
// or unparsable, capped at max so a misbehaving client can't
// pull the whole table at once.
func parseLimit(raw string, fallback, max int) int {
	if raw == "" {
		return fallback
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return fallback
	}
	if n > max {
		return max
	}
	return n
}

// parseOffset parses a non-negative ?offset= for pagination. Empty,
// malformed, or negative yields 0 (the first page). Capped at max to
// bound a pathological skip; a caller past the end gets an empty page.
func parseOffset(raw string, max int) int {
	if raw == "" {
		return 0
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		return 0
	}
	if n > max {
		return max
	}
	return n
}

// get handles GET /v1/receipts/{trace_id}. Returns 200 with the
// receipt body on success, 401 if no identity is on the context,
// 404 if no receipt matches the (tenant, trace) pair, 500 for
// store errors.
//
// The 404 path covers two conceptually distinct cases — "this
// receipt does not exist" and "this receipt belongs to a
// different tenant" — and intentionally renders both as 404. A
// 403 here would leak the receipt's existence to a tenant that
// has no business knowing it; 404 is the safer answer.
func (h *ReceiptsHandler) get(w http.ResponseWriter, r *http.Request) {
	id, err := auth.IdentityFrom(r.Context())
	if err != nil {
		writeReceiptError(w, http.StatusUnauthorized, err)
		return
	}
	if id.Tenant == "" {
		writeReceiptError(w, http.StatusUnauthorized, errors.New("identity has no tenant"))
		return
	}
	traceID := strings.TrimSpace(r.PathValue("trace_id"))
	if traceID == "" {
		writeReceiptError(w, http.StatusBadRequest, errors.New("missing trace_id"))
		return
	}

	rec, err := h.store.Get(r.Context(), id.Tenant, traceID)
	if err != nil {
		if errors.Is(err, receipt.ErrReceiptNotFound) {
			writeReceiptError(w, http.StatusNotFound, err)
			return
		}
		writeReceiptError(w, http.StatusInternalServerError, err)
		return
	}
	if id.DomainID != "" && rec.DomainID != id.DomainID {
		// Empty domain_id is legacy and cannot be attributed safely to a
		// bound key. Return the same 404 as an unknown trace to avoid
		// disclosing that another domain owns it.
		writeReceiptError(w, http.StatusNotFound, receipt.ErrReceiptNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Add("Vary", "Accept-Language")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(withWords(rec, r))
}

func writeReceiptError(w http.ResponseWriter, status int, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}
