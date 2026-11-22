package components

import (
	"context"
	"strings"

	"github.com/a-h/templ"
)

// SiteLanguages are the languages the pages a tafs user reaches are
// written in: the eight tafs speaks (kiff/tafs's languages doc).
// English is the source and is served unprefixed; every other language
// has its own path ("/de/privacy"), so a URL is one text, a search engine
// indexes each language on its own, and a shared link keeps its language.
var SiteLanguages = []string{"en", "de", "fr", "es", "it", "pt", "nl", "pl"}

// partialPages are the languages that have some of the translated pages,
// not all, and the pages each has. A link from one of them to any other
// page goes to its English version, marked as English (InEnglish). They
// are not site languages: the screens the apps open (RequestLanguage)
// speak SiteLanguages only, so a browser in one of these languages still
// gets them in English. Korean and Japanese have KIFF's and tafs's privacy
// policies and terms.
var partialPages = map[string][]string{
	"ko": {"/privacy", "/terms", "/tafs/privacy", "/tafs/terms"},
	"ja": {"/privacy", "/terms", "/tafs/privacy", "/tafs/terms"},
	"et": {"/privacy", "/terms", "/tafs/privacy", "/tafs/terms"},
	"lv": {"/privacy", "/terms", "/tafs/privacy", "/tafs/terms"},
	"el": {"/privacy", "/terms", "/tafs/privacy", "/tafs/terms"},
	"lt": {"/privacy", "/terms", "/tafs/privacy", "/tafs/terms"}, // Lithuanian: the same four
	"cs": {"/privacy", "/terms", "/tafs/privacy", "/tafs/terms"}, // Czech: the same four
	"sk": {"/privacy", "/terms", "/tafs/privacy", "/tafs/terms"},
	"sl": {"/privacy", "/terms", "/tafs/privacy", "/tafs/terms"}, // Slovene: the same four
	"hu": {"/privacy", "/terms", "/tafs/privacy", "/tafs/terms"}, // Hungarian: the same four
	"ro": {"/privacy", "/terms", "/tafs/privacy", "/tafs/terms"}, // Romanian: the same four
	"bg": {"/privacy", "/terms", "/tafs/privacy", "/tafs/terms"}, // Bulgarian: the same four
	"hr": {"/privacy", "/terms", "/tafs/privacy", "/tafs/terms"}, // Croatian: the same four
	"tr": {"/privacy", "/terms", "/tafs/privacy", "/tafs/terms"},
	"id": {"/privacy", "/terms", "/tafs/privacy", "/tafs/terms"},
	"vi": {"/privacy", "/terms", "/tafs/privacy", "/tafs/terms"},
	"th": {"/privacy", "/terms", "/tafs/privacy", "/tafs/terms"},
	"zh": {"/privacy", "/terms", "/tafs/privacy", "/tafs/terms"},
	"ar": {"/privacy", "/terms", "/tafs/privacy", "/tafs/terms"},
	"sr": {"/privacy", "/terms", "/tafs/privacy", "/tafs/terms"},
	"sq": {"/privacy", "/terms", "/tafs/privacy", "/tafs/terms"},
	"ka": {"/privacy", "/terms", "/tafs/privacy", "/tafs/terms"},
	"hy": {"/privacy", "/terms", "/tafs/privacy", "/tafs/terms"},
	"az": {"/privacy", "/terms", "/tafs/privacy", "/tafs/terms"},
	"bn": {"/privacy", "/terms", "/tafs/privacy", "/tafs/terms"},
}

// LangTag is the BCP 47 tag a language's pages are marked with: its path
// code, except Traditional Chinese, whose path is /zh and whose tag says
// which script it is ("zh-Hant"), and Serbian, written in Latin script ("sr-Latn"), for <html lang>, hreflang and
// Content-Language.
func LangTag(lang string) string {
	switch lang {
	case "zh":
		return "zh-Hant"
	case "sr":
		return "sr-Latn"
	}
	return lang
}

// IsRightToLeft reports a language written right to left (Arabic).
func IsRightToLeft(lang string) bool { return lang == "ar" }

