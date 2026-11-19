package components

import (
	"context"
	"net/http/httptest"
	"testing"
)

// A screen the apps open speaks ?lang= first, then the browser's language
// by its q, then English.
func TestRequestLanguage(t *testing.T) {
	for _, c := range []struct{ target, accept, want string }{
		{"/auth/device?lang=de", "fr-FR,fr;q=0.9", "de"},
		{"/auth/device?lang=sv", "fr-FR,fr;q=0.9", "fr"},
		{"/auth/device", "fr;q=0.5, nl;q=0.8", "nl"},
		{"/auth/device", "it;q=0.7, es;q=0.7", "it"},
		{"/auth/device", "de;q=0, pl", "pl"},
		{"/auth/device", "sv-SE, da", "en"},
		{"/auth/device", "", "en"},
		{"/needs-you/exc_1?lang=PT", "", "pt"},
		{"/needs-you/exc_1", "pt_BR", "pt"},
		{"/needs-you/exc_1", "de;q=2, es;q=0.1", "es"},
	} {
		r := httptest.NewRequest("GET", c.target, nil)
		if c.accept != "" {
			r.Header.Set("Accept-Language", c.accept)
		}
		if got := RequestLanguage(r); got != c.want {
			t.Errorf("%s with %q: %q, want %q", c.target, c.accept, got, c.want)
		}
	}
}

// The app layout is English unless a screen says otherwise; a return URL
// keeps the screen's language.
func TestScreenLanguage(t *testing.T) {
	if got := ScreenLanguage(context.Background()); got != "en" {
		t.Errorf("no screen language: %q", got)
	}
	if got := ScreenLanguage(WithScreenLanguage(context.Background(), "nl")); got != "nl" {
		t.Errorf("Dutch: %q", got)
	}
	if got := ScreenLanguage(WithScreenLanguage(context.Background(), "xx")); got != "en" {
		t.Errorf("not a site language: %q", got)
	}
	for _, c := range []struct{ path, lang, want string }{
		{"/auth/device?code=AB12-CD34", "de", "/auth/device?code=AB12-CD34&lang=de"},
		{"/auth/device", "fr", "/auth/device?lang=fr"},
		{"/auth/device?code=X", "en", "/auth/device?code=X"},
	} {
		if got := KeepLang(c.path, c.lang); got != c.want {
			t.Errorf("KeepLang(%q, %q) = %q", c.path, c.lang, got)
		}
	}
}
