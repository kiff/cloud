package pages

import (
	"context"
	"regexp"
	"strings"
	"testing"

	"github.com/a-h/templ"
)

// The Catalan legal pages address the reader as vosaltres throughout, as most of each already did: no tu
// possessive or verb, in the pages or in the words they take from Go.
func TestCatalanLegalPagesSayVosaltres(t *testing.T) {
	tu := regexp.MustCompile(`(?i)\b(teu|teva|teus|teves|pots|hagis|escrius|envies|vincules|aprovis|actives|activis|inicies|toques|guardes|suprimeixis|desconnectes)\b`)
	for name, page := range map[string]templ.Component{
		"/ca/privacy": PrivacyCA(), "/ca/tafs/privacy": TafsPrivacyCA(), "/ca/terms": TermsCA(), "/ca/tafs/terms": TafsTermsCA(),
	} {
		var b strings.Builder
		if err := page.Render(context.Background(), &b); err != nil {
			t.Fatal(err)
		}
		if m := tu.FindAllString(b.String(), -1); m != nil {
			t.Errorf("%s says tu: %v", name, m)
		}
		if !strings.Contains(b.String(), "vostr") {
			t.Errorf("%s: no vosaltres at all", name)
		}
	}
}
