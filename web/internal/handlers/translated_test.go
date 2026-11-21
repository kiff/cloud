package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kiff/cloud/web/internal/components"
	"github.com/kiff/cloud/web/internal/pages"
)

// getPage serves one request through the canonical-path middleware, as the
// binary wraps every route.
func getPage(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	CanonicalPath(h).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

// A translated page is served at its language's path, marked as that
// language, with every language's URL in hreflang and in the row a
// reader changes language with. The browser's language changes nothing.
func TestATranslatedPageIsItsOwnURL(t *testing.T) {
	routes := TranslatedRoutes()
	if len(routes) == 0 {
		t.Skip("no translation is registered")
	}
	for _, tr := range routes {
		rec := getPage(t, Translated(tr.Lang, tr.Page, OfferLinks{}), tr.Path)
		body := rec.Body.String()
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status %d", tr.Path, rec.Code)
		}
		if got := rec.Header().Get("Content-Language"); got != tr.Lang {
			t.Errorf("%s: Content-Language %q", tr.Path, got)
		}
		for _, want := range []string{
			`<html lang="` + tr.Lang + `"`,
			`<link rel="canonical" href="` + components.SiteBaseURL + tr.Path + `"`,
			`<link rel="alternate" hreflang="en" href="` + components.SiteBaseURL + tr.Page + `"`,
			`<link rel="alternate" hreflang="x-default" href="` + components.SiteBaseURL + tr.Page + `"`,
			`<link rel="alternate" hreflang="` + tr.Lang + `" href="` + components.SiteBaseURL + tr.Path + `"`,
			`<nav class="lang-row"`,
		} {
			if !strings.Contains(body, want) {
				t.Errorf("%s: missing %s", tr.Path, want)
			}
		}
		// /tafs draws its own header, footer and language stickers, in its language.
		if tr.Page == "/tafs" {
			for _, want := range []string{
				`aria-current="page"><b>` + strings.ToUpper(tr.Lang) + `</b>` + components.LanguageName(tr.Lang) + `<`,
				`href="/tafs" hreflang="en" lang="en"`,
			} {
				if !strings.Contains(body, want) {
					t.Errorf("%s: missing %s", tr.Path, want)
				}
			}
			if strings.Contains(body, `<p class="translation-notice">`) {
				t.Errorf("%s: the product's page carries the legal notice", tr.Path)
			}
			continue
		}
		for _, want := range []string{
			`aria-current="page">` + components.LanguageName(tr.Lang) + `<`,
			`href="` + tr.Page + `" lang="en" hreflang="en">English<`,
		} {
			if !strings.Contains(body, want) {
				t.Errorf("%s: missing %s", tr.Path, want)
			}
		}
		// The site's own navigation and footer stay English, and say so.
		if !strings.Contains(body, `<header class="site-nav" role="banner" lang="en">`) {
			t.Errorf("%s: the navigation isn't marked as English", tr.Path)
		}
		// Legal text says the English version governs; where to get the app doesn't.
		if notice := strings.Contains(body, `<p class="translation-notice">`); notice == components.HasNoNotice(tr.Page) {
			t.Errorf("%s: translation notice shown = %v", tr.Path, notice)
		}
	}
}

// The English page of a translated page lists its languages too, and an
// English-only page lists none.
func TestTheEnglishPageNamesItsLanguages(t *testing.T) {
	if len(TranslatedRoutes()) == 0 {
		t.Skip("no translation is registered")
	}
	body := getPage(t, Cookies(), "/cookies").Body.String()
	for _, want := range []string{`<html lang="en"`, `<nav class="lang-row"`, `hreflang="x-default" href="https://kiff.dev/cookies"`} {
		if !strings.Contains(body, want) {
			t.Errorf("/cookies: missing %s", want)
		}
	}
	if strings.Contains(body, `class="translation-notice"`) || strings.Contains(body, `<header class="site-nav" role="banner" lang="en">`) {
		t.Errorf("/cookies: the English page is dressed as a translation")
	}
	sla := getPage(t, SLA(), "/legal/sla").Body.String()
	if strings.Contains(sla, "hreflang") || strings.Contains(sla, "lang-row") {
		t.Errorf("/legal/sla has no translations, and lists languages")
	}
}

// A page held back is not served in any other language, and its English
// page stops pointing at them: one switch, for counsel's answer.
func TestAHeldBackPageIsEnglishOnly(t *testing.T) {
	routes := TranslatedRoutes()
	if len(routes) == 0 {
		t.Skip("no translation is registered")
	}
	page := routes[0].Page
	pages.HeldBackTranslations[page] = true
	defer delete(pages.HeldBackTranslations, page)
	for _, tr := range TranslatedRoutes() {
		if tr.Page == page {
			t.Errorf("%s is still routed", tr.Path)
		}
	}
	rec := getPage(t, Translated(routes[0].Lang, page, OfferLinks{}), routes[0].Path)
	if rec.Code != http.StatusNotFound {
		t.Errorf("%s: status %d, want 404", routes[0].Path, rec.Code)
	}
	if alts := components.AlternatesOf(page); alts != nil {
		t.Errorf("%s still lists %d languages", page, len(alts))
	}
}

// The sitemap lists every translated URL, each with its page's languages.
func TestTheSitemapNamesEveryLanguage(t *testing.T) {
	routes := TranslatedRoutes()
	if len(routes) == 0 {
		t.Skip("no translation is registered")
	}
	body := getPage(t, Sitemap(), "/sitemap.xml").Body.String()
	if !strings.Contains(body, `xmlns:xhtml="http://www.w3.org/1999/xhtml"`) {
		t.Fatalf("the sitemap doesn't declare xhtml links")
	}
	for _, tr := range routes {
		if !strings.Contains(body, "<loc>"+components.SiteBaseURL+tr.Path+"</loc>") {
			t.Errorf("the sitemap lacks %s", tr.Path)
		}
		if !strings.Contains(body, `<xhtml:link rel="alternate" hreflang="`+tr.Lang+`" href="`+components.SiteBaseURL+tr.Path+`"></xhtml:link>`) {
			t.Errorf("the sitemap names no %s alternate for %s", tr.Lang, tr.Page)
		}
	}
	if !strings.Contains(body, `<xhtml:link rel="alternate" hreflang="x-default" href="`+components.SiteBaseURL+routes[0].Page+`"></xhtml:link>`) {
		t.Errorf("the sitemap names no default for %s", routes[0].Page)
	}
}

// The app host sends a translated page to kiff.dev, as it does the English one.
func TestATranslatedPathIsPublic(t *testing.T) {
	for path, want := range map[string]bool{"/de/privacy": true, "/pl/legal/dpa": true, "/ko/tafs/terms": true, "/ko/cookies": false, "/ja/privacy": true, "/ja/tafs": false, "/et/tafs/privacy": true, "/et/support": false, "/de/dashboard": false, "/xx/privacy": false, "/de": false, "/el/privacy": true, "/el/tafs/watch": false, "/lt/privacy": true, "/lt/legal/dpa": false, "/cs/privacy": true, "/cs/tafs": false, "/sk/privacy": true, "/sk/tafs/watch": false, "/sl/privacy": true, "/sl/legal/dpa": false, "/hu/privacy": true, "/hu/cookies": false, "/ro/privacy": true, "/ro/tafs": false, "/bg/privacy": true, "/bg/support": false, "/hr/privacy": true, "/hr/tafs/watch": false} {
		if got := isPublicPath(path); got != want {
			t.Errorf("isPublicPath(%q) = %v, want %v", path, got, want)
		}
	}
}
