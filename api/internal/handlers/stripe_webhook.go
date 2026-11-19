package handlers

import (
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/kiff/cloud/api/internal/billing"
)

// StripeWebhookHandler serves the unauthenticated Stripe webhook
// endpoint. It sits OUTSIDE the /v1 auth middleware — Stripe
// cannot present a tenant identity — so the Stripe-Signature header
// verified against the signing secret is the only trust boundary.
//
//	POST /webhooks/stripe
//
// Behavior:
//   - missing/invalid/expired signature       → 400 (never processed)
//   - body too large                           → 413
//   - valid signature, processed/duplicate     → 200
//   - valid signature, unmapped (no tenant)    → 200 (acked; recorded
//     for reconciliation — a non-2xx would make Stripe retry a
//     genuinely unmappable event forever)
//   - valid signature, transient store failure → 500 (Stripe retries)
type StripeWebhookHandler struct {
	processor *billing.Processor
	secret    string
	maxBody   int64
	now       func() time.Time
	logger    *slog.Logger
}

// NewStripeWebhookHandler wires the processor and signing secret.
// Returns nil when secret is empty so the caller can leave the route
// unmounted in environments without billing configured (mirrors the
// ops/alerts wiring).
func NewStripeWebhookHandler(processor *billing.Processor, secret string, logger *slog.Logger) *StripeWebhookHandler {
	if processor == nil || secret == "" {
		return nil
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &StripeWebhookHandler{
		processor: processor,
		secret:    secret,
		maxBody:   1 << 20, // 1 MiB — Stripe events are small
		now:       func() time.Time { return time.Now() },
		logger:    logger,
	}
}

// Register mounts the webhook on the (public) mux. No-op on a nil
// handler so an unconfigured environment simply has no route.
func (h *StripeWebhookHandler) Register(mux *http.ServeMux) {
	if h == nil {
		return
	}
	mux.Handle("POST /webhooks/stripe", h)
}

func (h *StripeWebhookHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, h.maxBody+1))
	if err != nil {
		http.Error(w, "read error", http.StatusBadRequest)
		return
	}
	if int64(len(body)) > h.maxBody {
		http.Error(w, "payload too large", http.StatusRequestEntityTooLarge)
		return
	}

	if err := billing.VerifySignature(body, r.Header.Get("Stripe-Signature"), h.secret, 0, h.now()); err != nil {
		// Do not echo the reason in detail to the caller; log it.
		h.logger.WarnContext(r.Context(), "billing: webhook signature rejected",
			slog.String("err", err.Error()))
		http.Error(w, "invalid signature", http.StatusBadRequest)
		return
	}

	event, err := billing.ParseEvent(body)
	if err != nil {
		h.logger.WarnContext(r.Context(), "billing: webhook parse failed", slog.String("err", err.Error()))
		http.Error(w, "bad event", http.StatusBadRequest)
		return
	}

	res, err := h.processor.Process(r.Context(), event)
	if err != nil {
		// Transient failure: 500 so Stripe retries; idempotency makes
		// the retry safe.
		h.logger.ErrorContext(r.Context(), "billing: webhook processing failed",
			slog.String("event", event.ID), slog.String("type", event.Type), slog.String("err", err.Error()))
		http.Error(w, "processing error", http.StatusInternalServerError)
		return
	}

	// All verified, non-erroring outcomes ack with 200 so Stripe
	// stops redelivering. Duplicate/unmapped/ignored are intentional
	// no-ops already logged by the processor.
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
	_ = res
}
