package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWhitepaper(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodGet, "/whitepaper", nil)
	rec := httptest.NewRecorder()

	Whitepaper().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want %d", rec.Code, http.StatusOK)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("content-type: got %q, want text/html", ct)
	}

	body := rec.Body.String()
	wants := []string{
		// Layout is shared with the landing page; topbar + footer must
		// still render so the whitepaper feels like part of the same site.
		`class="site-nav"`,
		`class="site-footer"`,
		`href="/static/app.css"`,
		`href="/static/css/prose.css"`,
		`href="/static/css/whitepaper.css"`,
		// Whitepaper-specific shell
		`class="whitepaper"`,
		"Whitepaper · CC BY 4.0",
		`href="https://github.com/kiff/kiff/blob/main/docs/whitepaper.md"`,
		// Rendered markdown content
		"<h1",
		"<h2",
		"KIFF",
	}
	for _, w := range wants {
		if !strings.Contains(body, w) {
			t.Errorf("body missing %q", w)
		}
	}
}
