package handlers

import (
	"net/http"
	"regexp"
)

// The tafs Chrome extension claims its view-only KIFF device (POST
// /v1/pair/claim) and ends that device's session (POST
// /v1/auth/device/logout) from its service worker, with no host permission for
// api.kiff.dev (none is asked at install), so Chrome sends each as a CORS
// request, a preflight first. The claim carries no cookie and no bearer: it's
// proved by the device key's signature over the pairing's nonce, which the
// phone approved. The logout carries the device session's bearer in its
// header, never a cookie. So a browser is let read those two answers from an
// extension's own origin only, never `*` and never with credentials, and the
// answer to a preflight carries no cookie or credential either. The origin's
// scheme and shape are the CORS gate only, as no Web Store id is fixed yet:
// the claim's proof and the logout's bearer stay the checks.

// extensionOrigin is a Chrome extension's origin: the scheme, and an id of 32
// letters a to p, as Chrome makes them, with nothing after it.
var extensionOrigin = regexp.MustCompile(`^chrome-extension://[a-p]{32}$`)

// allowExtension says a browser may read the answer when the request comes
// from an extension's own origin: its origin echoed, never `*`, and no
// credentials. It reports whether it did.
func allowExtension(w http.ResponseWriter, r *http.Request) bool {
	w.Header().Add("Vary", "Origin")
	origin := r.Header.Get("Origin")
	if !extensionOrigin.MatchString(origin) {
		return false
	}
	w.Header().Set("Access-Control-Allow-Origin", origin)
	return true
}

// extensionCORS lets the extension read h's answers, refusals included.
func extensionCORS(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		allowExtension(w, r)
		h(w, r)
	}
}

// extensionPreflight answers Chrome's preflight for one of the extension's
// routes: POST with a JSON body and the request headers it names, from an
// extension's origin. Anything else gets no CORS headers, so the browser stops
// it. It reads and writes nothing else.
func extensionPreflight(headers string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if allowExtension(w, r) {
			w.Header().Set("Access-Control-Allow-Methods", "POST")
			w.Header().Set("Access-Control-Allow-Headers", headers)
			w.Header().Set("Access-Control-Max-Age", "600")
		}
		w.WriteHeader(http.StatusNoContent)
	}
}
