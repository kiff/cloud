package pages

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/a-h/templ"
)

// The Mac's security key is said nowhere while its switch is off, and in
// every language's privacy pages and /support §6 once it's on. Not parallel:
// it sets the package's switch.
func TestMacSecurityKeyFollowsItsSwitch(t *testing.T) {
	defer func(was bool) { MacSecurityKeyApprovals = was }(MacSecurityKeyApprovals)
	section := map[string]templ.Component{
		"en": supportComputer(), "de": supportComputerDE(), "es": supportComputerES(), "fr": supportComputerFR(),
		"it": supportComputerIT(), "nl": supportComputerNL(), "pl": supportComputerPL(), "pt": supportComputerPT(),
	}
	render := func(c templ.Component) string {
		var b bytes.Buffer
		if err := c.Render(context.Background(), &b); err != nil {
			t.Fatal(err)
		}
		return strings.ReplaceAll(b.String(), "&#39;", "'")
	}
	langs := append([]string{"en"}, TranslatedLanguages()...)
	for _, on := range []bool{false, true} {
		MacSecurityKeyApprovals = on
		for _, lang := range langs {
			clause := macSecurityKeyClause[lang]
			if clause == "" {
				t.Fatalf("no clause in %s", lang)
			}
			pages := map[string]string{}
			if c, ok := section[lang]; ok { // Korean and Japanese have no /support
				pages["/support §6"] = render(c)
			}
			for _, path := range []string{"/privacy", "/tafs/privacy"} {
				pages[path] = renderPage(t, lang, path)
			}
			for where, html := range pages {
				if got := strings.Count(html, clause); on && got != 1 {
					t.Errorf("%s %s, switch on: the clause appears %d times, want once", lang, where, got)
				} else if !on && got != 0 {
					t.Errorf("%s %s, switch off: the clause appears", lang, where)
				}
			}
		}
	}
}
