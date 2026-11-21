package handlers

import (
	"net/http"

	"github.com/a-h/templ"

	"github.com/kiff/cloud/web/internal/pages"
)

// OfferLinks are the app's links, for /tafs and /pricing.
type OfferLinks = pages.OfferLinks

// Tafs serves /tafs: tafs, the KIFF family's product for business owners.
func Tafs(o OfferLinks) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = pages.Tafs(o).Render(r.Context(), w)
	})
}

// TafsWatch serves /tafs/watch: tafs on a watch, in English (the other
// languages are at /<lang>/tafs/watch).
func TafsWatch() http.Handler { return htmlPage(pages.TafsWatch()) }

// TafsPrivacy serves /tafs/privacy: tafs's own privacy policy, in English
// (the other languages are at /<lang>/tafs/privacy),
// with the Google user data section Google's review of the sign-in reads.
func TafsPrivacy() http.Handler { return inEnglish(htmlPage(pages.TafsPrivacy())) }

// TafsTerms serves /tafs/terms: tafs's terms, in English (the other
// languages are at /<lang>/tafs/terms), restating KIFF's.
func TafsTerms() http.Handler { return inEnglish(htmlPage(pages.TafsTerms())) }

// inEnglish marks a page's response as English: Content-Language: en, to
// match its <html lang="en">.
func inEnglish(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Language", "en")
		h.ServeHTTP(w, r)
	})
}

// htmlPage serves a page that needs nothing from the request.
func htmlPage(c templ.Component) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = c.Render(r.Context(), w)
	})
}
