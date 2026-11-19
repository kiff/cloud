package handlers

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/kiff/cloud/api/internal/appstore"
	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/membership"
)

// AppleHandler serves the tafs app's Apple in-app purchases:
//
//	POST /webhooks/apple                      App Store Server Notifications V2; unauthenticated, Apple's signature is the trust
//	POST /v1/me/billing/apple/transactions    {"signed_transaction": …, "request_id": …}: a purchase, restore or update the app saw
//
// Both verify Apple's signed payload (its x5c chain to the pinned Apple
// root) before reading anything in it. The app finishes a transaction only
// once this route accepted it, so a purchase KIFF hasn't recorded is sent
// again at the app's next launch.
type AppleHandler struct {
	verifier  *appstore.Verifier
	processor *appstore.Processor
	logger    *slog.Logger
}

// NewAppleHandler returns nil when either is missing, leaving the routes
// unmounted.
func NewAppleHandler(v *appstore.Verifier, p *appstore.Processor, logger *slog.Logger) *AppleHandler {
	if v == nil || p == nil {
		return nil
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &AppleHandler{verifier: v, processor: p, logger: logger}
}

// RegisterPublic mounts the notifications webhook on the public mux.
func (h *AppleHandler) RegisterPublic(mux *http.ServeMux) {
	if h == nil {
		return
	}
	mux.HandleFunc("POST /webhooks/apple", h.notification)
}

// Register mounts the app's route on the authenticated mux.
func (h *AppleHandler) Register(mux *http.ServeMux) {
	if h == nil {
		return
	}
	mux.HandleFunc("POST /v1/me/billing/apple/transactions", h.transaction)
}

// maxAppleBody bounds a signed payload: a notification carries two JWS,
// each with three certificates.
const maxAppleBody = 64 << 10

// notification applies one App Store Server Notification. Like the Stripe
// webhook: a bad signature is 400 and never processed; processed,
// duplicate and unmapped are 200 (Apple stops retrying); a transient
// failure is 500, so Apple retries, and idempotency makes that safe.
func (h *AppleHandler) notification(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxAppleBody+1))
	if err != nil || len(body) > maxAppleBody {
		http.Error(w, "payload too large", http.StatusRequestEntityTooLarge)
		return
	}
	var in struct {
		SignedPayload string `json:"signedPayload"`
	}
	if err := json.Unmarshal(body, &in); err != nil || in.SignedPayload == "" {
		http.Error(w, "send {\"signedPayload\": …}", http.StatusBadRequest)
		return
	}
	n, txn, err := h.verifier.DecodeNotification(in.SignedPayload)
	if err != nil {
		h.logger.WarnContext(r.Context(), "appstore: notification refused", slog.String("err", err.Error()))
		http.Error(w, "invalid signature", http.StatusBadRequest)
		return
	}
	res, err := h.processor.Notify(r.Context(), n, txn)
	if err != nil {
		h.logger.ErrorContext(r.Context(), "appstore: notification failed", slog.String("notification", n.NotificationUUID),
			slog.String("type", n.NotificationType), slog.String("err", err.Error()))
		http.Error(w, "processing error", http.StatusInternalServerError)
		return
	}
	h.logger.InfoContext(r.Context(), "appstore: notification", slog.String("notification", n.NotificationUUID),
		slog.String("type", n.NotificationType), slog.String("subtype", n.Subtype), slog.String("tenant", res.TenantID),
		slog.String("action", res.Action), slog.Bool("duplicate", res.Duplicate), slog.Bool("unmapped", res.Unmapped))
	w.WriteHeader(http.StatusOK)
}

// transaction records a transaction the app saw: a purchase, a restored
// one, or one Apple updated (a renewal, a refund). Buying or restoring is
// the billing admin's; the transaction must carry the caller's account's
// appAccountToken. The answer says whether the app may finish it.
func (h *AppleHandler) transaction(w http.ResponseWriter, r *http.Request) {
	id, err := auth.IdentityFrom(r.Context())
	if err != nil || strings.TrimSpace(id.Tenant) == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "sign in to an account"})
		return
	}
	if id.AgentID != "" || (id.MemberRole != "" && !membership.Can(id.MemberRole, membership.CapBilling)) || (id.MemberRole == "" && !id.ManagesAccount()) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "adding tasks is the account's billing admin's"})
		return
	}
	var in struct {
		SignedTransaction string `json:"signed_transaction"`
		RequestID         string `json:"request_id"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxAppleBody)).Decode(&in); err != nil || in.SignedTransaction == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": `send {"signed_transaction": <the transaction's JWS>}`})
		return
	}
	txn, err := h.verifier.DecodeTransaction(in.SignedTransaction)
	if err != nil {
		h.logger.WarnContext(r.Context(), "appstore: transaction refused", slog.String("tenant", id.Tenant), slog.String("err", err.Error()))
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "not a transaction signed by Apple for this app"})
		return
	}
	res, err := h.processor.Purchased(r.Context(), txn, appstore.Caller{TenantID: id.Tenant, Subject: id.Subject, RequestID: strings.TrimSpace(in.RequestID)})
	switch {
	case errors.Is(err, appstore.ErrNotYours):
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "not_this_account", "message": "This purchase belongs to another account."})
		return
	case errors.Is(err, appstore.ErrBilledElsewhere), errors.Is(err, appstore.ErrNotSolo):
		writeJSON(w, http.StatusConflict, map[string]string{"error": "billed_elsewhere", "message": "This account's plan is billed elsewhere."})
		return
	case errors.Is(err, appstore.ErrSandbox):
		// Retrying changes nothing: the app may finish it.
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "test_purchase",
			"message": "Test purchases don't add tasks to this account."})
		return
	case errors.Is(err, appstore.ErrUnknownProduct):
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "unknown_product"})
		return
	case err != nil:
		h.logger.ErrorContext(r.Context(), "appstore: transaction failed", slog.String("tenant", id.Tenant), slog.String("err", err.Error()))
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not record the purchase"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"recorded": true, "action": res.Action, "duplicate": res.Duplicate})
}
