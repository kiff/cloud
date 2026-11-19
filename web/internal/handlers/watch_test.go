package handlers

import (
	"net/http"
	"strings"
	"testing"

	"github.com/kiff/cloud/web/internal/components"
	"github.com/kiff/cloud/web/internal/pages"
)

// /tafs/watch is served in English and at /<lang>/tafs/watch in each other
// language, in that language, with no notice that the English governs (it is
// no legal text), and /support links it in each language.
func TestTafsWatchInEveryLanguage(t *testing.T) {
	// Live, as with KIFF_WATCH_LIVE=on. Not parallel: the switch is the binary's, so it's set and restored here alone.
	pages.HoldWatch(true)
	defer pages.HoldWatch(false)
	// Each language's "A taf needs you", as the watch says it (alert.needs_you).
	needsYou := map[string]string{
		"en": "A taf needs you", "de": "Ein taf braucht dich", "fr": "Un taf vous attend",
		"es": "Un taf te necesita", "it": "Un taf ti aspetta", "pt": "Um taf precisa de si",
		"nl": "Een taf wacht op jou", "pl": "Taf czeka na Ciebie",
	}
	routed := map[string]bool{}
	for _, tr := range TranslatedRoutes() {
		routed[tr.Path] = true
	}
	for _, lang := range components.SiteLanguages {
		path := components.LangPath(lang, "/tafs/watch")
		var h http.Handler = TafsWatch()
		if lang != "en" {
			if !routed[path] {
				t.Errorf("%s isn't routed", path)
			}
			h = Translated(lang, "/tafs/watch", OfferLinks{})
		}
		rec := getPage(t, h, path)
		body := rec.Body.String()
		if rec.Code != http.StatusOK {
			t.Errorf("%s: status %d", path, rec.Code)
			continue
		}
		for _, want := range []string{
			`<html lang="` + lang + `"`,
			`<link rel="canonical" href="` + components.SiteBaseURL + path + `"`,
			`<link rel="alternate" hreflang="x-default" href="` + components.SiteBaseURL + `/tafs/watch"`,
			`<section id="get-it">`,
			needsYou[lang],
			`href="` + components.LangPath(lang, "/tafs/privacy") + `#data"`,
			`href="` + components.LangPath(lang, "/support") + `"`,
		} {
			if !strings.Contains(body, want) {
				t.Errorf("%s: missing %s", path, want)
			}
		}
		if strings.Contains(body, `class="translation-notice"`) {
			t.Errorf("%s carries the legal notice", path)
		}
		support := Support()
		if lang != "en" {
			support = Translated(lang, "/support", OfferLinks{})
		}
		if s := getPage(t, support, components.LangPath(lang, "/support")).Body.String(); !strings.Contains(s, `<a href="`+path+`">`) {
			t.Errorf("%s doesn't link %s", components.LangPath(lang, "/support"), path)
		}
	}
	if !strings.Contains(getPage(t, TafsWatch(), "/tafs/watch").Body.String(), "Coming to Google Play") {
		t.Errorf("/tafs/watch doesn't say the watch app is coming to Google Play")
	}
}

// Until the watch is on Google Play (KIFF_WATCH_LIVE off, the default), /tafs/watch is held back: no language routes it, the
// sitemap and llms.txt leave it out, and /support doesn't link it. The page itself is written and tested, so going live is a
// value.
func TestTafsWatchIsHeldBackUntilLive(t *testing.T) {
	t.Parallel()
	if pages.WatchLive {
		t.Fatal("the watch page is live by default")
	}
	for _, tr := range TranslatedRoutes() {
		if tr.Page == pages.WatchPage {
			t.Errorf("%s is routed while the watch isn't live", tr.Path)
		}
	}
	for _, u := range SitemapURLs() {
		if strings.HasSuffix(u, pages.WatchPage) {
			t.Errorf("the sitemap lists %s", u)
		}
	}
	if strings.Contains(llmsContent(), "kiff.dev/tafs/watch") {
		t.Error("llms.txt links /tafs/watch")
	}
	for _, lang := range components.SiteLanguages {
		support := Support()
		if lang != "en" {
			support = Translated(lang, "/support", OfferLinks{})
		}
		if s := getPage(t, support, components.LangPath(lang, "/support")).Body.String(); strings.Contains(s, "/tafs/watch") {
			t.Errorf("%s links /tafs/watch", components.LangPath(lang, "/support"))
		}
	}
}
