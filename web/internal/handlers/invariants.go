package handlers

import (
	"log/slog"
	"net/http"

	"github.com/kiff/cloud/web/internal/content/invariants"
	"github.com/kiff/cloud/web/internal/pages"
)

// Invariants serves /invariants, the published guarantees and their
// proofs.
//
// A parse failure here means the embedded artifact is malformed, which
// would render an empty promise. Serving 500 is correct: a page that
// claims guarantees must not appear with none on it.
func Invariants() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		groups, err := invariants.Grouped()
		if err != nil || len(groups) == 0 {
			slog.Error("invariants: cannot render", "err", err, "groups", len(groups))
			http.Error(w, "invariants unavailable", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = pages.Invariants(groups).Render(r.Context(), w)
	})
}
