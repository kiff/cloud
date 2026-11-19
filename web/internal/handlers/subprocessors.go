package handlers

import (
	"net/http"

	"github.com/kiff/cloud/web/internal/pages"
)

// Subprocessors serves the list of subprocessors at /subprocessors. Draft for legal review. Keep the public
// surface free of private repository links and internal review notes.
func Subprocessors() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = pages.Subprocessors().Render(r.Context(), w)
	})
}
