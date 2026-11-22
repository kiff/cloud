package handlers

import (
	"net/http"

	"github.com/kiff/cloud/web/internal/components"
	"github.com/kiff/cloud/web/internal/pages"
)

// TranslatedRoute is one page in one language and the path it is served at.
type TranslatedRoute struct {
	Lang, Page, Path string
}

// TranslatedRoutes lists every translated page that is served: each page
// language other than English, each page it has, at "/<lang><page>".
// English stays at its own path, served by the page's own handler.
func TranslatedRoutes() []TranslatedRoute {
	var out []TranslatedRoute
	for _, lang := range components.PageLanguages {
		if lang == "en" {
			continue
		}
		for _, page := range components.TranslatedPages {
			if _, ok := pages.Translation(lang, page); ok {
				out = append(out, TranslatedRoute{Lang: lang, Page: page, Path: components.LangPath(lang, page)})
			}
		}
	}
	return out
}

// Translated serves one translated page. The language is the path's:
// the browser's language never redirects, so a URL is always one text.
func Translated(lang, page string, o OfferLinks) http.Handler {
	build, ok := pages.Translation(lang, page)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Language", components.LangTag(lang))
		ctx := r.Context()
		if page == "/tafs/download" {
			// Its order depends on the device, so a cache must keep each apart.
			w.Header().Add("Vary", "User-Agent")
			ctx = pages.WithDevice(ctx, deviceOf(r.UserAgent()))
		}
		_ = build(o).Render(ctx, w)
	})
}

// LanguageRootTargets maps each language's bare prefix ("/de" and "/de/")
// to where a reader who types it lands: that language's /tafs, or the
// home page for a language that has none. The prefix holds no page of its
// own. Found, not Moved Permanently, so a page added there later is not
// hidden by a browser's cached redirect.
func LanguageRootTargets() map[string]string {
	out := map[string]string{}
	for _, lang := range components.PageLanguages {
		if lang == "en" {
			continue
		}
		target := "/"
		if _, ok := pages.Translation(lang, "/tafs"); ok {
			target = components.LangPath(lang, "/tafs")
		}
		out["/"+lang] = target
		out["/"+lang+"/"] = target
	}
	return out
}

// LanguageRoot sends a bare language prefix to target.
func LanguageRoot(target string) http.Handler {
	return http.RedirectHandler(target, http.StatusFound)
}
