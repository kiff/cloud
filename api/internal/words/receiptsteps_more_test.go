package words

import (
	"strings"
	"testing"
)

// Every language in ReceiptStepsMore words every step code English words, takes
// the step's free part (%s) where English does, and has an "asked again" form
// that carries the step's line.
func TestReceiptStepsMoreWordEveryCodeLikeEnglish(t *testing.T) {
	en := Of(EN).ReceiptStepLines
	if len(en) != 23 {
		t.Fatalf("English words %d codes", len(en))
	}
	for lang, r := range ReceiptStepsMore {
		if len(r.ReceiptStepLines) != len(en) {
			t.Errorf("%s: %d lines, English has %d", lang, len(r.ReceiptStepLines), len(en))
		}
		for code, want := range en {
			got := r.ReceiptStepLines[code]
			if strings.TrimSpace(got) == "" {
				t.Errorf("%s %s: no words", lang, code)
			}
			if strings.Count(got, "%s") != strings.Count(want, "%s") {
				t.Errorf("%s %s: the free part is placed differently from English: %q", lang, code, got)
			}
			if got == want {
				t.Errorf("%s %s: left in English", lang, code)
			}
		}
		if got := r.ReceiptAskedAgain("«x»"); !strings.Contains(got, "«x»") || got == "«x»" {
			t.Errorf("%s: asked again reads %q", lang, got)
		}
	}
	if len(ReceiptStepsMore) != 41 {
		t.Errorf("%d languages", len(ReceiptStepsMore))
	}
}
