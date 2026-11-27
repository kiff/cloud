package pages

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Every language says it in its own words, naming Qonto (as each language inflects it) and IBAN, with a label
// and text of its own: no language falls back to English.
func TestBankDataIsSaidInEveryLanguage(t *testing.T) {
	en := bankDataText["en"]
	files, err := filepath.Glob("*privacy.templ")
	if err != nil || len(files) != 98 {
		t.Fatalf("want 98 privacy templates, got %d", len(files))
	}
	use := regexp.MustCompile(`bankDataWords\("([a-z-]+)"\)`)
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		m := use.FindSubmatch(b)
		if m == nil {
			t.Errorf("%s: no bank data item", f)
			continue
		}
		lang := string(m[1])
		w, ok := bankDataText[lang]
		if !ok {
			t.Errorf("%s: no bank data words for %s", f, lang)
			continue
		}
		if w.Label == "" || !strings.Contains(w.Text, "Qont") || !strings.Contains(w.Text, "IBAN") || !strings.Contains(w.Text, "planque") || !strings.Contains(w.Text, "KIFF") {
			t.Errorf("%s: %q %q", lang, w.Label, w.Text)
		}
		if lang != "en" && (w.Label == en.Label || w.Text == en.Text) {
			t.Errorf("%s: still English", lang)
		}
	}
	if len(bankDataText) != len(hostedBrowserText) {
		t.Errorf("%d languages for bank data, %d for the hosted browser", len(bankDataText), len(hostedBrowserText))
	}
}

// Both policies show the bank data item: /privacy's categories and
// /tafs/privacy's list of what tafs collects.
func TestBothPoliciesSayBankData(t *testing.T) {
	var b strings.Builder
	if err := privacyCategories().Render(context.Background(), &b); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), "lifts its banking secrecy") || !strings.Contains(b.String(), "add and change clients") || !strings.Contains(b.String(), "credit notes") {
		t.Errorf("/privacy lacks the bank data item")
	}
	b.Reset()
	if err := TafsPrivacyFR().Render(context.Background(), &b); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), "levée de son secret bancaire") {
		t.Errorf("/tafs/privacy (fr) lacks the bank data item")
	}
}
