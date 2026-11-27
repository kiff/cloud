package pages

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"unicode"
)

// /tafs/privacy's line on a connection's token ends with the revoke note in its own language: the
// same language as the page's bank data item, naming Qonto and tafs, never the English fallback.
func TestRevokeNoteIsSaidInEveryLanguage(t *testing.T) {
	files, err := filepath.Glob("*tafs_privacy.templ")
	if err != nil || len(files) != 49 {
		t.Fatalf("want 49 tafs privacy templates, got %d", len(files))
	}
	bank := regexp.MustCompile(`bankDataWords\("([a-z-]+)"\)`)
	note := regexp.MustCompile(`revokeNoteWords\("([a-z-]+)"\)`)
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		n := note.FindAllSubmatch(b, -1)
		if len(n) != 1 {
			t.Errorf("%s: %d revoke notes, want 1", f, len(n))
			continue
		}
		lang := string(n[0][1])
		if m := bank.FindSubmatch(b); m == nil || string(m[1]) != lang {
			t.Errorf("%s: revoke note in %s, not the page's language", f, lang)
		}
		w, ok := revokeNoteText[lang]
		if !ok {
			t.Errorf("%s: no revoke note for %s", f, lang)
			continue
		}
		if !strings.Contains(w, "Qonto") || !strings.Contains(w, "tafs") {
			t.Errorf("%s: %q", lang, w)
		}
		if lang != "en" && w == revokeNoteText["en"] {
			t.Errorf("%s: still English", lang)
		}
	}
	for _, f := range files {
		b, _ := os.ReadFile(f)
		n := regexp.MustCompile(`chatEndsWords\("([a-z-]+)"\)`).FindAllSubmatch(b, -1)
		if len(n) != 1 {
			t.Errorf("%s: %d chat-deletion lines, want 1", f, len(n))
			continue
		}
		lang := string(n[0][1])
		if m := note.FindSubmatch(b); m == nil || string(m[1]) != lang {
			t.Errorf("%s: chat-deletion line in %s, not the page's language", f, lang)
		}
		w, ok := chatEndsText[lang]
		if !ok || !strings.Contains(w, "Coach") || !strings.Contains(w, "taf") || (lang != "en" && w == chatEndsText["en"]) {
			t.Errorf("%s: %q", lang, w)
		}
	}
	if len(chatEndsText) != len(bankDataText) {
		t.Errorf("%d languages for the chat-deletion line, %d for bank data", len(chatEndsText), len(bankDataText))
	}
	if len(revokeNoteText) != len(bankDataText) {
		t.Errorf("%d languages for the revoke note, %d for bank data", len(revokeNoteText), len(bankDataText))
	}
}

// Serbian pages are in Latin script and Chinese pages in Traditional characters, addressing the
// reader as 您, so the words these pages take from Go are too.
func TestSerbianAndChineseWordsMatchTheirPages(t *testing.T) {
	for name, s := range map[string]string{
		"bank label": bankDataText["sr"].Label, "bank text": bankDataText["sr"].Text, "revoke note": revokeNoteText["sr"],
	} {
		for _, r := range s {
			if unicode.Is(unicode.Cyrillic, r) {
				t.Errorf("sr %s: Cyrillic %q in %q", name, r, s)
				break
			}
		}
	}
	simplified := "们银这设资户连删账为与时会处网"
	for name, s := range map[string]string{
		"bank label": bankDataText["zh"].Label, "bank text": bankDataText["zh"].Text, "revoke note": revokeNoteText["zh"],
	} {
		if strings.ContainsAny(s, simplified) || strings.Contains(s, "你") {
			t.Errorf("zh %s: Simplified or 你 in %q", name, s)
		}
	}
}

// The note addresses the reader as its page does: German du, Dutch je, Spanish tú, Catalan vosaltres
// and Dhivehi ތިބާ, as each page's own connection line does; so do the Catalan and Dhivehi bank items.
func TestRevokeNoteAddressesTheReaderAsItsPageDoes(t *testing.T) {
	for lang, w := range map[string]struct{ has, hasnt []string }{
		"de": {[]string{"bis du ", "den du uns", "wenn du einen chat"}, []string{"Sie "}},
		"nl": {[]string{"tot je ", "die je ons", "als je een chat"}, []string{" u "}},
		"es": {[]string{"retires", "hayas", "elimines", "desconectaras"}, []string{"retire ", "haya dado", "elimine ", "desconectara "}},
		"ca": {[]string{"retireu", "hàgiu", "elimineu", "connecteu", "vostre banc", "desconnectéssiu"}, []string{"retiris", "hagis", "eliminis", "connectes", "teu banc"}},
		"dv": {[]string{"ތިބާ"}, []string{"ތިޔަބޭފުޅާ"}},
	} {
		note := strings.ToLower(revokeNoteText[lang]) + " " + strings.ToLower(chatEndsText[lang])
		if lang == "de" {
			note = revokeNoteText[lang] + " " + strings.ToLower(chatEndsText[lang])
		}
		if lang == "ca" || lang == "dv" {
			note += " " + bankDataText[lang].Label + " " + bankDataText[lang].Text
		}
		for _, s := range w.has {
			if !strings.Contains(note, s) {
				t.Errorf("%s: no %q in %q", lang, s, note)
			}
		}
		for _, s := range w.hasnt {
			if strings.Contains(note, s) {
				t.Errorf("%s: %q in %q", lang, s, note)
			}
		}
	}
}

// The English /tafs/privacy says, right after "then revoked", that a sign-in is revoked only where
// the vendor offers a way to, and that a key keeps working at its vendor until the owner deletes it there.
func TestTafsPrivacySaysRevokingDependsOnTheVendor(t *testing.T) {
	var b strings.Builder
	if err := TafsPrivacy().Render(context.Background(), &b); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), "then revoked. Revoking depends on the vendor") {
		t.Errorf("/tafs/privacy lacks the revoke note right after \"then revoked\"")
	}
	if !strings.Contains(b.String(), "A key you gave us is deleted here too, and keeps working at its vendor until you delete it there. Deleting a chat with Coach also ends the connections it asked for that no taf uses, as disconnecting them would. The record of its approvals: until the account ends.") {
		t.Errorf("/tafs/privacy lacks the key's half of the revoke note")
	}
}
