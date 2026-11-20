package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCountryPages(t *testing.T) {
	mux := http.NewServeMux()
	mux.Handle("GET /privacy/countries", CountryIndex())
	mux.Handle("GET /privacy/countries/{country}", CountrySection())
	mux.Handle("GET /privacy/countries/{country}/{lang}", CountrySection())
	get := func(path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		return w
	}
	if w := get("/privacy/countries"); w.Code != 200 || !strings.Contains(w.Body.String(), `href="/privacy/countries/kr"`) {
		t.Errorf("index: %d", w.Code)
	}
	w := get("/privacy/countries/kr")
	if w.Code != 200 || w.Header().Get("Content-Language") != "en" || !strings.Contains(w.Body.String(), `lang="en"`) || !strings.Contains(w.Body.String(), `href="/privacy/countries/kr/ko"`) {
		t.Errorf("kr: %d %s", w.Code, w.Header().Get("Content-Language"))
	}
	w = get("/privacy/countries/kr/ko")
	if w.Code != 200 || w.Header().Get("Content-Language") != "ko" || !strings.Contains(w.Body.String(), `<article class="legal prose" lang="ko">`) || !strings.Contains(w.Body.String(), "대한민국") || !strings.Contains(w.Body.String(), `href="/privacy/countries/kr"`) {
		t.Errorf("kr/ko: %d %s", w.Code, w.Header().Get("Content-Language"))
	}
	if w := get("/privacy/countries/kr/en"); w.Code != http.StatusMovedPermanently || w.Header().Get("Location") != "/privacy/countries/kr" {
		t.Errorf("kr/en: %d %s", w.Code, w.Header().Get("Location"))
	}
	for _, p := range []string{"/privacy/countries/us", "/privacy/countries/kr/ja", "/privacy/countries/..%2fx"} {
		if w := get(p); w.Code != 404 {
			t.Errorf("%s: %d, want 404", p, w.Code)
		}
	}
}
