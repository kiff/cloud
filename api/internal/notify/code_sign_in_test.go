package notify

import (
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/words"
)

func TestTheCodeEmailsSpeakTheMoreLanguages(t *testing.T) {
	at := time.Date(2026, 11, 22, 14, 0, 0, 0, time.UTC)
	for tag, e := range words.CodeSignInEmailMore {
		l := words.CodeLang(tag)
		subject, text, html, err := renderSignInCode("123456", l, CodeAsked{At: at, Browser: "Firefox", Country: "FR"})
		if err != nil {
			t.Fatalf("%s: %v", tag, err)
		}
		if subject != e.CodeSubject {
			t.Errorf("%s: subject %q, want %q", tag, subject, e.CodeSubject)
		}
		for _, line := range []string{e.CodeIntro, e.CodeNeverShare, e.CodeNotYou} {
			if !strings.Contains(text, line) {
				t.Errorf("%s: the text lacks %q", tag, line)
			}
		}
		if !strings.Contains(html, "123456") {
			t.Errorf("%s: the code is not in the HTML", tag)
		}
		off, _, _, err := renderSignInCodeChanged(true, l, at)
		if err != nil || off != e.CodeTurnedOnSubject {
			t.Errorf("%s: turned-on subject %q (%v)", tag, off, err)
		}
	}
}
