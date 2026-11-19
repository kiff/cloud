package pages

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// The Limits surface — which the card surface replaced — shipped with
// three classes no stylesheet defined: its two-column layout stacked,
// its empty state had no frame, and — worst — its warning that usage
// could not be read rendered as ordinary body text. A page that exists
// to be believed about numbers cannot have a warning that looks like a
// note.
//
// This test asserts the correspondence rather than the appearance: every
// class the card templates name must be defined in a stylesheet those
// templates actually load. It cannot tell whether a rule looks right,
// but it can tell that a rule exists, which is the failure that
// happened.
func TestCardPagesDefineEveryClassTheyUse(t *testing.T) {
	t.Parallel()

	// Each page and the sheets it loads, kept as a literal list so adding
	// a stylesheet to a page is a deliberate act rather than a way to
	// make this test pass. The card pages use the dashboard layout plus
	// cards.css; the issue form is a full-viewport walkthrough with its
	// own sheet over the app shell's tokens.
	pages := []struct {
		path   string
		extra  string
		sheets []string
	}{
		{"cards.templ", "css/cards.css", []string{"css/app-shell.css", "css/dashboard.css", "css/cards.css"}},
		{"card_detail.templ", "css/cards.css", []string{"css/app-shell.css", "css/dashboard.css", "css/cards.css"}},
		{"card_new.templ", "css/walkthrough.css", []string{"css/app-shell.css", "css/walkthrough.css"}},
		{"card_walkthrough.templ", "", []string{"css/app-shell.css", "css/walkthrough.css"}},
		{"card_quick.templ", "css/walkthrough.css", []string{"css/app-shell.css", "css/walkthrough.css"}},
	}

	classRe := regexp.MustCompile(`class="([^"{}]+)"`)
	for _, pg := range pages {
		var css strings.Builder
		for _, s := range pg.sheets {
			b, err := os.ReadFile(filepath.Join("..", "assets", "static", s))
			if err != nil {
				t.Fatalf("read %s: %v", s, err)
			}
			css.Write(b)
			css.WriteString("\n")
		}
		defined := definedClasses(css.String())
		path, extra := pg.path, pg.extra
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		src := string(b)

		if extra != "" && !strings.Contains(src, `"`+extra+`"`) {
			t.Errorf("%s: does not load %s", path, extra)
			continue
		}

		var missing []string
		seen := map[string]bool{}
		for _, m := range classRe.FindAllStringSubmatch(src, -1) {
			for _, c := range strings.Fields(m[1]) {
				if defined[c] || seen[c] {
					continue
				}
				seen[c] = true
				missing = append(missing, c)
			}
		}
		sort.Strings(missing)
		if len(missing) > 0 {
			t.Errorf("%s: uses classes no loaded stylesheet defines: %s\n"+
				"An undefined class does not fail loudly — it renders as unstyled text, "+
				"which on this page turns a warning into a sentence nobody reads.",
				path, strings.Join(missing, ", "))
		}
	}
}

// Cards render inside the app shell, whose light and dark themes expose
// --fg/--fg-muted/--rule. The retired --dash-ink style names have no
// definition there; using one makes the fallback win in both themes and
// can turn the selected tab into black text on the dark background.
func TestCardCSSUsesAppShellThemeTokens(t *testing.T) {
	t.Parallel()

	b, err := os.ReadFile(filepath.Join("..", "assets", "static", "css", "cards.css"))
	if err != nil {
		t.Fatal(err)
	}
	css := string(b)
	// The section tabs are shared with Settings, so they live in
	// dashboard.css, which every dashboard page loads.
	shared, err := os.ReadFile(filepath.Join("..", "assets", "static", "css", "dashboard.css"))
	if err != nil {
		t.Fatal(err)
	}
	for _, stale := range []string{
		"var(--dash-ink",
		"var(--dash-muted",
		"var(--dash-line",
		"var(--dash-border",
		"var(--dash-rail",
		"var(--dash-surface",
	} {
		if strings.Contains(css, stale) {
			t.Errorf("cards.css still uses undefined theme token %s", stale)
		}
	}
	for _, selector := range []string{".ac-inventory-bar a.is-current", ".ac-detail-tabs a.is-current"} {
		re := regexp.MustCompile(regexp.QuoteMeta(selector) + `\s*\{[^}]*color\s*:\s*var\(--fg[,)]`)
		if !re.MatchString(css + string(shared)) {
			t.Errorf("%s does not derive its active label color from --fg", selector)
		}
	}
}

// Card holders are external identifiers and can be materially longer than
// the friendly names used in the mockups. On the Card face (kCard) the
// holder line stays on one line inside the card and ends in an ellipsis
// rather than pushing the action and limit lines out of it.
func TestCardFacesKeepLongRealIdentifiersInsideTheCard(t *testing.T) {
	t.Parallel()

	b, err := os.ReadFile(filepath.Join("..", "assets", "static", "css", "cards.css"))
	if err != nil {
		t.Fatal(err)
	}
	css := string(b)
	agent := regexp.MustCompile(`\.k-agent\s*\{([^}]*)\}`).FindStringSubmatch(css)
	if agent == nil {
		t.Fatal("cards.css has no .k-agent rule")
	}
	for _, rule := range []string{"overflow:hidden", "text-overflow:ellipsis", "white-space:nowrap"} {
		if !strings.Contains(agent[1], rule) {
			t.Errorf(".k-agent is missing %q, so a long holder can leave the card", rule)
		}
	}
	if !regexp.MustCompile(`\.k-lines\s*\{[^}]*min-width:0`).MatchString(css) {
		t.Error(".k-lines needs min-width:0 for its lines to shrink inside the card")
	}
}

// definedClasses collects every class selector in a stylesheet.
// Deliberately crude: it looks for .name in selector position anywhere,
// which over-accepts (a class named only inside a comment counts) and
// never under-accepts. Over-accepting costs a missed warning; the
// alternative, a real CSS parser, is a dependency for a test whose
// whole job is to notice a missing rule.
func definedClasses(css string) map[string]bool {
	out := map[string]bool{}
	for _, m := range regexp.MustCompile(`\.(-?[_a-zA-Z][-_a-zA-Z0-9]*)`).FindAllStringSubmatch(css, -1) {
		out[m[1]] = true
	}
	return out
}
