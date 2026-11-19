package handlers

import (
	"net/http"

	"github.com/kiff/cloud/web/internal/pages"
)

// Cookies serves the Cookie Policy at /cookies. Draft for legal review. Keep the public
// surface free of private repository links and internal review notes.
func Cookies() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = pages.Cookies().Render(r.Context(), w)
	})
}
