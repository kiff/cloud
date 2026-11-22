package handlers

import (
	"github.com/kiff/cloud/web/internal/components"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/kiff/cloud/web/internal/content/country"
)

var htmlLangRE = regexp.MustCompile(`<html lang="([^"]*)"`)

// Every served legal page says its language twice, and the two agree: the
// Content-Language header, and <html lang> (which screen readers and a
// browser's translation read). Only the page head is checked, not the text.
func TestEveryServedLegalPageSaysItsLanguage(t *testing.T) {
	mux := http.NewServeMux()
	mux.Handle("GET /privacy", Privacy())
	mux.Handle("GET /terms", Terms())
	mux.Handle("GET /tafs/privacy", TafsPrivacy())
	mux.Handle("GET /tafs/terms", TafsTerms())
	mux.Handle("GET /privacy/countries", CountryIndex())
	mux.Handle("GET /privacy/countries/{country}", CountrySection())
	mux.Handle("GET /privacy/countries/{country}/{lang}", CountrySection())
	want := map[string]string{
		"/privacy": "en", "/terms": "en", "/tafs/privacy": "en", "/tafs/terms": "en",
		"/privacy/countries": "en",
	}
	for c, langs := range country.Served {
		for _, l := range langs {
			p := "/privacy/countries/" + c
			if l != "en" {
				p += "/" + l
			}
			want[p] = l
		}
	}
	for name, n := range country.Notices {
		mux.Handle("GET "+n.Path, LegalNotice(name))
		mux.Handle("GET "+n.Path+"/{lang}", LegalNotice(name))
		for _, l := range n.Langs {
			p := n.Path
			if l != "en" {
				p += "/" + l
			}
			want[p] = l
		}
	}
	// And every translated page, in its language.
	for _, tr := range TranslatedRoutes() {
		mux.Handle("GET "+tr.Path, Translated(tr.Lang, tr.Page, OfferLinks{}))
		want[tr.Path] = components.LangTag(tr.Lang)
	}
	for path, lang := range want {
		rec := getPage(t, mux, path)
		if rec.Code != http.StatusOK {
			t.Errorf("%s: status %d", path, rec.Code)
			continue
		}
		if got := rec.Header().Get("Content-Language"); got != lang {
			t.Errorf("%s: Content-Language %q, want %q", path, got, lang)
		}
		m := htmlLangRE.FindStringSubmatch(rec.Body.String())
		if m == nil || m[1] != lang {
			t.Errorf("%s: <html lang> %v, want %q", path, m, lang)
		}
	}
	if len(want) < 20 || !strings.Contains(strings.Join(keys(want), " "), "/legal/japan/ja") {
		t.Errorf("the test covers too few pages: %d", len(want))
	}
}

func keys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
