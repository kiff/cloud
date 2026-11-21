package pages

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/a-h/templ"
)

// §6 stays dark until a computer's app has a public download, and appears
// the day one does.
func TestSupportComputerWaitsForADownload(t *testing.T) {
	if anyDesktopDownload(DesktopApps()) {
		t.Skip("a computer's app is released: §6 is live")
	}
	for _, lang := range append([]string{"en"}, languagesWith("/support")...) {
		if html := renderPage(t, lang, "/support"); strings.Contains(html, `id="computer"`) {
			t.Errorf("/%s/support shows §6 before any computer's app can be downloaded", lang)
		}
	}
	if anyDesktopDownload([]DesktopApp{{OS: "linux"}, {OS: "macos"}}) {
		t.Error("no URL, yet §6 would show")
	}
	if !anyDesktopDownload([]DesktopApp{{OS: "linux"}, {OS: "windows", URL: "https://example.test/tafs.msix"}}) {
		t.Error("a download, yet §6 would stay dark")
	}
}

// While dark, §6's translations still keep the English section's shape and
// the app's own labels, so turning it on needs no further review.
func TestSupportComputerTranslationsKeepTheEnglishSection(t *testing.T) {
	sections := map[string]templ.Component{
		"de": supportComputerDE(), "es": supportComputerES(), "fr": supportComputerFR(), "it": supportComputerIT(),
		"nl": supportComputerNL(), "pl": supportComputerPL(), "pt": supportComputerPT(),
	}
	render := func(c templ.Component) string {
		var b bytes.Buffer
		if err := c.Render(context.Background(), &b); err != nil {
			t.Fatal(err)
		}
		return b.String()
	}
	en := shapeOf(render(supportComputer()), "en")
	for _, want := range []string{"Add this computer with your phone", "Account", "Devices", "Add a device", "Show a new code"} {
		if !strings.Contains(render(supportComputer()), want) {
			t.Errorf("English §6 is missing the app's %q", want)
		}
	}
	for _, lang := range languagesWith("/support") {
		c, ok := sections[lang]
		if !ok {
			t.Errorf("no §6 in %s", lang)
			continue
		}
		tr := shapeOf(render(c), lang)
		if d := firstDiff(en.ids, tr.ids); d != "" {
			t.Errorf("%s: anchors differ %s", lang, d)
		}
		if d := firstDiff(en.blocks, tr.blocks); d != "" {
			t.Errorf("%s: structure differs %s", lang, d)
		}
		if a, b := multisetDiff(en.figures, tr.figures); len(a)+len(b) > 0 {
			t.Errorf("%s: figures differ: %v, %v", lang, a, b)
		}
	}
}