// HTMLAttrs are the extra attributes the public shell puts on <html>: dir="rtl"
// on a right-to-left page, none on any other, so no other page's markup changes.
func HTMLAttrs(ctx context.Context) templ.Attributes {
	if IsRightToLeft(HTMLLanguage(ctx)) {
		return templ.Attributes{"dir": "rtl"}
	}
	return nil
}

// PageLanguages are every language a translated page can be in: the site
// languages, then the languages that have some of the pages.
var PageLanguages = append(append([]string{}, SiteLanguages...), "ko", "ja", "et", "lv", "lt", "el", "cs", "sk", "sl", "hu", "ro", "bg", "hr", "tr", "id", "vi", "th", "zh", "ar", "sr", "sq", "ka", "hy", "az", "bn")

// TranslatedPages are the public pages that exist in every site
// language: /tafs, where to get it (/tafs/download), tafs on a watch
// (/tafs/watch), how to reach a person (/support), tafs's own privacy policy
// and terms, and the pages the apps
// and those pages send a tafs user to. Everything else on the site
// (developer docs, the blog, pricing, the SLA, the whitepaper, the
// dashboard) is English only.
var TranslatedPages = []string{
	"/tafs", "/privacy", "/terms", "/acceptable-use", "/cookies",
	"/security", "/subprocessors", "/legal/dpa", "/support", "/tafs/download",
	"/tafs/watch", "/tafs/privacy", "/tafs/terms",
}

// languageNames is each language's name in itself, for the language row.
var languageNames = map[string]string{
	"en": "English", "de": "Deutsch", "fr": "Français", "es": "Español",
	"it": "Italiano", "pt": "Português", "nl": "Nederlands", "pl": "Polski",
	"ko": "한국어",
	"ja": "日本語",
	"et": "Eesti",
	"lv": "Latviešu",
	"el": "Ελληνικά",
	"lt": "Lietuvių",
	"cs": "Čeština",
	"sk": "Slovenčina",
	"sl": "Slovenščina",
	"hu": "Magyar",
	"ro": "Română",
	"bg": "Български",
	"hr": "Hrvatski",
	"tr": "Türkçe",
	"id": "Bahasa Indonesia",
	"vi": "Tiếng Việt",
	"th": "ไทย",
	"zh": "繁體中文",
	"ar": "العربية",
	"sr": "Srpski",
	"sq": "Shqip",
	"ka": "ქართული",
	"hy": "Հայերեն",
	"az": "Azərbaycanca",
	"bn": "বাংলা",
}

// LanguageName is a site language's name in that language.
func LanguageName(lang string) string { return languageNames[lang] }

// IsSiteLanguage reports whether lang is one of the site languages.
func IsSiteLanguage(lang string) bool {
	for _, l := range SiteLanguages {
		if l == lang {
			return true
		}
	}
	return false
}

// IsPageLanguage reports whether lang is one of PageLanguages.
func IsPageLanguage(lang string) bool {
	for _, l := range PageLanguages {
		if l == lang {
			return true
		}
	}
	return false
}

// HasPage reports whether page (unprefixed, no anchor) is a translated
// page that lang has: every one for English and the site languages, the
// listed ones for a language that has some (partialPages).
func HasPage(lang, page string) bool {
	if !IsTranslatedPage(page) {
		return false
	}
	if lang == "en" || IsSiteLanguage(lang) {
		return true
	}
	for _, p := range partialPages[lang] {
		if p == page {
			return true
		}
	}
	return false
}

// IsTranslatedPage reports whether path (unprefixed, no anchor) is a page
// that exists in every site language.
func IsTranslatedPage(path string) bool {
	for _, p := range TranslatedPages {
		if p == path {
			return true
		}
	}
	return false
}

// LangPath is a translated page's path in a language: English as it is,
// any other language under its code. A path that isn't a translated page
// comes back unchanged, so a link to an English-only page stays English,
// and so does a page the language doesn't have. An anchor or query is kept.
func LangPath(lang, path string) string {
	page := path
	if i := strings.IndexAny(page, "#?"); i >= 0 {
		page = page[:i]
	}
	if lang == "en" || !HasPage(lang, page) {
		return path
	}
	return "/" + lang + path
}

