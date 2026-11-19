package handlers

import "net/http"

// Robots tells crawlers to keep off api.kiff.dev. Its URLs are for
// programs; a crawler that follows one from a public page only gets 401s.
func Robots() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("User-agent: *\nDisallow: /\n"))
	})
}
