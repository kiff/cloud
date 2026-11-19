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
	"github.com/kiff/cloud/api/internal/playstore"
)

// GoogleHandler serves the tafs Android app's Google Play purchases:
//
//	POST /webhooks/google                  Play Real-time Developer Notifications, a Pub/Sub push; Google's OIDC token is the trust
//	POST /v1/me/billing/google/purchases   {"product": …, "purchase_token": …, "request_id": …}: a purchase the app saw
//
// Nothing in either body is trusted for state: the purchase token is read
// back from the Play Developer API, and the purchase must carry the
// caller's account's app_account_token as Play's obfuscatedAccountId. The
// answer comes only after the purchase is recorded (and Solo acknowledged),
// so the app consumes a pack only once KIFF has it. Without Google Play's
// config, both routes answer that it isn't set up.
type GoogleHandler struct {
	billing *playstore.Billing
	push    *playstore.PushVerifier
	logger  *slog.Logger
}

// NewGoogleHandler serves b's purchases and the pushes push verifies;
// either nil answers that Google Play billing isn't set up.
func NewGoogleHandler(b *playstore.Billing, push *playstore.PushVerifier, logger *slog.Logger) *GoogleHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &GoogleHandler{billing: b, push: push, logger: logger}
}

// RegisterPublic mounts the notifications webhook on the public mux.
func (h *GoogleHandler) RegisterPublic(mux *http.ServeMux) {
	mux.HandleFunc("POST /webhooks/google", h.notification)
}

// Register mounts the app's route on the authenticated mux.
func (h *GoogleHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/me/billing/google/purchases", h.purchase)
}

// maxGoogleBody bounds a push or a purchase: a notification is small.
const maxGoogleBody = 64 << 10

func notSetUp(w http.ResponseWriter) {
	writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "google_play_not_set_up",
		"message": "Google Play billing isn't set up."})
}

// notification applies one Play notification. A push without Google's
// token for this audience and service account is 401 and never read;
// applied, duplicate, ignored and unmapped are 200 (Pub/Sub stops
// retrying); a transient failure is 500, so Pub/Sub retries, and reading
// the purchase back each time makes that safe.
func (h *GoogleHandler) notification(w http.ResponseWriter, r *http.Request) {
	if h.billing == nil || h.push == nil {
		notSetUp(w)
		return
	}
	if err := h.push.Verify(r.Context(), r); err != nil {
		h.logger.WarnContext(r.Context(), "playstore: push refused", slog.String("err", err.Error()))
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxGoogleBody+1))
	if err != nil || len(body) > maxGoogleBody {
		http.Error(w, "payload too large", http.StatusRequestEntityTooLarge)
		return
	}
	n, err := playstore.DecodePush(body)
	if err != nil {
		// Retrying a malformed push changes nothing: acknowledged.
		h.logger.WarnContext(r.Context(), "playstore: push not a Play notification", slog.String("err", err.Error()))
		w.WriteHeader(http.StatusOK)
		return
	}
	out, err := h.billing.Notify(r.Context(), n)
	if err != nil {
		h.logger.ErrorContext(r.Context(), "playstore: notification failed", slog.String("message", n.MessageID),
			slog.String("type", n.Kind()), slog.String("err", err.Error()))
		http.Error(w, "processing error", http.StatusInternalServerError)
		return
	}
	h.logger.InfoContext(r.Context(), "playstore: notification", slog.String("message", n.MessageID), slog.String("type", n.Kind()),
		slog.String("tenant", out.TenantID), slog.String("action", out.Action), slog.Bool("ignored", out.Ignored),
		slog.Bool("unmapped", out.Unmapped), slog.Bool("acknowledged", out.Acknowledged))
	w.WriteHeader(http.StatusOK)
}

// purchase records a purchase the app saw. Buying is the billing admin's,
// as with Apple; the codes are the Apple route's.
func (h *GoogleHandler) purchase(w http.ResponseWriter, r *http.Request) {
	id, err := auth.IdentityFrom(r.Context())
	if err != nil || strings.TrimSpace(id.Tenant) == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "sign in to an account"})
		return
	}
	if id.AgentID != "" || (id.MemberRole != "" && !membership.Can(id.MemberRole, membership.CapBilling)) || (id.MemberRole == "" && !id.ManagesAccount()) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "adding tasks is the account's billing admin's"})
		return
	}
	if h.billing == nil {
		notSetUp(w)
		return
	}
	var in struct {
		Product       string `json:"product"`
		PurchaseToken string `json:"purchase_token"`
		RequestID     string `json:"request_id"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxGoogleBody)).Decode(&in); err != nil ||
		strings.TrimSpace(in.Product) == "" || strings.TrimSpace(in.PurchaseToken) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": `send {"product": <Play product id>, "purchase_token": <Play's purchase token>}`})
		return
	}
	out, err := h.billing.Purchased(r.Context(), in.Product, in.PurchaseToken,
		appstore.Caller{TenantID: id.Tenant, Subject: id.Subject, RequestID: strings.TrimSpace(in.RequestID)})
	switch {
	case errors.Is(err, appstore.ErrNotYours):
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "not_this_account", "message": "This purchase belongs to another account."})
		return
	case errors.Is(err, appstore.ErrBilledElsewhere), errors.Is(err, appstore.ErrNotSolo):
		writeJSON(w, http.StatusConflict, map[string]string{"error": "billed_elsewhere", "message": "This account's plan is billed elsewhere."})
		return
	case errors.Is(err, appstore.ErrSandbox):
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "test_purchase",
			"message": "Test purchases don't add tasks to this account."})
		return
	case errors.Is(err, appstore.ErrUnknownProduct):
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "unknown_product"})
		return
	case errors.Is(err, playstore.ErrNoPurchase):
		h.logger.WarnContext(r.Context(), "playstore: purchase refused", slog.String("tenant", id.Tenant), slog.String("err", err.Error()))
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "not a purchase Google Play knows for this app"})
		return
	case errors.Is(err, playstore.ErrPending):
		// Not paid yet: nothing added, and the app keeps it; Play's
		// notification applies it once paid.
		writeJSON(w, http.StatusConflict, map[string]string{"error": "purchase_pending", "message": "This purchase isn't paid yet."})
		return
	case err != nil:
		h.logger.ErrorContext(r.Context(), "playstore: purchase failed", slog.String("tenant", id.Tenant), slog.String("err", err.Error()))
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not record the purchase"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"recorded": true, "action": out.Action, "duplicate": out.Duplicate, "acknowledged": out.Acknowledged})
}
