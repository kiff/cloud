package pages

import (
	"sort"

	"github.com/a-h/templ"
	"github.com/kiff/cloud/web/internal/components"
)

// A TranslatedPage builds one of the pages that exist in every site
// language (components.TranslatedPages). /tafs takes the offer's links;
// the others ignore them.
type TranslatedPage func(OfferLinks) templ.Component

// englishPages are the English sources of the translated pages, by path.
// Every translation is held to its source by TestTranslationsKeepTheEnglishPage.
var englishPages = map[string]TranslatedPage{
	"/tafs":           func(o OfferLinks) templ.Component { return Tafs(o) },
	"/privacy":        func(OfferLinks) templ.Component { return Privacy() },
	"/terms":          func(OfferLinks) templ.Component { return Terms() },
	"/acceptable-use": func(OfferLinks) templ.Component { return AcceptableUse() },
	"/cookies":        func(OfferLinks) templ.Component { return Cookies() },
	"/security":       func(OfferLinks) templ.Component { return Security() },
	"/subprocessors":  func(OfferLinks) templ.Component { return Subprocessors() },
	"/legal/dpa":      func(OfferLinks) templ.Component { return DPA() },
	"/support":        func(OfferLinks) templ.Component { return Support() },
	"/tafs/download":  func(o OfferLinks) templ.Component { return downloadIn(downloadEN, o) },
	"/tafs/watch":     func(OfferLinks) templ.Component { return TafsWatch() },
	"/tafs/privacy":   func(OfferLinks) templ.Component { return TafsPrivacy() },
	"/tafs/terms":     func(OfferLinks) templ.Component { return TafsTerms() },
}

// translations holds each language's pages, by language then path. A
// language registers its own from an init in its `<lang>_pages.go`, so
// adding a language touches no shared file.
var translations = map[string]map[string]TranslatedPage{}

// registerTranslation adds one page in one language. It panics on a
// language or a path the site doesn't have, or a page registered twice:
// each is a mistake in a language's own file, caught when the binary starts.
func registerTranslation(lang, path string, page TranslatedPage) {
	if lang == "en" || !components.IsSiteLanguage(lang) {
		panic("pages: not a translated site language: " + lang)
	}
	if !components.IsTranslatedPage(path) {
		panic("pages: not a translated page: " + path)
	}
	if translations[lang] == nil {
		translations[lang] = map[string]TranslatedPage{}
	}
	if _, dup := translations[lang][path]; dup {
		panic("pages: registered twice: " + lang + " " + path)
	}
	translations[lang][path] = page
}

// HeldBackTranslations are pages whose translations are written but not
// served yet, by path: their language paths answer as any unknown path
// does, they are left out of hreflang and the sitemap, and the English
// page shows no language row. The legal pages can wait here for counsel
// without holding /tafs. Empty means every translation is live.
var HeldBackTranslations = map[string]bool{}

// Translation is the page at path in lang: English from its source, any
// other language from its registered translation. ok is false for a page
// that has no translation in that language, or that is held back.
func Translation(lang, path string) (TranslatedPage, bool) {
	if lang == "en" {
		p, ok := englishPages[path]
		return p, ok
	}
	if HeldBackTranslations[path] {
		return nil, false
	}
	p, ok := translations[lang][path]
	return p, ok
}

// TranslatedLanguages lists the languages that have at least one
// translation registered, sorted.
func TranslatedLanguages() []string {
	out := make([]string, 0, len(translations))
	for l := range translations {
		out = append(out, l)
	}
	sort.Strings(out)
	return out
}

func init() {
	// The layout asks which languages a page is served in, for hreflang
	// and the language row.
	components.TranslationServed = func(lang, page string) bool {
		_, ok := Translation(lang, page)
		return ok
	}
}
