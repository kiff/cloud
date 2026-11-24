package handlers

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/kiff/cloud/web/internal/components"
	"github.com/kiff/cloud/web/internal/pages"
)

// The Limited Use statement, in Google's own words with tafs's name, and the
// policy it links to: what Google's review of tafs's sign-in reads.
const tafsLimitedUse = "tafs's use and transfer to any other app of information received from Google APIs will adhere to " +
	`<a href="https://developers.google.com/terms/api-services-user-data-policy">Google API Services User Data Policy</a>` +
	", including the Limited Use requirements."

// tafsLegalPage renders one of tafs's own documents, last updated on updated.
func tafsLegalPage(t *testing.T, h http.Handler, path, updated string) string {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("%s: status %d", path, rec.Code)
	}
	body := strings.ReplaceAll(rec.Body.String(), "&#39;", "'")
	for _, s := range []string{`class="legal prose"`, `href="/static/css/legal.css"`, updated, "Kiff Agents OÜ"} {
		if !strings.Contains(body, s) {
			t.Errorf("%s is missing %q", path, s)
		}
	}
	return body
}

// /tafs/privacy: tafs's privacy policy, with the #google section Google's
// review reads, the Limited Use statement word for word, and both links.
func TestTafsPrivacy(t *testing.T) {
	t.Parallel()
	body := tafsLegalPage(t, TafsPrivacy(), "/tafs/privacy", "24 November 2026")
	// Doors: who a door keeps, by each kind of sender id, before the first Slack, Telegram or WhatsApp door.
	for _, s := range []string{"<strong>Doors:</strong>", "Slack member ID", "Telegram user ID", "a phone number for a WhatsApp door"} {
		if !strings.Contains(body, s) {
			t.Errorf("/tafs/privacy is missing %q", s)
		}
	}
	start := strings.Index(body, `<section id="google">`)
	if start < 0 {
		t.Fatal("no #google section")
	}
	sec := body[start : start+strings.Index(body[start:], "</section>")]
	for _, s := range []string{
		"Google user data", tafsLimitedUse, `href="https://myaccount.google.com/permissions"`, `href="/subprocessors"`,
		"Gmail", "drafts", "Card", "Why:", "Who keeps your sign-in:", "never given to a taf or to planque",
		"Who else receives it:", "How long, and where:", "Stopping it:", "Connect apps",
		"Google user data is not used to train AI models.",
		"Images:", "never uses Google APIs or Google user data to create, edit or share intimate images of anyone (non-consensual intimate imagery, NCII)",
		"keeps what Google returns for 24 hours", "keeps only whether the call was sent",
	} {
		if !strings.Contains(sec, s) {
			t.Errorf("the #google section is missing %q", s)
		}
	}
	for _, never := range []string{"being deleted", "being cleared", "Nothing is sent without", "nothing is sent without", "Anthropic", "gmail."} {
		if strings.Contains(sec, never) {
			t.Errorf("the #google section says %q", never)
		}
	}
	// §6 prints the scopes of tafs's own Google sign-in, and only of the
	// Google apps on inhouse_apps.json: Google's review reads them there.
	scopes := map[string][]string{
		"Google Sheets":   {"auth/spreadsheets</code>"},
		"Google Calendar": {"auth/calendar.events.readonly</code>", "auth/calendar.events</code>"},
	}
	inHouse := 0
	for app, want := range scopes {
		listed := slices.Contains(pages.InHouseApps, app)
		for _, s := range want {
			if strings.Contains(sec, s) != listed {
				t.Errorf("the #google section shows %q = %v, but %s in-house = %v", s, !listed, app, listed)
			}
		}
		if listed {
			inHouse += len(want)
		}
	}
	if got := strings.Count(sec, "googleapis.com/auth/"); got != inHouse {
		t.Errorf("the #google section prints %d Google scopes, want %d (the in-house apps')", got, inHouse)
	}
	for _, s := range []string{`id="ai"`, `id="retention"`, `id="rights"`, `href="/privacy"`, `href="/tafs/terms"`, "privacy@kiff.dev"} {
		if !strings.Contains(body, s) {
			t.Errorf("missing %q", s)
		}
	}
}

