package handlers

import (
	"net/http"

	"github.com/kiff/cloud/web/internal/pages"
	"github.com/kiff/cloud/web/internal/session"
)

// Landing serves the marketing page at /. Three server-side selectors,
// each an empty-or-unknown-safe fallback to the first option:
//   - ?example= — legacy examples tab (retained for old inbound links).
//   - ?stack=   — the agent-framework picker in the "connect" section.
//   - ?agent=   — the coding-agent picker in the "make it yours" section.
//
// They are plain query params (not JS state) so every tab is a real,
// crawlable, linkable URL — legible to the agents that read the page.
//
// "GET /" is also the mux's catch-all, so any path no other route claims
// lands here. Those answer a real 404 instead of the landing page.
func Landing() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if r.URL.Path != "/" {
			NotFound(w, r)
			return
		}
		q := r.URL.Query()
		_ = pages.Landing(q.Get("example"), q.Get("stack"), q.Get("agent")).Render(r.Context(), w)
	})
}

// AppHome sends a signed-in person who opens the app host's root
// (app.kiff.dev/) to their Cards instead of the marketing page, which
// is what they saw there before (onboarding test, 2026-10-08). A
// signed-out visitor, someone who just signed out (auth.go redirects to
// "/") and the marketing apex still get the landing page. appHost ""
// (local single-host dev) leaves / as the landing page.
func AppHome(appHost string, sessions *session.Manager, landing http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" && appHost != "" && sessions != nil && hostNoPort(r.Host) == appHost {
			if _, err := sessions.Read(r); err == nil {
				http.Redirect(w, r, "/cards", http.StatusFound)
				return
			}
		}
		landing.ServeHTTP(w, r)
	})
}

// NotFound answers 404 with the public not-found page.
func NotFound(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("X-Robots-Tag", "noindex")
	w.WriteHeader(http.StatusNotFound)
	_ = pages.NotFound().Render(r.Context(), w)
}
