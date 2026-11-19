package components

import "testing"

func TestLangPath(t *testing.T) {
	for _, c := range []struct{ lang, path, want string }{
		{"de", "/privacy", "/de/privacy"},
		{"de", "/privacy#tafs", "/de/privacy#tafs"},
		{"pl", "/legal/dpa", "/pl/legal/dpa"},
		{"en", "/privacy", "/privacy"},
		{"de", "/pricing", "/pricing"}, // English only: the link stays English
		{"xx", "/privacy", "/privacy"}, // not a site language
		{"de", "/docs/tafs", "/docs/tafs"},
	} {
		if got := LangPath(c.lang, c.path); got != c.want {
			t.Errorf("LangPath(%q, %q) = %q, want %q", c.lang, c.path, got, c.want)
		}
	}
}

func TestSplitLangPath(t *testing.T) {
	for _, c := range []struct {
		path, lang, page string
		ok               bool
	}{
		{"/de/privacy", "de", "/privacy", true},
		{"/privacy", "en", "/privacy", true},
		{"/pl/legal/dpa/", "pl", "/legal/dpa", true},
		{"/en/privacy", "en", "/en/privacy", false}, // English has no prefix
		{"/de/pricing", "en", "/de/pricing", false},
		{"/pricing", "en", "/pricing", false},
		{"/", "en", "/", false},
	} {
		lang, page, ok := SplitLangPath(c.path)
		if lang != c.lang || page != c.page || ok != c.ok {
			t.Errorf("SplitLangPath(%q) = %q, %q, %v; want %q, %q, %v", c.path, lang, page, ok, c.lang, c.page, c.ok)
		}
	}
}

// Every site language has its name, its notice and its marker.
func TestEveryLanguageHasItsWords(t *testing.T) {
	for _, l := range SiteLanguages {
		if LanguageName(l) == "" {
			t.Errorf("%s has no name", l)
		}
		if l == "en" {
			continue
		}
		if TranslationNoticeText(l) == "" || InEnglish(l) == "" {
			t.Errorf("%s lacks its notice or its marker", l)
		}
	}
}