// /tafs/terms: tafs's terms, which restate KIFF's and defer to them.
func TestTafsTerms(t *testing.T) {
	t.Parallel()
	body := tafsLegalPage(t, TafsTerms(), "/tafs/terms", "22 November 2026")
	for _, s := range []string{
		"KIFF's Terms apply to anything this page doesn't cover.", `href="/terms"`, `href="/acceptable-use"`,
		`id="control"`, `id="fees"`, `id="liability"`, `id="governing-law"`, "€12.50", "€11.49", "Harju County Court",
		`href="/tafs/privacy"`, "legal@kiff.dev",
	} {
		if !strings.Contains(body, s) {
			t.Errorf("missing %q", s)
		}
	}
}

// Both documents are listed as public pages (the sitemap), next to /tafs/download.
func TestTafsLegalPagesAreListed(t *testing.T) {
	t.Parallel()
	urls := SitemapURLs()
	for _, p := range []string{"/tafs/download", "/tafs/privacy", "/tafs/terms"} {
		if !slices.Contains(urls, "https://kiff.dev"+p) {
			t.Errorf("the sitemap doesn't list %s", p)
		}
		if !isPublicPath(p) {
			t.Errorf("%s isn't a public path", p)
		}
	}
}

// Both documents are served in each of the seven other site languages, and
// in Korean and Japanese, which have the legal texts only, at
// /<lang>/tafs/terms and /<lang>/tafs/privacy, as translations that say the
// English version governs and keep the company, its address, the emails
// and the date; the Limited Use statement stays in Google's English. The
// unprefixed paths stay English, and are every page's default.
func TestTafsLegalPagesInEveryLanguage(t *testing.T) {
	t.Parallel()
	routed := map[string]bool{}
	for _, tr := range TranslatedRoutes() {
		routed[tr.Path] = true
	}
	for _, page := range []string{"/tafs/terms", "/tafs/privacy"} {
		for _, lang := range components.PageLanguages {
			if lang == "en" || !components.HasPage(lang, page) {
				continue
			}
			path := "/" + lang + page
			if !routed[path] {
				t.Errorf("%s isn't routed", path)
				continue
			}
			rec := getPage(t, Translated(lang, page, OfferLinks{}), path)
			if rec.Code != http.StatusOK {
				t.Errorf("%s: status %d", path, rec.Code)
				continue
			}
			body := strings.ReplaceAll(rec.Body.String(), "&#39;", "'")
			for _, want := range []string{
				`<html lang="` + components.LangTag(lang) + `"`, `<p class="translation-notice">`, components.TranslationNoticeText(lang),
				`<a href="` + page + `" lang="en" hreflang="en">kiff.dev` + page + `</a>`,
				`<link rel="alternate" hreflang="x-default" href="` + components.SiteBaseURL + page + `"`,
				"Kiff Agents OÜ", "17372313", "Tööstuse tn 75-71, Põhja-Tallinna linnaosa, Tallinn 10416",
				"17", "2026", "legal@kiff.dev", "privacy@kiff.dev",
			} {
				if !strings.Contains(body, want) {
					t.Errorf("%s: missing %s", path, want)
				}
			}
			if page == "/tafs/privacy" {
				for _, want := range []string{`<section id="google">`, `<p lang="en">` + tafsLimitedUse + `</p>`, "EE102924327"} {
					if !strings.Contains(body, want) {
						t.Errorf("%s: missing %s", path, want)
					}
				}
			} else if !strings.Contains(body, "support@kiff.dev") {
				t.Errorf("%s: missing support@kiff.dev", path)
			}
		}
		// English stays the default: the unprefixed path is English, carries
		// no translation notice, and is the x-default of every language.
		h := TafsTerms()
		if page == "/tafs/privacy" {
			h = TafsPrivacy()
		}
		body := getPage(t, h, page).Body.String()
		for _, want := range []string{
			`<html lang="en"`, `<link rel="alternate" hreflang="x-default" href="` + components.SiteBaseURL + page + `"`,
			`<link rel="canonical" href="` + components.SiteBaseURL + page + `"`,
		} {
			if !strings.Contains(body, want) {
				t.Errorf("%s: missing %s", page, want)
			}
		}
		if strings.Contains(body, `class="translation-notice"`) {
			t.Errorf("%s: the English page is dressed as a translation", page)
		}
		if lang, p, ok := components.SplitLangPath(page); lang != "en" || p != page || !ok {
			t.Errorf("SplitLangPath(%q) = %q, %q, %v", page, lang, p, ok)
		}
	}
}
