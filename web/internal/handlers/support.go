package handlers

import (
	"net/http"

	"github.com/kiff/cloud/web/internal/pages"
)

// Support serves /support: how to reach a person, the status page, and how
// to delete an account. The App Store listing points here, so the page
// names no price and no way to buy. Keep the public surface free of
// private repository links and internal review notes.
func Support() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = pages.Support().Render(r.Context(), w)
	})
}
