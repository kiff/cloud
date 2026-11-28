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
	for _, bad := range []string{`{}`, `{"customers": "yes"}`, `{"customers": false}`, `{"customers": false, "pictures": false, "downloads": false}`, `true`, ``} {
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

// Each language's no-screenshot sentence is the one its paragraph says, once,
// so a switched-on feature swaps exactly that sentence and nothing else.
func TestEveryNoScreenshotSentenceIsInItsParagraph(t *testing.T) {
	for lang, w := range hostedBrowserText {
		ns, ok := hostedBrowserNoScreenshot[lang]
		if !ok || ns == "" {
			t.Errorf("%s: no no-screenshot sentence", lang)
			continue
		}
		if n := strings.Count(w.Text, ns); n != 1 {
			t.Errorf("%s: the no-screenshot sentence is in its paragraph %d times, want once", lang, n)
		}
	}
	for lang := range hostedBrowserNoScreenshot {
		if _, ok := hostedBrowserText[lang]; !ok {
			t.Errorf("%s: a no-screenshot sentence for a language with no paragraph", lang)
		}
	}
}

// As shipped every feature switch is off, and the paragraph is today's, word
// for word.
func TestBrowserSwitchesAreOffAsShippedAndChangeNothing(t *testing.T) {
	if hostedBrowserSwitches.Pictures || hostedBrowserSwitches.Downloads || hostedBrowserSwitches.Health {
		t.Errorf("hosted_browser.json switches a feature on: %+v; each waits for its words in every language and the owner's yes", hostedBrowserSwitches)
	}
	for lang, w := range hostedBrowserText {
		if got := hostedBrowserWords(lang).Text; got != w.Text {
			t.Errorf("%s: with every switch off the paragraph changed", lang)
		}
	}
}

// A switch can be true only when every language has its words, so no page
// ever says it in English alone, or says nothing.
func TestEveryBrowserSwitchOnHasItsWords(t *testing.T) {
	for lang := range hostedBrowserText {
		on := hostedBrowserOn[lang]
		need := map[string]string{}
		if hostedBrowserSwitches.Pictures {
			need["pictures"], need["kept"] = on.Pictures, on.Kept
		}
		if hostedBrowserSwitches.Downloads {
			need["downloads"], need["kept"] = on.Downloads, on.Kept
		}
		if hostedBrowserSwitches.Health {
			need["health"] = on.Health
		}
		for what, words := range need {
			if words == "" {
				t.Errorf("%s: %s is switched on with no words", lang, what)
			}
		}
	}
}

// What each switch swaps in, and what it leaves: pictures replace the
// no-screenshot sentence; downloads and the sign-in check add to it.
func TestBrowserSwitchesSwapOnlyTheirSentence(t *testing.T) {
	saved := hostedBrowserOn["en"]
	defer func() { hostedBrowserOn["en"] = saved }()
	hostedBrowserOn["en"] = hostedBrowserOnItem{Pictures: "P.", Downloads: "D.", Kept: "K.", Health: "H."}
	base := hostedBrowserText["en"].Text
	ns := hostedBrowserNoScreenshot["en"]
	for _, c := range []struct {
		sw   browserSwitches
		want string
	}{
		{browserSwitches{Pictures: true}, "P. K."},
		{browserSwitches{Downloads: true}, ns + " D. K."},
		{browserSwitches{Health: true}, ns + " H."},
		{browserSwitches{Pictures: true, Downloads: true, Health: true}, "P. D. K. H."},
	} {
		if got := withBrowserSwitches(base, "en", c.sw); got != strings.Replace(base, ns, c.want, 1) {
			t.Errorf("%+v: got %q", c.sw, got)
		}
	}
}
