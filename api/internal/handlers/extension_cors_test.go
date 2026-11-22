package handlers_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The tafs Chrome extension claims its view-only device from its own origin
// with no host permission for KIFF, so Chrome asks first (22 Nov, the owner's
// Chrome: the preflight got 401, and the claim never ran). Only an
// extension's origin is let read the claim's answers: never `*`, never with
// credentials, and nothing else is opened.
func TestOnlyAnExtensionsOriginReadsTheClaim(t *testing.T) {
	const ext = "chrome-extension://abcdefghijklmnopabcdefghijklmnop"
	r := newPairRig(t)
	call := func(method, path, origin, body string) *httptest.ResponseRecorder {
		q := httptest.NewRequest(method, path, strings.NewReader(body))
		if origin != "" {
			q.Header.Set("Origin", origin)
		}
		if method == http.MethodOptions {
			q.Header.Set("Access-Control-Request-Method", "POST")
			q.Header.Set("Access-Control-Request-Headers", "content-type")
		}
		w := httptest.NewRecorder()
		r.pub.ServeHTTP(w, q)
		return w
	}
	plain := func(name string, w *httptest.ResponseRecorder) {
		t.Helper()
		if v := w.Header().Get("Access-Control-Allow-Credentials"); v != "" {
			t.Errorf("%s: Allow-Credentials %q", name, v)
		}
		if v := w.Header().Get("Set-Cookie"); v != "" {
			t.Errorf("%s: a cookie %q", name, v)
		}
		if !strings.Contains(strings.Join(w.Header().Values("Vary"), ","), "Origin") {
			t.Errorf("%s: no Vary: Origin", name)
		}
	}

	// The preflight: 204, no body, the origin echoed, POST with a JSON body only.
	pre := call(http.MethodOptions, "/v1/pair/claim", ext, "")
	if pre.Code != http.StatusNoContent || pre.Body.Len() != 0 || pre.Header().Get("Access-Control-Allow-Origin") != ext ||
		pre.Header().Get("Access-Control-Allow-Methods") != "POST" || pre.Header().Get("Access-Control-Allow-Headers") != "content-type" ||
		pre.Header().Get("Access-Control-Max-Age") == "" {
		t.Fatalf("the preflight: %d %q %v", pre.Code, pre.Body.String(), pre.Header())
	}
	plain("the preflight", pre)

	// The claim itself: its refusal is readable by the extension, and refused exactly as without the origin.
	bad := `{"nonce":"x","pub":"y","proof":"z"}`
	with, without := call(http.MethodPost, "/v1/pair/claim", ext, bad), call(http.MethodPost, "/v1/pair/claim", "", bad)
	if with.Header().Get("Access-Control-Allow-Origin") != ext || with.Code != without.Code || with.Code < 400 ||
		with.Body.String() != without.Body.String() {
		t.Fatalf("a bad claim: %d %s with the origin, %d %s without", with.Code, with.Body.String(), without.Code, without.Body.String())
	}
	plain("the claim", with)

	// Any other origin gets no CORS answer: a web page, another scheme, an id not Chrome's shape, a look-alike, null.
	for _, origin := range []string{"https://evil.example", "moz-extension://abcdefghijklmnopabcdefghijklmnop",
		"chrome-extension://abcdefghijklmnopabcdefghijklmnoz", "chrome-extension://abcdefghijklmnopabcdefghijklmnop.evil.example",
		"chrome-extension://abcdefghijklmnopabcdefghijklmnop/x", "chrome-extension://short", "null", ""} {
		for _, w := range []*httptest.ResponseRecorder{call(http.MethodOptions, "/v1/pair/claim", origin, ""), call(http.MethodPost, "/v1/pair/claim", origin, bad)} {
			if v := w.Header().Get("Access-Control-Allow-Origin"); v != "" {
				t.Errorf("origin %q: Allow-Origin %q", origin, v)
			}
			if v := w.Header().Get("Access-Control-Allow-Methods"); v != "" {
				t.Errorf("origin %q: Allow-Methods %q", origin, v)
			}
		}
	}

	// Only the claim: another public route answers no preflight, and its answer carries no CORS header.
	for _, path := range []string{"/v1/auth/device/start", "/v1/auth/device/token"} {
		for _, w := range []*httptest.ResponseRecorder{call(http.MethodOptions, path, ext, ""), call(http.MethodPost, path, ext, `{}`)} {
			if v := w.Header().Get("Access-Control-Allow-Origin"); v != "" {
				t.Errorf("%s: Allow-Origin %q", path, v)
			}
		}
	}
}

// The extension's sign-out ends its device session with its bearer from the
// same origin, so Chrome asks first, with no bearer: the preflight is answered
// outside the auth middleware, and the logout itself stays behind it, its
// answer readable by the extension.
func TestOnlyAnExtensionsOriginReadsTheLogout(t *testing.T) {
	const ext = "chrome-extension://abcdefghijklmnopabcdefghijklmnop"
	r := newPairRig(t)
	pre := httptest.NewRequest(http.MethodOptions, "/v1/auth/device/logout", nil)
	pre.Header.Set("Origin", ext)
	pre.Header.Set("Access-Control-Request-Method", "POST")
	pre.Header.Set("Access-Control-Request-Headers", "authorization,content-type")
	w := httptest.NewRecorder()
	r.pub.ServeHTTP(w, pre)
	if w.Code != http.StatusNoContent || w.Header().Get("Access-Control-Allow-Origin") != ext ||
		w.Header().Get("Access-Control-Allow-Methods") != "POST" || w.Header().Get("Access-Control-Allow-Headers") != "authorization, content-type" ||
		w.Header().Get("Access-Control-Allow-Credentials") != "" || !strings.Contains(strings.Join(w.Header().Values("Vary"), ","), "Origin") {
		t.Fatalf("the preflight: %d %v", w.Code, w.Header())
	}

	logout := func(origin string) *httptest.ResponseRecorder {
		q := httptest.NewRequest(http.MethodPost, "/v1/auth/device/logout", strings.NewReader("{}"))
		q.Header.Set("Origin", origin)
		w := httptest.NewRecorder()
		withFakeIdentity(r.authed, r.phone).ServeHTTP(w, q)
		return w
	}
	if w := logout(ext); w.Code != http.StatusNoContent || w.Header().Get("Access-Control-Allow-Origin") != ext ||
		w.Header().Get("Access-Control-Allow-Credentials") != "" {
		t.Fatalf("the logout from the extension: %d %v", w.Code, w.Header())
	}
	for _, origin := range []string{"https://evil.example", "chrome-extension://abcdefghijklmnopabcdefghijklmnop.evil.example", "null"} {
		if w := logout(origin); w.Header().Get("Access-Control-Allow-Origin") != "" {
			t.Errorf("origin %q: Allow-Origin %q", origin, w.Header().Get("Access-Control-Allow-Origin"))
		}
		q := httptest.NewRequest(http.MethodOptions, "/v1/auth/device/logout", nil)
		q.Header.Set("Origin", origin)
		w := httptest.NewRecorder()
		r.pub.ServeHTTP(w, q)
		if w.Header().Get("Access-Control-Allow-Origin") != "" || w.Header().Get("Access-Control-Allow-Methods") != "" {
			t.Errorf("origin %q: preflight %v", origin, w.Header())
		}
	}
}
