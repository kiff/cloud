package handlers

import (
	"net/http"

	"github.com/kiff/cloud/web/internal/pages"
)

// SLA serves the service-level agreement at /legal/sla: what the Terms
// (§14) commit to on a paid plan. Keep the public surface free of private
// repository links and internal review notes.
func SLA() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = pages.SLA().Render(r.Context(), w)
	})
}
