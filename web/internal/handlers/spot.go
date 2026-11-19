package handlers

import (
	"log/slog"
	"net/http"

	"github.com/kiff/cloud/web/internal/content/spots"
	"github.com/kiff/cloud/web/internal/pages"
)

// Spot serves the enablement-proof infographics.
//
//	GET /spot          → the index (one card per enablement flow)
//	GET /spot/{slug}    → one infographic
//
// The enablement counterpart to /proof: where /proof shows the boundary
// stopping the unsafe repeat, /spot shows it as the thing that lets you put an
// agent on a money flow at all. Data lives in internal/content/spots;
// rendering is templ + native CSS, no JS. Like the decks and /proof, these are
// pitch artifacts, so they are noindex.
func Spot(logger *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Robots-Tag", "noindex")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")

		slug := r.PathValue("slug")
		if slug == "" {
			_ = pages.SpotIndex(spots.All()).Render(r.Context(), w)
			return
		}

		s, ok := spots.Lookup(slug)
		if !ok {
			http.NotFound(w, r)
			return
		}
		_ = pages.Spot(s).Render(r.Context(), w)
	})
}
