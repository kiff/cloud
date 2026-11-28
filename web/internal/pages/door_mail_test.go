package pages

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// /tafs/privacy's Doors item ends with how an email door's mail comes in, in the page's own language: one per page,
// the same language as its bank data item, naming Resend and chutchut, never the English fallback, and addressing
// the reader as the page does.
func TestDoorMailIsSaidInEveryLanguage(t *testing.T) {
	files, err := filepath.Glob("*tafs_privacy.templ")
	if err != nil || len(files) != 49 {
		t.Fatalf("want 49 tafs privacy templates, got %d", len(files))
	}
	bank := regexp.MustCompile(`bankDataWords\("([a-z-]+)"\)`)
	door := regexp.MustCompile(`doorMailWords\("([a-z-]+)"\)`)
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		n := door.FindAllSubmatch(b, -1)
		if len(n) != 1 {
			t.Errorf("%s: %d door mail lines, want 1", f, len(n))
			continue
		}
		lang := string(n[0][1])
		if m := bank.FindSubmatch(b); m == nil || string(m[1]) != lang {
			t.Errorf("%s: door mail line in %s, not the page's language", f, lang)
		}
		w, ok := doorMailText[lang]
		if !ok || !strings.Contains(w, "Resend") || !strings.Contains(w, "chutchut") || !strings.Contains(w, "30") ||
			(lang != "en" && w == doorMailText["en"]) {
			t.Errorf("%s: %q", lang, w)
		}
	}
	if len(doorMailText) != len(bankDataText) {
		t.Errorf("%d languages for the door mail line, %d for bank data", len(doorMailText), len(bankDataText))
	}
	for lang, want := range map[string]string{"de": "wartet auf dich", "nl": "wacht op jou", "es": "te espera", "ca": "us espera", "dv": "ތިބާ"} {
		if !strings.Contains(doorMailText[lang], want) {
			t.Errorf("%s: no %q, the page's form of address", lang, want)
		}
	}
}
