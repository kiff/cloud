package components

import (
	"context"
	"net/http"
	"strconv"
	"strings"
)

// The screens a tafs app opens in the browser (device approval at
// /auth/device, a held call at /needs-you/{id}) speak the app's language,
// not the path's: the app says it with ?lang=, and the browser's
// Accept-Language stands in when it doesn't. The translated public pages
// never read either; their language is their path (PageLanguage).

type screenLangKey struct{}

// RequestLanguage is the language a screen the apps open speaks: ?lang=
// when it's a site language, else the Accept-Language tag with the
// highest q that is one (the earlier on a tie), else English. A tag
// counts by its language ("pt-PT" is "pt"); q=0 is refused.
func RequestLanguage(r *http.Request) string {
	if l := siteCode(r.URL.Query().Get("lang")); l != "" {
		return l
	}
	best, bestQ := "en", 0.0
	for _, part := range strings.Split(r.Header.Get("Accept-Language"), ",") {
		tag, params, _ := strings.Cut(strings.TrimSpace(part), ";")
		if l, q := siteCode(tag), quality(params); l != "" && q > bestQ {
			best, bestQ = l, q
		}
	}
	return best
}

// siteCode is tag's language when it's a site language ("de-CH" is "de"),
// or "".
func siteCode(tag string) string {
	code, _, _ := strings.Cut(strings.ToLower(strings.TrimSpace(tag)), "-")
	code, _, _ = strings.Cut(code, "_")
	if IsSiteLanguage(code) {
		return code
	}
	return ""
}

// quality is a language's q: 1 when it gives none, 0 when it's malformed.
func quality(params string) float64 {
	for _, p := range strings.Split(params, ";") {
		k, v, ok := strings.Cut(strings.TrimSpace(p), "=")
		if !ok || strings.TrimSpace(k) != "q" {
			continue
		}
		q, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if err != nil || q < 0 || q > 1 {
			return 0
		}
		return q
	}
	return 1
}

// WithScreenLanguage marks ctx as rendering a screen in lang, for the
// app layout's <html lang>.
func WithScreenLanguage(ctx context.Context, lang string) context.Context {
	return context.WithValue(ctx, screenLangKey{}, lang)
}

// ScreenLanguage is the language of the app screen being rendered:
// English unless a handler set one with WithScreenLanguage.
func ScreenLanguage(ctx context.Context) string {
	if l, _ := ctx.Value(screenLangKey{}).(string); IsSiteLanguage(l) {
		return l
	}
	return "en"
}

// KeepLang adds lang to a URL on this site that brings the person back to
// the screen (after signing in, say), so the screen keeps its language.
// English adds nothing.
func KeepLang(path, lang string) string {
	if lang == "en" || !IsSiteLanguage(lang) {
		return path
	}
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	return path + sep + "lang=" + lang
}

type htmlLangKey struct{}

// WithHTMLLanguage marks ctx as rendering a public page whose text is in
// lang, though its path isn't a translated page's ("/privacy/countries/kr/ko"
// is Korean), so the document's <html lang> says so. It changes nothing else
// about the page: hreflang and the language row still follow the path.
func WithHTMLLanguage(ctx context.Context, lang string) context.Context {
	return context.WithValue(ctx, htmlLangKey{}, lang)
}

// HTMLLanguage is the language the public shell puts on <html lang>: the one
// a handler set with WithHTMLLanguage, else the language of the page's path.
func HTMLLanguage(ctx context.Context) string {
	if l, _ := ctx.Value(htmlLangKey{}).(string); l != "" {
		return l
	}
	return PageLanguage(ctx)
}