// SplitLangPath reads a request path as a language and a translated
// page: "/de/privacy" is ("de", "/privacy", true) and "/privacy" is
// ("en", "/privacy", true). Any other path is ("en", path, false).
func SplitLangPath(path string) (lang, page string, ok bool) {
	if i := strings.IndexAny(path, "#?"); i >= 0 {
		path = path[:i]
	}
	if len(path) > 1 {
		path = strings.TrimRight(path, "/")
	}
	if IsTranslatedPage(path) {
		return "en", path, true
	}
	if len(path) > 4 && path[0] == '/' && path[3] == '/' {
		if l := path[1:3]; l != "en" && HasPage(l, path[3:]) {
			return l, path[3:], true
		}
	}
	return "en", path, false
}

// PageLanguage is the language of the page being rendered, read from its
// path: "en" for every page that isn't a translation.
func PageLanguage(ctx context.Context) string {
	p, _ := ctx.Value(canonicalPathKey).(string)
	lang, _, _ := SplitLangPath(p)
	return lang
}

// Alternate is one language version of a page, for hreflang and the
// language row.
type Alternate struct {
	Lang string // "de", or "x-default" for the version to fall back to
	Name string // the language's own name
	Path string // "/de/privacy"
	URL  string // absolute, for hreflang
}

// PageAlternates lists every language version of the page being
// rendered, English first, or nil for a page that has no translations.
func PageAlternates(ctx context.Context) []Alternate {
	p, _ := ctx.Value(canonicalPathKey).(string)
	_, page, ok := SplitLangPath(p)
	if !ok {
		return nil
	}
	return AlternatesOf(page)
}

// HasNoNotice reports a translated page that is no legal text, so it
// carries no notice that the English version governs: the product's page,
// where to get it, and tafs on a watch.
func HasNoNotice(page string) bool {
	return page == "/tafs" || page == "/tafs/download" || page == "/tafs/watch"
}

// TranslationServed reports whether a translated page is served in a
// language. The pages package sets it from its registry; until then every
// site language counts, which is what a test of this package alone wants.
var TranslationServed = func(lang, page string) bool { return true }

// AlternatesOf lists the language versions of a translated page that are
// served, English first. A page that is served in English only has none:
// there is nothing to choose between, so no row and no hreflang.
func AlternatesOf(page string) []Alternate {
	out := make([]Alternate, 0, len(PageLanguages))
	for _, l := range PageLanguages {
		if l != "en" && (!HasPage(l, page) || !TranslationServed(l, page)) {
			continue
		}
		path := LangPath(l, page)
		out = append(out, Alternate{Lang: l, Name: LanguageName(l), Path: path, URL: SiteBaseURL + path})
	}
	if len(out) < 2 {
		return nil
	}
	return out
}

