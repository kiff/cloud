package handlers

import (
	"html"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kiff/cloud/web/internal/words"
)

func TestRegionRefusedSaysSoInThePersonsLanguageWithNothingToRetry(t *testing.T) {
	for _, c := range []struct{ name, target, accept, lang string }{
		{"no language", "/sign-in", "", "en"},
		{"the browser's", "/sign-in", "de-DE,de;q=0.9", "de"},
		{"the app's ui_locales over the browser's", "/sign-in/app?ui_locales=fr", "de", "fr"},
		{"an unknown ui_locales", "/sign-in/app?ui_locales=xx", "pl", "pl"},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, c.target, nil)
			if c.accept != "" {
				r.Header.Set("Accept-Language", c.accept)
			}
			w := httptest.NewRecorder()
			RegionRefused(w, r, "ZZ")
			if w.Code != http.StatusForbidden {
				t.Fatalf("status %d, want 403", w.Code)
			}
			for k, want := range map[string]string{"Content-Language": c.lang, "Cache-Control": "no-store", "X-Frame-Options": "DENY", "Content-Type": "text/html; charset=utf-8"} {
				if got := w.Header().Get(k); got != want {
					t.Errorf("%s: %q, want %q", k, got, want)
				}
			}
			if csp := w.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'none'") || strings.Contains(csp, "script-src") {
				t.Errorf("CSP %q: the page runs nothing", csp)
			}
			body := w.Body.String()
			if !strings.Contains(html.UnescapeString(body), words.RegionIn(c.lang).Here) {
				t.Errorf("the page doesn't say %q", words.RegionIn(c.lang).Here)
			}
			if !strings.Contains(body, `<html lang="`+c.lang+`">`) {
				t.Error("the page isn't marked in its language")
			}
			// Nothing to retry or submit, and the guessed country never shown.
			for _, no := range []string{"<form", "<script", "<a ", "ZZ"} {
				if strings.Contains(body, no) {
					t.Errorf("the page has %q", no)
				}
			}
		})
	}
}
