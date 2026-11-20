package country

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A served section is its approved draft, byte for byte.
func TestEachServedSectionIsItsDraft(t *testing.T) {
	for c, langs := range Served {
		if langs[0] != "en" {
			t.Errorf("%s: English comes first, got %v", c, langs)
		}
		if Names[c] == "" {
			t.Errorf("%s has no name for the index", c)
		}
		for _, l := range langs {
			name := c + "." + l + ".md"
			served, err := files.ReadFile(name)
			if err != nil {
				t.Fatal(err)
			}
			draft, err := os.ReadFile(filepath.Join("../../../legal-drafts/country", name))
			if err != nil {
				t.Fatal(err)
			}
			if string(served) != string(draft) {
				t.Errorf("%s differs from web/legal-drafts/country/%s; copy the approved draft over", name, name)
			}
		}
	}
}

func TestKoreaRendersInBothLanguages(t *testing.T) {
	en, ok, err := Get("kr", "en")
	if err != nil || !ok {
		t.Fatalf("kr en: %v %v", ok, err)
	}
	ko, ok, err := Get("kr", "ko")
	if err != nil || !ok {
		t.Fatalf("kr ko: %v %v", ok, err)
	}
	if en.Title != "Korea" || ko.Title != "대한민국" {
		t.Errorf("titles %q %q", en.Title, ko.Title)
	}
	for _, s := range []Section{en, ko} {
		h := string(s.HTML)
		if !strings.Contains(h, "<table>") || !strings.Contains(h, `href="https://www.anthropic.com/legal/privacy"`) || !strings.Contains(h, `rel="noreferrer noopener"`) {
			t.Errorf("%s: the tables or links didn't render:\n%.400s", s.Lang, h)
		}
	}
	if _, ok, _ := Get("kr", "ja"); ok {
		t.Error("a language not served came back")
	}
	if _, ok, _ := Get("us", "en"); ok {
		t.Error("a country not served came back")
	}
}

// Raw HTML is dropped, and only safe links are made.
func TestRawHTMLIsOff(t *testing.T) {
	h := string(render([]byte("# T\n\n<script>alert(1)</script>\n\nText <b onclick=\"x()\">bold</b> [x](javascript:alert(1)) [y](https://example.com)\n")))
	for _, bad := range []string{"<script", "onclick", "<b", "javascript:"} {
		if strings.Contains(h, bad) {
			t.Errorf("rendered %q:\n%s", bad, h)
		}
	}
	if !strings.Contains(h, `href="https://example.com"`) {
		t.Errorf("a safe link was dropped:\n%s", h)
	}
}
