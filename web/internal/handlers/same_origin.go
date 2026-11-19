package handlers

import (
	"net/http"
	"net/url"
)

// SameOriginOnly refuses state-changing requests a browser sent from
// another origin, so a page elsewhere cannot use the owner's session
// cookie to answer a held action, connect or remove a tool, mint a
// key, or change anything else.
//
// The session cookie is SameSite=Lax, which stops cross-site POSTs but
// not same-site ones: with the cookie on .kiff.dev, every *.kiff.dev
// subdomain (canyouforge, prove, …) is the same site. So the check is
// on the origin, not the site:
//
//   - Sec-Fetch-Site, sent by current browsers, must be "same-origin"
//     (or "none": the user typed it or used a bookmark);
//   - without it, an Origin header must name this request's host;
//   - with neither, the request did not come from a browser page, and
//     passes: a cross-origin form post always carries one of them.
//
// GET, HEAD and OPTIONS pass; they must not change state. This is the
// check net/http's CrossOriginProtection makes from Go 1.25, without
// its bypass list.
func SameOriginOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !crossOrigin(r) {
			next.ServeHTTP(w, r)
			return
		}
		http.Error(w, "Cross-origin request refused. Open this page on its own site and try again.", http.StatusForbidden)
	})
}

func crossOrigin(r *http.Request) bool {
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return false
	}
	switch r.Header.Get("Sec-Fetch-Site") {
	case "same-origin", "none":
		return false
	case "":
	default:
		return true
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return false
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		// "null" (an opaque origin) and anything unparseable.
		return true
	}
	return u.Host != r.Host
}
