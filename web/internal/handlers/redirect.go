package handlers

import "net/http"

// Redirect returns a handler that permanently (301) redirects to target.
// It keeps retired URLs pointing at the section they were folded into —
// /solutions → /#use-cases, /how-it-works → /integrate#how — instead of
// silently falling through to the catch-all homepage.
func Redirect(target string) http.Handler {
	return http.RedirectHandler(target, http.StatusMovedPermanently)
}
