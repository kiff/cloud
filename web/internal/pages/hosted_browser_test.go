package pages

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The hosted browser stays off /tafs/privacy while it's offered to test
// accounts only. Flipping hosted_browser.json is the page-first step before
// PLANQUE_BROWSER_TENANTS names any other account.
func TestHostedBrowserIsOffForCustomersAsShipped(t *testing.T) {
	if mustHostedBrowser(hostedBrowserJSON) {
		t.Error("hosted_browser.json says customers: true; keep it false until the hosted browser is offered beyond test accounts")
	}
}

func TestHostedBrowserFlagRefusesAnythingElse(t *testing.T) {
	for _, bad := range []string{`{}`, `{"customers": "yes"}`, `true`, ``} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("%q was accepted", bad)
				}
			}()
			mustHostedBrowser([]byte(bad))
		}()
	}
}

// Every language of /tafs/privacy names its own words for the hosted browser,
// never the English fallback.
func TestEveryPrivacyLanguageHasHostedBrowserWords(t *testing.T) {
	files, err := filepath.Glob("*tafs_privacy.templ")
	if err != nil || len(files) == 0 {
		t.Fatal("no privacy templates found")
	}
	call := regexp.MustCompile(`hostedBrowserWords\("([a-z-]+)"\)`)
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		langs := map[string]bool{}
		for _, m := range call.FindAllStringSubmatch(string(b), -1) {
			langs[m[1]] = true
		}
		if len(langs) != 1 {
			t.Errorf("%s: want one language's hosted browser words, got %v", f, langs)
		}
		for l := range langs {
			w, ok := hostedBrowserText[l]
			if !ok || w.Label == "" || w.Text == "" {
				t.Errorf("%s: no hosted browser words for %q", f, l)
			}
		}
	}
}

// The English paragraph says only what planque does today: no screenshot,
// no pay or order, values of secret fields removed.
func TestHostedBrowserEnglishSaysWhatIsTrue(t *testing.T) {
	w := hostedBrowserText["en"]
	for _, want := range []string{"Ireland", "only on the sites its connection lists", "never stored or logged",
		"deleted when you disconnect it", "the values of password, card and code fields removed",
		"last four digits", "can't pay or place an order", "No screenshot of these pages is kept", "as processor"} {
		if !strings.Contains(w.Text, want) {
			t.Errorf("the English paragraph lacks %q", want)
		}
	}
}

func TestHostedBrowserShowsOnlyWhenOffered(t *testing.T) {
	saved := HostedBrowserForCustomers
	defer func() { HostedBrowserForCustomers = saved }()
	render := func() string {
		var b bytes.Buffer
		if err := TafsPrivacy().Render(context.Background(), &b); err != nil {
			t.Fatal(err)
		}
		return strings.ReplaceAll(b.String(), "&#39;", "'")
	}
	label := "<strong>" + hostedBrowserText["en"].Label + "</strong>"
	HostedBrowserForCustomers = false
	if strings.Contains(render(), label) {
		t.Error("/tafs/privacy describes the hosted browser while it isn't offered to customers")
	}
	HostedBrowserForCustomers = true
	s := render()
	if !strings.Contains(s, label+" "+hostedBrowserText["en"].Text+"</li>") {
		t.Error("/tafs/privacy doesn't describe the hosted browser once it's offered")
	}
	if strings.Index(s, label) < strings.Index(s, "Your browser (Chrome):") {
		t.Error("the hosted browser's item should follow the Chrome item")
	}
}
