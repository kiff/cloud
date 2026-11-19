package handlers_test

import (
	"encoding/xml"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kiff/cloud/web/internal/handlers"
)

func TestRobotsIsPlainTextWithRules(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()
	handlers.Robots().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/robots.txt", nil))
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Fatalf("Content-Type = %q, want text/plain (it used to be the landing page)", ct)
	}
	body := rec.Body.String()
	for _, want := range []string{
		"User-agent: *",
		"User-agent: GPTBot",
		"User-agent: ClaudeBot",
		"User-agent: PerplexityBot",
		"Disallow: /dashboard",
		"Disallow: /ops",
		"Disallow: /sign-in",
		"Disallow: /demo",
		"Sitemap: https://kiff.dev/sitemap.xml",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("robots.txt missing %q", want)
		}
	}
	if strings.Contains(body, "<html") {
		t.Error("robots.txt is serving HTML")
	}
}

func TestSitemapListsPublicPagesOnly(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()
	handlers.Sitemap().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/sitemap.xml", nil))
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/xml") {
		t.Fatalf("Content-Type = %q, want application/xml", ct)
	}
	var set struct {
		URLs []struct {
			Loc string `xml:"loc"`
		} `xml:"url"`
	}
	if err := xml.Unmarshal(rec.Body.Bytes(), &set); err != nil {
		t.Fatalf("sitemap is not valid XML: %v", err)
	}
	seen := map[string]bool{}
	for _, u := range set.URLs {
		if !strings.HasPrefix(u.Loc, "https://kiff.dev/") {
			t.Errorf("sitemap URL %q is not on https://kiff.dev", u.Loc)
		}
		for _, banned := range []string{"/dashboard", "/ops", "/sign-in", "/deck", "/spot", "/demo"} {
			if strings.Contains(u.Loc, banned) {
				t.Errorf("sitemap lists a private or noindex page: %q", u.Loc)
			}
		}
		if seen[u.Loc] {
			t.Errorf("sitemap lists %q twice", u.Loc)
		}
		seen[u.Loc] = true
	}
	for _, want := range []string{
		"https://kiff.dev/", "https://kiff.dev/docs/ai-act", "https://kiff.dev/pricing",
		"https://kiff.dev/docs/quickstart", "https://kiff.dev/docs/kiff-cards",
	} {
		if !seen[want] {
			t.Errorf("sitemap missing %q", want)
		}
	}
}

// Every docs URL in the sitemap must render: the list comes from the
// docs registry, so this catches a registry entry with no page.
func TestSitemapDocsURLsResolve(t *testing.T) {
	t.Parallel()
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	mux := http.NewServeMux()
	mux.Handle("GET /docs/{slug}", handlers.Docs(quiet))
	mux.Handle("GET /blog/{slug}", handlers.Blog(quiet))
	for _, u := range handlers.SitemapURLs() {
		path := strings.TrimPrefix(u, "https://kiff.dev")
		if !strings.HasPrefix(path, "/docs/") && !strings.HasPrefix(path, "/blog/") {
			continue
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Errorf("%s answered %d", path, rec.Code)
		}
	}
}

func TestUnknownPathIsA404NotTheLandingPage(t *testing.T) {
	t.Parallel()
	for _, path := range []string{"/no-such-page", "/robots.txt.bak", "/wp-login.php"} {
		rec := httptest.NewRecorder()
		handlers.Landing().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s answered %d, want 404", path, rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "Page not found") {
			t.Errorf("%s did not render the not-found page", path)
		}
	}
	rec := httptest.NewRecorder()
	handlers.Landing().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("/ answered %d, want 200", rec.Code)
	}
}

// Every public page advertises a share image and its own description.
func TestPublicPagesCarryShareMetadata(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()
	handlers.Security().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/security", nil))
	body := rec.Body.String()
	for _, want := range []string{
		`<meta property="og:image" content="https://kiff.dev/static/img/og/kiff-og.png"`,
		`<meta name="twitter:image"`,
		`<meta property="og:site_name" content="KIFF"`,
		`<a class="skip-link" href="#main">`,
		`<main id="main"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("/security missing %q", want)
		}
	}
	if strings.Contains(body, `<meta name="description" content="KIFF gives each AI agent a Card: which actions it may take, how much, and what happens outside it. KIFF checks`) {
		t.Error("/security still uses the site-wide default description")
	}
}

// Blog posts carry their date as lastmod; other pages carry none.
func TestSitemapDatesBlogPosts(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()
	handlers.Sitemap().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/sitemap.xml", nil))
	var set struct {
		URLs []struct {
			Loc     string `xml:"loc"`
			LastMod string `xml:"lastmod"`
		} `xml:"url"`
	}
	if err := xml.Unmarshal(rec.Body.Bytes(), &set); err != nil {
		t.Fatalf("sitemap is not XML: %v", err)
	}
	posts := 0
	for _, u := range set.URLs {
		isPost := strings.HasPrefix(u.Loc, "https://kiff.dev/blog/")
		switch {
		case isPost && len(u.LastMod) != len("2006-01-02"):
			t.Errorf("%s: lastmod %q, want a date", u.Loc, u.LastMod)
		case !isPost && u.LastMod != "":
			t.Errorf("%s: lastmod %q on a page that isn't a post", u.Loc, u.LastMod)
		}
		if isPost {
			posts++
		}
	}
	if posts == 0 {
		t.Fatal("no blog posts in the sitemap")
	}
}
