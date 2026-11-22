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
		{"bg", "/terms#fees", "/bg/terms#fees"},
		{"bg", "/tafs/privacy", "/bg/tafs/privacy"},
		{"bg", "/subprocessors", "/subprocessors"}, // and Bulgarian
		{"hr", "/terms#fees", "/hr/terms#fees"},
		{"hr", "/tafs/privacy", "/hr/tafs/privacy"},
		{"hr", "/security", "/security"}, // and Croatian
		{"si", "/terms#fees", "/si/terms#fees"},
		{"si", "/tafs/privacy", "/si/tafs/privacy"},
		{"si", "/security", "/security"}, // and Sinhala
		{"lo", "/terms#fees", "/lo/terms#fees"},
		{"lo", "/tafs/privacy", "/lo/tafs/privacy"},
		{"lo", "/security", "/security"}, // and Lao
		{"km", "/terms#fees", "/km/terms#fees"},
		{"km", "/tafs/privacy", "/km/tafs/privacy"},
		{"km", "/security", "/security"}, // and Khmer
		{"ne", "/terms#fees", "/ne/terms#fees"},
		{"ne", "/tafs/privacy", "/ne/tafs/privacy"},
		{"ne", "/security", "/security"}, // and Nepali
		{"mn", "/terms#fees", "/mn/terms#fees"},
		{"mn", "/tafs/privacy", "/mn/tafs/privacy"},
		{"mn", "/security", "/security"}, // and Mongolian
		{"mk", "/terms#fees", "/mk/terms#fees"},
		{"mk", "/tafs/privacy", "/mk/tafs/privacy"},
		{"mk", "/security", "/security"}, // and Macedonian
		{"ca", "/terms#fees", "/ca/terms#fees"},
		{"ca", "/tafs/privacy", "/ca/tafs/privacy"},
		{"ca", "/security", "/security"}, // and Catalan
		{"he", "/terms#fees", "/he/terms#fees"},
		{"he", "/tafs/privacy", "/he/tafs/privacy"},
		{"he", "/security", "/security"}, // and Hebrew
		{"bn", "/terms#fees", "/bn/terms#fees"},
		{"bn", "/tafs/privacy", "/bn/tafs/privacy"},
		{"bn", "/security", "/security"}, // and Bengali
		{"az", "/terms#fees", "/az/terms#fees"},
		{"az", "/tafs/privacy", "/az/tafs/privacy"},
		{"az", "/security", "/security"}, // and Azerbaijani
		{"hy", "/terms#fees", "/hy/terms#fees"},
		{"hy", "/tafs/privacy", "/hy/tafs/privacy"},
		{"hy", "/security", "/security"}, // and Armenian
		{"ka", "/terms#fees", "/ka/terms#fees"},
		{"ka", "/tafs/privacy", "/ka/tafs/privacy"},
		{"ka", "/security", "/security"}, // and Georgian
		{"sq", "/terms#fees", "/sq/terms#fees"},
		{"sq", "/tafs/privacy", "/sq/tafs/privacy"},
		{"sq", "/security", "/security"}, // and Albanian
		{"sr", "/terms#fees", "/sr/terms#fees"},
		{"sr", "/tafs/privacy", "/sr/tafs/privacy"},
		{"sr", "/security", "/security"}, // and Serbian
		{"ar", "/terms#fees", "/ar/terms#fees"},
		{"ar", "/tafs/privacy", "/ar/tafs/privacy"},
		{"ar", "/security", "/security"}, // and Arabic
		{"zh", "/terms#fees", "/zh/terms#fees"},
		{"zh", "/tafs/privacy", "/zh/tafs/privacy"},
		{"zh", "/security", "/security"}, // and Traditional Chinese
		{"th", "/terms#fees", "/th/terms#fees"},
		{"th", "/tafs/privacy", "/th/tafs/privacy"},
		{"th", "/security", "/security"}, // and Thai
		{"vi", "/terms#fees", "/vi/terms#fees"},
		{"vi", "/tafs/privacy", "/vi/tafs/privacy"},
		{"vi", "/security", "/security"}, // and Vietnamese
		{"id", "/terms#fees", "/id/terms#fees"},
		{"id", "/tafs/privacy", "/id/tafs/privacy"},
		{"id", "/security", "/security"}, // and Indonesian
		{"tr", "/terms#fees", "/tr/terms#fees"},
		{"tr", "/tafs/privacy", "/tr/tafs/privacy"},
		{"tr", "/security", "/security"}, // and Turkish
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
		{"/bg/tafs/terms", "bg", "/tafs/terms", true},
		{"/bg/cookies", "en", "/bg/cookies", false},
		{"/hr/tafs/terms", "hr", "/tafs/terms", true},
		{"/hr/support", "en", "/hr/support", false},
		{"/si/tafs/terms", "si", "/tafs/terms", true},
		{"/si/support", "en", "/si/support", false},
		{"/lo/tafs/terms", "lo", "/tafs/terms", true},
		{"/lo/support", "en", "/lo/support", false},
		{"/km/tafs/terms", "km", "/tafs/terms", true},
		{"/km/support", "en", "/km/support", false},
		{"/ne/tafs/terms", "ne", "/tafs/terms", true},
		{"/ne/support", "en", "/ne/support", false},
		{"/mn/tafs/terms", "mn", "/tafs/terms", true},
		{"/mn/support", "en", "/mn/support", false},
		{"/mk/tafs/terms", "mk", "/tafs/terms", true},
		{"/mk/support", "en", "/mk/support", false},
		{"/ca/tafs/terms", "ca", "/tafs/terms", true},
		{"/ca/support", "en", "/ca/support", false},
		{"/he/tafs/terms", "he", "/tafs/terms", true},
		{"/he/support", "en", "/he/support", false},
		{"/bn/tafs/terms", "bn", "/tafs/terms", true},
		{"/bn/support", "en", "/bn/support", false},
		{"/az/tafs/terms", "az", "/tafs/terms", true},
		{"/az/support", "en", "/az/support", false},
		{"/hy/tafs/terms", "hy", "/tafs/terms", true},
		{"/hy/support", "en", "/hy/support", false},
		{"/ka/tafs/terms", "ka", "/tafs/terms", true},
		{"/ka/support", "en", "/ka/support", false},
		{"/sq/tafs/terms", "sq", "/tafs/terms", true},
		{"/sq/support", "en", "/sq/support", false},
		{"/sr/tafs/terms", "sr", "/tafs/terms", true},
		{"/sr/support", "en", "/sr/support", false},
		{"/ar/tafs/terms", "ar", "/tafs/terms", true},
		{"/ar/support", "en", "/ar/support", false},
		{"/zh/tafs/terms", "zh", "/tafs/terms", true},
		{"/zh/support", "en", "/zh/support", false},
		{"/th/tafs/terms", "th", "/tafs/terms", true},
		{"/th/support", "en", "/th/support", false},
		{"/vi/tafs/terms", "vi", "/tafs/terms", true},
		{"/vi/support", "en", "/vi/support", false},
		{"/id/tafs/terms", "id", "/tafs/terms", true},
		{"/id/support", "en", "/id/support", false},
		{"/tr/tafs/terms", "tr", "/tafs/terms", true},
		{"/tr/support", "en", "/tr/support", false},
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
