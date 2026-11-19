package handlers

import (
	"log/slog"
	"net/http"

	"github.com/kiff/cloud/web/internal/content/whitepaper"
	"github.com/kiff/cloud/web/internal/pages"
)

// Whitepaper serves the long-form whitepaper page at /whitepaper.
// Markdown rendering is memoized inside the whitepaper package, so
// every request reuses the same already-parsed HTML.
func Whitepaper() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rendered, err := whitepaper.HTML()
		if err != nil {
			slog.ErrorContext(r.Context(), "whitepaper render failed", slog.String("err", err.Error()))
			http.Error(w, "whitepaper temporarily unavailable", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = pages.Whitepaper(rendered).Render(r.Context(), w)
	})
}
