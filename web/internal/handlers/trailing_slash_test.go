package handlers

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// A page asked for with a trailing slash goes to its path without it,
// keeping the query; "/", the routes that end in "/" and other methods are
// unchanged, and nothing redirects off the site.
func TestTrailingSlash(t *testing.T) {
	t.Parallel()

	ok := func(name string) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(name)) })
	}
	mux := http.NewServeMux()
	mux.Handle("GET /", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte("home"))
	}))
	mux.Handle("GET /docs", ok("docs"))
	mux.Handle("GET /docs/{slug}", ok("doc"))
	mux.Handle("GET /pricing", ok("pricing"))
	mux.Handle("GET /demo/", ok("demo"))
	mux.Handle("POST /demo/", ok("demo post"))
	mux.Handle("GET /static/", ok("static"))
	h := TrailingSlash(mux)

	for _, tc := range []struct {
		method, target string
		code           int
		location, body string
	}{
		{"GET", "/docs/", http.StatusMovedPermanently, "/docs", ""},
		{"GET", "/docs/what-kiff-is/", http.StatusMovedPermanently, "/docs/what-kiff-is", ""},
		{"GET", "/pricing/?x=1", http.StatusMovedPermanently, "/pricing?x=1", ""},
		{"HEAD", "/pricing/", http.StatusMovedPermanently, "/pricing", ""},
		{"GET", "/pricing//", http.StatusMovedPermanently, "/pricing", ""},
		// Unchanged.
		{"GET", "/", http.StatusOK, "", "home"},
		{"GET", "/docs", http.StatusOK, "", "docs"},
		{"GET", "/static/css/home.css", http.StatusOK, "", "static"},
		{"GET", "/demo/", http.StatusOK, "", "demo"},
		{"GET", "/demo/step/", http.StatusOK, "", "demo"},
		{"POST", "/demo/step/", http.StatusOK, "", "demo post"},
		{"POST", "/docs/", http.StatusMethodNotAllowed, "", ""},
		// The path as it was escaped: "%3F" stays in the path, and so does "é".
		{"GET", "/docs/a%3Fx=1/", http.StatusMovedPermanently, "/docs/a%3Fx=1", ""},
		{"GET", "/docs/caf%C3%A9/", http.StatusMovedPermanently, "/docs/caf%C3%A9", ""},
		// Never off the site, escaped or not.
		{"GET", `/\evil.example/`, http.StatusNotFound, "", ""},
		{"GET", "/%2F%2Fevil.example/", http.StatusNotFound, "", ""},
	} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(tc.method, tc.target, nil)
		h.ServeHTTP(rec, req)
		if rec.Code != tc.code {
			t.Errorf("%s %s: %d, want %d", tc.method, tc.target, rec.Code, tc.code)
			continue
		}
		if got := rec.Header().Get("Location"); got != tc.location {
			t.Errorf("%s %s: Location %q, want %q", tc.method, tc.target, got, tc.location)
		}
		if tc.body != "" && rec.Body.String() != tc.body {
			t.Errorf("%s %s: body %q, want %q", tc.method, tc.target, rec.Body.String(), tc.body)
		}
	}
}
