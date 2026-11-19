package handlers

import (
	"net/http"

	"github.com/kiff/cloud/web/internal/pages"
)

// AcceptableUse serves the Acceptable Use Policy at /acceptable-use. Draft for legal review. Keep the public
// surface free of private repository links and internal review notes.
func AcceptableUse() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = pages.AcceptableUse().Render(r.Context(), w)
	})
}
