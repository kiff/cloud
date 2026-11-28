package pages

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// /tafs/privacy's Doors item ends with how an email door's mail comes in, in the page's own language: one per page,
// the same language as its bank data item, naming chutchut and not Resend (every door's mail comes in through chutchut),
// never the English fallback.
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
		if !ok || strings.Contains(w, "Resend") || !strings.Contains(w, "chutchut") || !strings.Contains(w, "30") ||
			(lang != "en" && w == doorMailText["en"]) {
			t.Errorf("%s: %q", lang, w)
		}
	}
	if len(doorMailText) != len(bankDataText) {
		t.Errorf("%d languages for the door mail line, %d for bank data", len(doorMailText), len(bankDataText))
	}
	// Held mail isn't shown to anyone yet, so no language says it waits for the reader.
	if strings.Contains(doorMailText["en"], "waits for you") || !strings.Contains(doorMailText["en"], "never passed to the taf") {
		t.Errorf("en: held mail described as waiting for the reader: %q", doorMailText["en"])
	}
}

// Every email door's mail comes in through chutchut (one provider setting for all doors), so no page in any language
// gives Resend a part in an email door: the tafs privacy company lists, /subprocessors and the DPA.
func TestNoPageGivesResendTheEmailDoor(t *testing.T) {
	files, err := filepath.Glob("*.templ")
	if err != nil {
		t.Fatal(err)
	}
	door := regexp.MustCompile(`(?i)Resend[^<]{0,80}(email door|e-mail-tür|puerta de correo|porte e-mail|porta email|e-maildeur|drzwi e-mail|porta de email|porta de e-mail)`)
	resendCell := regexp.MustCompile(`(?s)<tr id="resend">.*?</tr>|Resend, Inc\.</td>\s*<td>[^<]*</td>`)
	doorWord := regexp.MustCompile(`(?i)(email door|e-mail-tür|puerta de correo|porte e-mail|porta email|e-maildeur|drzwi e-mail|porta de email)`)
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if door.Match(b) {
			t.Errorf("%s: Resend is still named for an email door", f)
		}
		for _, cell := range resendCell.FindAll(b, -1) {
			if doorWord.Match(cell) {
				t.Errorf("%s: Resend's row still names an email door", f)
			}
		}
	}
}

// tafs sends no mail through Resend any more: its door mail, its door replies and reported answers all go through
// chutchut on Amazon SES, so every /tafs/privacy company list names Amazon SES and not Resend.
func TestTafsPrivacyNamesNoResend(t *testing.T) {
	files, err := filepath.Glob("*tafs_privacy.templ")
	if err != nil || len(files) != 49 {
		t.Fatalf("want 49 tafs privacy templates, got %d", len(files))
	}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), "Resend") || !strings.Contains(string(b), "Amazon SES") {
			t.Errorf("%s: names Resend, or not Amazon SES", f)
		}
	}
}
