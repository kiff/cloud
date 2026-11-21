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
		{"ko", "/privacy#tafs", "/ko/privacy#tafs"},
		{"ko", "/tafs/terms", "/ko/tafs/terms"},
		{"ko", "/cookies", "/cookies"}, // Korean has the legal texts only
		{"ja", "/privacy#delete-account", "/ja/privacy#delete-account"},
		{"ja", "/tafs/privacy", "/ja/tafs/privacy"},
		{"ja", "/subprocessors#composio", "/subprocessors#composio"}, // so has Japanese
		{"et", "/terms#fees", "/et/terms#fees"},
		{"et", "/tafs/privacy", "/et/tafs/privacy"},
		{"et", "/legal/dpa", "/legal/dpa"}, // and Estonian
		{"lv", "/terms#fees", "/lv/terms#fees"},
		{"lv", "/tafs/privacy#google", "/lv/tafs/privacy#google"},
		{"lv", "/acceptable-use", "/acceptable-use"}, // and Latvian
		{"el", "/terms#fees", "/el/terms#fees"},
		{"el", "/tafs/privacy", "/el/tafs/privacy"},
		{"el", "/security", "/security"}, // and Greek
		{"lt", "/terms#fees", "/lt/terms#fees"},
		{"lt", "/tafs/privacy", "/lt/tafs/privacy"},
		{"lt", "/security", "/security"}, // and Lithuanian
		{"cs", "/terms#fees", "/cs/terms#fees"},
		{"cs", "/tafs/privacy", "/cs/tafs/privacy"},
		{"cs", "/cookies", "/cookies"}, // and Czech
		{"sk", "/terms#fees", "/sk/terms#fees"},
		{"sk", "/tafs/privacy", "/sk/tafs/privacy"},
		{"sk", "/security", "/security"}, // and Slovak
		{"sl", "/terms#fees", "/sl/terms#fees"},
		{"sl", "/tafs/privacy", "/sl/tafs/privacy"},
		{"sl", "/ai-act", "/ai-act"}, // and Slovene
		{"hu", "/terms#fees", "/hu/terms#fees"},
		{"hu", "/tafs/privacy", "/hu/tafs/privacy"},
		{"hu", "/cookies", "/cookies"}, // and Hungarian
		{"ro", "/terms#fees", "/ro/terms#fees"},
		{"ro", "/tafs/privacy", "/ro/tafs/privacy"},
		{"ro", "/security", "/security"}, // and Romanian
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
		{"/ko/tafs/privacy", "ko", "/tafs/privacy", true},
		{"/ko/cookies", "en", "/ko/cookies", false},
		{"/ja/terms", "ja", "/terms", true},
		{"/ja/tafs", "en", "/ja/tafs", false},
		{"/et/tafs/terms", "et", "/tafs/terms", true},
		{"/et/security", "en", "/et/security", false},
		{"/lv/tafs/terms", "lv", "/tafs/terms", true},
		{"/lv/support", "en", "/lv/support", false},
		{"/el/tafs/terms", "el", "/tafs/terms", true},
		{"/el/support", "en", "/el/support", false},
		{"/lt/tafs/terms", "lt", "/tafs/terms", true},
		{"/lt/support", "en", "/lt/support", false},
		{"/cs/tafs/terms", "cs", "/tafs/terms", true},
		{"/cs/support", "en", "/cs/support", false},
		{"/sk/tafs/terms", "sk", "/tafs/terms", true},
		{"/sk/support", "en", "/sk/support", false},
		{"/sl/tafs/terms", "sl", "/tafs/terms", true},
		{"/sl/support", "en", "/sl/support", false},
		{"/hu/tafs/terms", "hu", "/tafs/terms", true},
		{"/hu/support", "en", "/hu/support", false},
		{"/ro/tafs/terms", "ro", "/tafs/terms", true},
		{"/ro/support", "en", "/ro/support", false},
		{"/pricing", "en", "/pricing", false},
		{"/", "en", "/", false},
	} {
		lang, page, ok := SplitLangPath(c.path)
		if lang != c.lang || page != c.page || ok != c.ok {
			t.Errorf("SplitLangPath(%q) = %q, %q, %v; want %q, %q, %v", c.path, lang, page, ok, c.lang, c.page, c.ok)
		}
	}
}

// Every page language has its name, its notice and its marker.
func TestEveryLanguageHasItsWords(t *testing.T) {
	for _, l := range PageLanguages {
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
