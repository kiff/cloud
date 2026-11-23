package country

import (
	"os"
	"path/filepath"
	"regexp"
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

// A served notice is its approved draft, byte for byte, at a /legal path.
func TestEachServedNoticeIsItsDraft(t *testing.T) {
	for name, n := range Notices {
		if len(n.Langs) == 0 || n.Langs[0] != "en" {
			t.Errorf("%s: English comes first, got %v", name, n.Langs)
		}
		if !strings.HasPrefix(n.Path, "/legal/") {
			t.Errorf("%s is served at %q, outside /legal", name, n.Path)
		}
		for _, l := range n.Langs {
			file := name + "." + l + ".md"
			served, err := files.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			draft, err := os.ReadFile(filepath.Join("../../../legal-drafts/country", file))
			if err != nil {
				t.Fatal(err)
			}
			if string(served) != string(draft) {
				t.Errorf("%s differs from web/legal-drafts/country/%s; copy the approved draft over", file, file)
			}
			if _, ok, err := GetNotice(name, l); err != nil || !ok {
				t.Errorf("%s %s: %v %v", name, l, ok, err)
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

// Both privacy policies, in each of the 8 languages, carry the line
// (web/legal-drafts/policy-link.md) as written, twice: at transfers and at
// rights, each linking this page.
func TestThePoliciesLinkThePageInEachLanguage(t *testing.T) {
	src, err := os.ReadFile("../../../legal-drafts/policy-link.md")
	if err != nil {
		t.Fatal(err)
	}
	rows := regexp.MustCompile(`(?m)^\| (en|de|fr|es|it|pt|nl|pl) \| ([^|]+) \| \*\*(.+?)\*\* (.+) \|$`).FindAllStringSubmatch(string(src), -1)
	if len(rows) != 8 {
		t.Fatalf("%d languages in policy-link.md, want 8", len(rows))
	}
	for _, r := range rows {
		lang, text, head, rest := r[1], strings.TrimSpace(r[2]), r[3], r[4]
		want := "<p><strong>" + head + "</strong> " + strings.Replace(rest, "{link}", `<a href="/privacy/countries">`+text+`</a>`, 1) + "</p>"
		prefix := ""
		if lang != "en" {
			prefix = lang + "_"
		}
		for _, page := range []string{"privacy", "tafs_privacy"} {
			b, err := os.ReadFile(filepath.Join("../../pages", prefix+page+".templ"))
			if err != nil {
				t.Fatal(err)
			}
			if n := strings.Count(string(b), want); n != 2 {
				t.Errorf("%s%s.templ carries the line %d times, want 2 (transfers and rights)", prefix, page, n)
			}
		}
	}
}

// Both privacy policies open their "who else receives data" section (KIFF's
// §6, tafs's §7) with the no-sale line, as written in each language.
func TestThePoliciesSayNoSaleInEachLanguage(t *testing.T) {
	src, err := os.ReadFile("../../../legal-drafts/policy-link.md")
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.SplitN(string(src), "## No sale, no advertising", 2)
	if len(parts) != 2 {
		t.Fatal("policy-link.md has no no-sale section")
	}
	rows := regexp.MustCompile(`(?m)^\| (en|de|fr|es|it|pt|nl|pl) \| (.+?) \|$`).FindAllStringSubmatch(parts[1], -1)
	if len(rows) != 8 {
		t.Fatalf("%d languages in the no-sale table, want 8", len(rows))
	}
	for _, r := range rows {
		lang, line := r[1], r[2]
		prefix := ""
		if lang != "en" {
			prefix = lang + "_"
		}
		for page, num := range map[string]string{"privacy": "6", "tafs_privacy": "7"} {
			b, err := os.ReadFile(filepath.Join("../../pages", prefix+page+".templ"))
			if err != nil {
				t.Fatal(err)
			}
			want := regexp.MustCompile(`<h2>` + num + `\. [^<]*</h2>\s*<p>` + regexp.QuoteMeta(line) + `</p>`)
			if !want.Match(b) {
				t.Errorf("%s%s.templ: §%s doesn't open with %q", prefix, page, num, line)
			}
		}
	}
}