// translationNotices is what a translated legal page says first, in its
// own language: it is a translation, and the English version governs.
// The link to the English page follows the sentence.
var translationNotices = map[string]string{
	"de": "Dies ist eine Übersetzung zur Orientierung. Maßgeblich ist die englische Fassung:",
	"fr": "Ceci est une traduction fournie pour information. La version anglaise fait foi :",
	"es": "Esta es una traducción a título informativo. Prevalece la versión en inglés:",
	"it": "Questa è una traduzione a scopo informativo. Fa fede la versione inglese:",
	"pt": "Esta é uma tradução a título informativo. Prevalece a versão em inglês:",
	"nl": "Dit is een vertaling ter informatie. De Engelse versie is leidend:",
	"pl": "To tłumaczenie ma charakter informacyjny. Wiążąca jest wersja angielska:",
	"ko": "이 번역본은 참고용입니다. 영어 원문이 우선합니다:",
	"ja": "これは参考のための翻訳です。英語版が優先します：",
	"et": "See on teavitamiseks mõeldud tõlge. Kehtib ingliskeelne versioon:",
	"lv": "Šis ir tulkojums uzziņai. Noteicošā ir versija angļu valodā:",
	"el": "Αυτή είναι μετάφραση για ενημερωτικούς σκοπούς. Υπερισχύει η αγγλική έκδοση:",
	"lt": "Tai vertimas, pateikiamas informacijai. Viršenybę turi versija anglų kalba:",
	"cs": "Toto je překlad pro informaci. Rozhodující je anglická verze:",
	"sk": "Toto je preklad na informačné účely. Rozhodujúce je anglické znenie:",
	"sl": "To je prevod, namenjen obveščanju. Prevlada angleška različica:",
	"hu": "Ez egy tájékoztató jellegű fordítás. Az angol nyelvű változat az irányadó:",
	"ro": "Aceasta este o traducere cu titlu informativ. Prevalează versiunea în limba engleză:",
	"bg": "Това е превод с информационна цел. Меродавна е версията на английски език:",
	"hr": "Ovo je prijevod u informativne svrhe. Mjerodavna je engleska verzija:",
	"tr": "Bu, bilgilendirme amaçlı bir çeviridir. İngilizce sürüm esastır:",
	"id": "Ini adalah terjemahan untuk informasi. Versi bahasa Inggris yang berlaku:",
	"vi": "Đây là bản dịch để tham khảo. Bản tiếng Anh là bản có hiệu lực:",
	"th": "นี่คือคำแปลเพื่อให้ข้อมูล ฉบับภาษาอังกฤษเป็นฉบับที่มีผลบังคับ:",
	"zh": "這是供參考的翻譯。以英文版本為準：",
	"ar": "هذه ترجمة للاطلاع. النسخة الإنجليزية هي المعتمدة:",
	"sr": "Ovo je prevod u informativne svrhe. Merodavna je engleska verzija:",
	"sq": "Ky është një përkthim për qëllime informimi. Versioni në anglisht është ai që mbizotëron:",
	"ka": "ეს არის თარგმანი ინფორმაციის მიზნით. ძალაშია ინგლისური ვერსია:",
	"hy": "Սա թարգմանություն է տեղեկատվության նպատակով։ Գործում է անգլերեն տարբերակը՝",
	"az": "Bu, məlumat məqsədilə tərcümədir. İngilis dilindəki versiya qüvvədədir:",
	"bn": "এটি তথ্যের জন্য একটি অনুবাদ। ইংরেজি সংস্করণই প্রযোজ্য:",
}

// TranslationNoticeText is the notice's sentence in a language.
func TranslationNoticeText(lang string) string { return translationNotices[lang] }

// inEnglish marks a link from a translated page to a page that exists
// only in English, in the page's language.
var inEnglish = map[string]string{
	"de": "(auf Englisch)", "fr": "(en anglais)", "es": "(en inglés)", "it": "(in inglese)",
	"pt": "(em inglês)", "nl": "(in het Engels)", "pl": "(po angielsku)",
	"ko": "(영어)",
	"ja": "（英語）",
	"et": "(inglise keeles)",
	"lv": "(angliski)",
	"el": "(στα αγγλικά)",
	"lt": "(anglų kalba)",
	"cs": "(anglicky)",
	"sk": "(v angličtine)",
	"sl": "(v angleščini)",
	"hu": "(angolul)",
	"ro": "(în engleză)",
	"bg": "(на английски)",
	"hr": "(na engleskom)",
	"tr": "(İngilizce)",
	"id": "(dalam bahasa Inggris)",
	"vi": "(bằng tiếng Anh)",
	"th": "(ภาษาอังกฤษ)",
	"zh": "（英文）",
	"ar": "(بالإنجليزية)",
	"sr": "(na engleskom)",
	"sq": "(në anglisht)",
	"ka": "(ინგლისურად)",
	"hy": "(անգլերեն)",
	"az": "(ingiliscə)",
	"bn": "(ইংরেজিতে)",
}

// InEnglish is the marker a translated page puts after a link to an
// English-only page.
func InEnglish(lang string) string { return inEnglish[lang] }

// chromeLang marks the site's own navigation and footer as English on a
// translated page: they link to the English site, and a screen reader
// should read them as English.
func chromeLang(ctx context.Context) templ.Attributes {
	if PageLanguage(ctx) == "en" {
		return templ.Attributes{}
	}
	return templ.Attributes{"lang": "en"}
}
