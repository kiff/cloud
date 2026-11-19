package handlers

import (
	"net/http"
	"strings"

	"github.com/kiff/cloud/web/internal/components"
)

// CanonicalAppHost forces the authenticated surface onto a single
// canonical host so the Clerk sign-in handshake always runs on the
// origin Clerk is configured for.
//
// Why this exists. The web binary answers for both the marketing apex
// (kiff.dev) and the dashboard host (app.kiff.dev) — same process, one
// mux, no host routing. The Clerk sign-in flow derives its
// redirect_url from the request Host (clerk_provider.absoluteCallback),
// so a sign-in *started* on the apex keeps the whole Account-Portal ↔
// /sign-in/callback handshake on the apex. clerk.browser.js can only
// hydrate a session on the app's registered origin (app.kiff.dev), so
// on the apex the handshake never completes and the user ping-pongs
// between the Account Portal and the callback forever. The session
// cookie (.kiff.dev) and the callback loop-breaker addressed adjacent
// problems; neither moves the handshake to the host where it works.
//
// The public site works the other way round: its pages requested on the
// app host redirect permanently to kiff.dev (isPublicPath).
//
// Fix: when a request for a gated path (sign-in, sign-out, dashboard,
// ops) arrives on any host other than appHost, redirect it to appHost
// with the path and query preserved. The redirect happens before any
// Clerk redirect is minted, so the entire handshake then runs on the
// canonical origin and the loop cannot form. Marketing paths are never
// touched — they keep serving on whichever host they were requested.
//
// appHost is the canonical dashboard host (e.g. "app.kiff.dev"). When
// empty (the default, and the correct setting for local single-host
// dev) the middleware is a no-op pass-through.
func CanonicalAppHost(appHost string) func(http.Handler) http.Handler {
	appHost = strings.TrimSpace(appHost)
	return func(next http.Handler) http.Handler {
		if appHost == "" {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if hostNoPort(r.Host) == appHost && isPublicPath(r.URL.Path) {
				http.Redirect(w, r, components.SiteBaseURL+r.URL.RequestURI(), http.StatusMovedPermanently)
				return
			}
			if hostNoPort(r.Host) != appHost && isGatedPath(r.URL.Path) {
				target := "https://" + appHost + r.URL.RequestURI()
				http.Redirect(w, r, target, http.StatusFound)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// publicRoots are the public site's pages and sections. On the app host
// they answer with a permanent redirect to the same path on kiff.dev:
// serving them there made a second copy of the site that search engines
// crawled and indexed. The app's own routes, /static, robots.txt and the
// app host's home are never in this list.
var publicRoots = []string{
	"/agent", "/why", "/delegate", "/integrate", "/builders", "/on-rails",
	"/pricing", "/proof", "/invariants", "/whitepaper", "/security", "/docs",
	"/blog", "/privacy", "/terms", "/acceptable-use", "/legal", "/subprocessors",
	"/cookies", "/support", "/ai-act", "/tafs", "/download", "/deck", "/spot", "/demo",
	"/llms.txt", "/llms-full.txt", "/sitemap.xml",
}

// isPublicPath reports whether path is a public page or under one. A
// translated page ("/de/privacy") is as public as its English source.
func isPublicPath(path string) bool {
	if lang, page, ok := components.SplitLangPath(path); ok && lang != "en" {
		path = page
	}
	for _, p := range publicRoots {
		if path == p || strings.HasPrefix(path, p+"/") {
			return true
		}
	}
	return false
}

// gatedPrefixes are the authenticated-surface roots that must live on
// the canonical app host. /sign-in covers /sign-in/callback too.
var gatedPrefixes = []string{"/sign-in", "/sign-out", "/dashboard", "/ops", "/invite"}

// isGatedPath reports whether path is one of the authenticated-surface
// roots or a child of one. Matches an exact prefix or a prefix followed
// by "/" so "/sign-in" and "/sign-in/callback" match but a hypothetical
// "/sign-inflate" does not.
func isGatedPath(path string) bool {
	for _, p := range gatedPrefixes {
		if path == p || strings.HasPrefix(path, p+"/") {
			return true
		}
	}
	return false
}

// hostNoPort strips a trailing ":port" from a Host header so the
// comparison against appHost is port-insensitive.
func hostNoPort(host string) string {
	if i := strings.LastIndexByte(host, ':'); i >= 0 {
		return host[:i]
	}
	return host
}
