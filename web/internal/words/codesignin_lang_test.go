package words

import "testing"

func TestEveryMoreLanguageHasCodeSignInWordsThatAreNotEnglish(t *testing.T) {
	en := CodeSignInIn("en")
	if len(codeSignInMore) != 37 {
		t.Fatalf("expected 37 more languages, got %d", len(codeSignInMore))
	}
	for tag := range codeSignInMore {
		if CodeSignInTag(tag) != tag {
			t.Errorf("%s: CodeSignInTag doesn't know it", tag)
		}
		got := CodeSignInIn(tag)
		if got.Link == en.Link || got.Send == en.Send || got.SecondFactor == en.SecondFactor || got.SettingTitle == en.SettingTitle {
			t.Errorf("%s: a line is still English (Link %q)", tag, got.Link)
		}
	}
}

func TestCodeSignInTagAndHeader(t *testing.T) {
	for tag, want := range map[string]string{"ar": "ar", "ar-EG": "ar", "AR_eg": "ar", "pt-PT": "pt", "en-GB": "en", "de": "de", "zh-Hant": "zh", "xx": "", "": ""} {
		if got := CodeSignInTag(tag); got != want {
			t.Errorf("CodeSignInTag(%q) = %q, want %q", tag, got, want)
		}
	}
	for header, want := range map[string]string{
		"ar-EG,ar;q=0.9,en;q=0.5": "ar", "xx,ko;q=0.8,en;q=0.5": "ko", "de;q=0.4,th;q=0.9": "th", "xx,yy": "en",
		"ko;q=0.5,en;q=0.9": "en", "ko;q=0,en;q=0.1": "en", "": "en", "th;q=2,ko;q=0.9": "ko",
	} {
		if got := CodeSignInFromHeader(header); got != want {
			t.Errorf("CodeSignInFromHeader(%q) = %q, want %q", header, got, want)
		}
	}
}
