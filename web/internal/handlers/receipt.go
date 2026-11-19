package handlers

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/kiff/cloud/web/internal/apiclient"
	"github.com/kiff/cloud/web/internal/pages"
)

// Receipt serves the receipt dashboard at
// /dashboard/receipts/{trace_id}. The handler proxies a single
// authenticated GET to the apps/api binary using the operator API
// key the binary was started with.
//
// Five outcomes:
//
//   - configured + receipt found     → 200 with the rendered page
//   - configured + receipt missing   → 404 with the "not found" page
//   - configured + transport error   → 502 with the error page
//   - configured + 401 from API     → 502 with the error page (the
//     operator key is bad; do not
//     surface that to the visitor)
//   - not configured (no client)     → 200 with the "not configured"
//     explanatory page
//
// The "not configured" path renders 200, not 503, because public
// landing-site visits to /dashboard/* on an unconfigured deploy
// should not look like an outage.
//
// The handler does not enforce per-user auth. v0.1 ships the
// dashboard behind a single operator key the cloud team
// controls; production deploys with multi-tenant logins are
// gated on a separate slice that is not on this issue's path.
func Receipt(client *apiclient.Client, logger *slog.Logger) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")

		if client == nil || !client.Configured() {
			_ = pages.ReceiptNotConfigured().Render(r.Context(), w)
			return
		}

		traceID := strings.TrimSpace(r.PathValue("trace_id"))
		if traceID == "" {
			w.WriteHeader(http.StatusBadRequest)
			_ = pages.ReceiptError().Render(r.Context(), w)
			return
		}

		rec, err := client.GetReceipt(r.Context(), traceID)
		if err != nil {
			switch {
			case errors.Is(err, apiclient.ErrReceiptNotFound):
				w.WriteHeader(http.StatusNotFound)
				_ = pages.ReceiptNotFound(traceID).Render(r.Context(), w)
			case errors.Is(err, apiclient.ErrUnauthorized):
				logger.ErrorContext(r.Context(), "dashboard: api unauthorized; check KIFF_DEMO_API_KEY",
					slog.String("trace_id", traceID))
				w.WriteHeader(http.StatusBadGateway)
				_ = pages.ReceiptError().Render(r.Context(), w)
			default:
				logger.ErrorContext(r.Context(), "dashboard: api fetch failed",
					slog.String("trace_id", traceID),
					slog.String("err", err.Error()))
				w.WriteHeader(http.StatusBadGateway)
				_ = pages.ReceiptError().Render(r.Context(), w)
			}
			return
		}

		_ = pages.Receipt(pages.ReceiptView{R: rec}).Render(r.Context(), w)
	})
}
