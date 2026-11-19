package handlers

import (
	"net/http"

	"github.com/kiff/cloud/web/internal/components"
)

// CanonicalPath injects the request path into the context so the shared
// public shell can render a per-page canonical + og:url (instead of the
// hardcoded root every page used to emit). Cheap and cross-cutting: wrap
// the mux once and every public page gets a correct canonical.
func CanonicalPath(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := components.WithCanonicalPath(r.Context(), r.URL.Path)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
