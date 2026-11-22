package words

import "testing"

func TestEveryMoreLanguageSpeaksTheCodeEmails(t *testing.T) {
	en := Of(EN).CodeSignInEmail
	for tag, want := range CodeSignInEmailMore {
		got := CodeSignInEmailFor(CodeLang(tag))
		if got != want {
			t.Errorf("%s: the emails don't read CodeSignInEmailMore's lines", tag)
		}
		if got.CodeSubject == en.CodeSubject || got.CodeNeverShare == en.CodeNeverShare || got.CodeTurnedOnSubject == en.CodeTurnedOnSubject {
			t.Errorf("%s: a code email line is still English", tag)
		}
	}
	if len(CodeSignInEmailMore) != 37 {
		t.Errorf("expected 37 more languages, got %d", len(CodeSignInEmailMore))
	}
}

func TestTheEightKeepTheirOwnCodeEmails(t *testing.T) {
	for _, l := range All {
		if CodeSignInEmailFor(l) != Of(l).CodeSignInEmail {
			t.Errorf("%s: the eight must read their own lines", l)
		}
	}
	if CodeSignInEmailFor("xx") != Of(EN).CodeSignInEmail {
		t.Error("an unknown language reads English")
	}
}

func TestCodeLangReadsTagsAndHeaders(t *testing.T) {
	for tag, want := range map[string]Lang{
		"ar": "ar", "ar-EG": "ar", "AR_eg": "ar", "pt-PT": PT, "de": DE, "zh-Hant": "zh", "xx": EN, "": EN, "en-GB": EN,
	} {
		if got := CodeLang(tag); got != want {
			t.Errorf("CodeLang(%q) = %q, want %q", tag, got, want)
		}
	}
	for header, want := range map[string]Lang{
		"ar":                      "ar",
		"ar-EG,ar;q=0.9,en;q=0.5": "ar",
		"xx,ko;q=0.8,en;q=0.5":    "ko",
		"de;q=0.4,th;q=0.9":       "th",
		"xx,yy":                   EN,
		"ko;q=0.5,en;q=0.9":       EN,
		"ko;q=0,en;q=0.1":         EN,
		"":                        EN,
		"th;q=2,ko;q=0.9":         "ko",
	} {
		if got := CodeLangFromHeader(header); got != want {
			t.Errorf("CodeLangFromHeader(%q) = %q, want %q", header, got, want)
		}
	}
}
