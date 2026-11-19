package assets

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFSContainsStylesheets(t *testing.T) {
	t.Parallel()

	// Root-level shared foundation, loaded on every page.
	for _, name := range []string{"open-props.min.css", "app.css"} {
		if _, err := fs.Stat(FS(), name); err != nil {
			t.Errorf("missing %s: %v", name, err)
		}
	}

	// Per-surface stylesheets under css/, loaded per route via the
	// layout's extraCSS mechanism (the app.css monolith split).
	for _, name := range []string{
		"css/dashboard.css",
		"css/studio.css",
		"css/landing.css",
		"css/pricing.css",
		"css/prose.css",
		"css/whitepaper.css",
		"css/docs.css",
		"css/security.css",
		"css/legal.css",
		"css/receipt.css",
	} {
		if _, err := fs.Stat(FS(), name); err != nil {
			t.Errorf("missing %s: %v", name, err)
		}
	}
}

// TestFSContainsScripts asserts the first-party JS islands are
// embedded. docs.js is the copy-to-clipboard helper the docs page
// loads via /static/js/docs.js; if it falls out of the embed it
// would 404 silently in the browser.
func TestFSContainsScripts(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"js/docs.js"} {
		if _, err := fs.Stat(FS(), name); err != nil {
			t.Errorf("missing %s: %v", name, err)
		}
	}
}

func TestURLVersionsByContent(t *testing.T) {
	t.Parallel()
	u := URL("css/cards.css")
	if !strings.HasPrefix(u, "/static/css/cards.css?v=") || len(u) <= len("/static/css/cards.css?v=") {
		t.Fatalf("URL(css/cards.css) = %q, want a content version", u)
	}
	if got := URL("no/such.css"); got != "/static/no/such.css" {
		t.Fatalf("unknown file = %q, want the plain path", got)
	}
}

func TestHandlerCachesAndRevalidates(t *testing.T) {
	t.Parallel()
	h := Handler()
	get := func(target, inm string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, target, nil)
		if inm != "" {
			req.Header.Set("If-None-Match", inm)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	versioned := get(URL("css/cards.css"), "")
	if versioned.Code != http.StatusOK {
		t.Fatalf("versioned status = %d", versioned.Code)
	}
	if cc := versioned.Header().Get("Cache-Control"); !strings.Contains(cc, "immutable") {
		t.Fatalf("versioned Cache-Control = %q, want immutable", cc)
	}

	plain := get("/static/css/cards.css", "")
	if cc := plain.Header().Get("Cache-Control"); cc != "public, max-age=300" {
		t.Fatalf("unversioned Cache-Control = %q", cc)
	}
	etag := plain.Header().Get("ETag")
	if etag == "" {
		t.Fatal("no ETag on an embedded file")
	}
	if rec := get("/static/css/cards.css", etag); rec.Code != http.StatusNotModified {
		t.Fatalf("revalidation with the current ETag = %d, want 304", rec.Code)
	}

	if rec := get("/static/no/such.css", ""); rec.Code != http.StatusNotFound || rec.Header().Get("Cache-Control") != "" {
		t.Fatalf("missing file: status %d, Cache-Control %q", rec.Code, rec.Header().Get("Cache-Control"))
	}
}
