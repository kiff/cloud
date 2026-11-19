package handlers

import (
	"net/http"
	"strings"
)

// TrailingSlash answers a GET or HEAD for a path ending in "/" with a 301 to
// the same path without its trailing slashes, keeping the query: /docs/ goes
// to /docs, /pricing/?x=1 to /pricing?x=1. It acts only where the mux has
// nothing but its catch-all "GET /", which answers an unknown path with 404,
// so one rule covers every page, current and future. A route that ends in
// "/" (/demo/, /static/, /dashboard/, …) keeps its requests, and so does
// every other method.
func TrailingSlash(mux *http.ServeMux) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if (r.Method != http.MethodGet && r.Method != http.MethodHead) || p == "/" || !strings.HasSuffix(p, "/") {
			mux.ServeHTTP(w, r)
			return
		}
		if _, pattern := mux.Handler(r); pattern != "GET /" && pattern != "/" {
			mux.ServeHTTP(w, r)
			return
		}
		// "//host" or "/\host" would leave the site in a browser: an open
		// redirect. It's checked on the decoded path, so an escaped "%2F%2F"
		// is refused too; such a path stays the mux's (a 404).
		if d := strings.TrimRight(p, "/"); d == "" || strings.HasPrefix(d, "//") || strings.HasPrefix(d, `/\`) {
			mux.ServeHTTP(w, r)
			return
		}
		// The target keeps the path as it was escaped, so "%3F" stays a
		// character of the path, never a query.
		to := strings.TrimRight(r.URL.EscapedPath(), "/")
		if r.URL.RawQuery != "" {
			to += "?" + r.URL.RawQuery
		}
		http.Redirect(w, r, to, http.StatusMovedPermanently)
	})
}
