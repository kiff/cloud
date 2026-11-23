package words

import (
	"strings"
	"testing"
)

// Every shipped language has the footer's three lines, and the never-ask
// sentence still names KIFF and none of the English words that would be left over.
func TestEveryLanguageHasTheEmailFooter(t *testing.T) {
	if got := len(EmailFooters); got != 48 {
		t.Fatalf("the email footer is written in %d languages, want 48", got)
	}
	for code, f := range EmailFooters {
		for name, line := range map[string]string{"Privacy": f.Privacy, "Help": f.Help, "NeverAsk": f.NeverAsk} {
			if strings.TrimSpace(line) == "" {
				t.Errorf("%s: %s is empty", code, name)
			}
		}
		if !strings.Contains(f.NeverAsk, "KIFF") {
			t.Errorf("%s: the never-ask sentence lost the name KIFF: %q", code, f.NeverAsk)
		}
		if strings.ContainsAny(f.NeverAsk, "0123456789") {
			t.Errorf("%s: the never-ask sentence has a digit: %q", code, f.NeverAsk)
		}
	}
}
