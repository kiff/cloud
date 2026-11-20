package handlers

import (
	"log/slog"
	"net/http"

	"github.com/kiff/cloud/web/internal/content/country"
	"github.com/kiff/cloud/web/internal/pages"
)

// languageNames are each section language's name in itself.
var countryLanguageNames = map[string]string{"en": "English", "ko": "한국어"}

// CountryIndex serves /privacy/countries: the countries with a section.
func CountryIndex() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var links []pages.CountryLink
		for _, c := range country.Countries() {
			links = append(links, pages.CountryLink{Name: country.Names[c], Path: "/privacy/countries/" + c, Lang: "en"})
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = pages.CountryIndex(links).Render(r.Context(), w)
	})
}

// CountrySection serves /privacy/countries/{country} (English) and
// /privacy/countries/{country}/{lang} (the country's language).
func CountrySection() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, lang := r.PathValue("country"), r.PathValue("lang")
		if lang == "" {
			lang = "en"
		} else if lang == "en" {
			http.Redirect(w, r, "/privacy/countries/"+c, http.StatusMovedPermanently)
			return
		}
		s, ok, err := country.Get(c, lang)
		if err != nil {
			slog.ErrorContext(r.Context(), "country section failed", slog.String("err", err.Error()))
			http.Error(w, "this page is temporarily unavailable", http.StatusInternalServerError)
			return
		}
		if !ok {
			http.NotFound(w, r)
			return
		}
		var others []pages.CountryLink
		for _, l := range country.Served[c] {
			if l == lang {
				continue
			}
			p := "/privacy/countries/" + c
			if l != "en" {
				p += "/" + l
			}
			others = append(others, pages.CountryLink{Name: countryLanguageNames[l], Path: p, Lang: l})
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Language", lang)
		_ = pages.CountrySection(s, others).Render(r.Context(), w)
	})
}
