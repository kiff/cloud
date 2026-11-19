package pages

import (
	"html"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/kiff/cloud/web/internal/components"
)

// tafsByLang is /tafs's words in each language it's served in.
var tafsByLang = map[string]tafsWords{
	"en": tafsEN, "de": tafsDE, "fr": tafsFR, "es": tafsES, "it": tafsIT, "pt": tafsPT, "nl": tafsNL, "pl": tafsPL,
}

// emptyWords lists the words left empty in v, by their path, and each list
// of stickers whose length differs from English's.
func emptyWords(v, en reflect.Value, path string) []string {
	var out []string
	switch v.Kind() {
	case reflect.String:
		if v.String() == "" {
			out = append(out, path)
		}
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			out = append(out, emptyWords(v.Field(i), en.Field(i), path+"."+v.Type().Field(i).Name)...)
		}
	case reflect.Array, reflect.Slice:
		if v.Len() != en.Len() {
			out = append(out, path+" (length)")
		}
		for i := 0; i < v.Len() && i < en.Len(); i++ {
			out = append(out, emptyWords(v.Index(i), en.Index(i), path)...)
		}
	}
	return out
}

// /tafs in every language: every word its own, the same screens, today's
// prices as the language writes them, the links, and the footer's line.
func TestTafsInEveryLanguage(t *testing.T) {
	langs := append([]string{"en"}, TranslatedLanguages()...)
	if len(langs) != len(tafsByLang) {
		t.Fatalf("/tafs is served in %d languages, and %d have words", len(langs), len(tafsByLang))
	}
	for _, lang := range langs {
		w, ok := tafsByLang[lang]
		if !ok {
			t.Fatalf("%s has no words", lang)
		}
		t.Run(lang, func(t *testing.T) {
			if w.lang() != lang {
				t.Fatalf("its money is written as %q's", w.lang())
			}
			// English's empty fields are the ones no screen uses.
			for _, p := range emptyWords(reflect.ValueOf(w), reflect.ValueOf(tafsEN), "") {
				if !slices.Contains(emptyWords(reflect.ValueOf(tafsEN), reflect.ValueOf(tafsEN), ""), p) {
					t.Errorf("%s is empty", p)
				}
			}
			raw := renderPage(t, lang, "/tafs")
			body := html.UnescapeString(raw)
			if !strings.Contains(raw, `<html lang="`+lang+`"`) {
				t.Errorf("the page isn't marked as %s", lang)
			}
			if n := strings.Count(raw, "<h1"); n != 1 {
				t.Errorf("%d h1s, want one", n)
			}
			for _, s := range []string{
				w.H1, w.H1Em, w.Lede, w.Jobs[0], w.Taf.Big, w.Problem.Big, w.Problem.Giant, w.DoesLines[2][1],
				w.Different.Big, w.Card.Question, w.Names.Big, w.Glosses[1], w.Why.Big, w.Why.Giant,
				w.Price.Big, w.priceLine(), w.topUp(), w.Price.Stickers[3], w.Cost.money(TafsSoloCents),
				w.Get.Big, w.Get.Small, w.Get.Stickers[1], w.Tagline,
				// Its head: the title, description and share tags say what tafs is.
				"<title>" + w.Title + "</title>", `<meta name="description" content="` + w.Description + `">`,
				`property="og:title" content="` + w.Title + `"`, `property="og:description" content="` + w.Description + `"`,
				`property="og:type" content="website"`, `property="og:url" content="https://kiff.dev` + components.LangPath(lang, "/tafs") + `"`,
				`property="og:image" content="https://kiff.dev/static/img/og/kiff-og.png"`,
				// The Google band, and its link to tafs's privacy policy's Google section.
				`<h2 id="google-h">` + w.Google.Head + "</h2>", w.Google.Text,
				`<a href="` + string(w.legalPath(tafsGoogleURL)) + `">` + w.Google.Link + " →</a>" + w.legalInEnglish(tafsGoogleURL) + "</p>",
				// tafs's own documents, in the footer's Legal column, in this
				// language where they are served in it.
				`<a href="` + string(w.legalPath("/tafs/privacy")) + `">` + w.TafsPrivacy + "</a>" + w.legalInEnglish("/tafs/privacy"),
				`<a href="` + string(w.legalPath("/tafs/terms")) + `">` + w.TafsTerms + "</a>" + w.legalInEnglish("/tafs/terms"),
				"Part of Claude for Startups.</span> " + w.Cost.NotAffiliated,
				// The three words stay French in every language.
				">taf<", ">planque<", ">kiff<",
				// The screens people are sent to, and the links.
				`id="top"`, `id="pricing"`, `id="download"`, `href="#pricing"`, `href="#download"`,
				`href="https://play.example/tafs"`, `href="https://github.com/kiff/tafs"`,
				`href="/docs"`, `href="/whitepaper"`, `href="mailto:support@kiff.dev"`,
				`href="` + components.LangPath(lang, "/security") + `"`, `href="` + components.LangPath(lang, "/privacy") + `"`,
				`href="` + components.LangPath(lang, "/terms") + `"`,
			} {
				if !strings.Contains(body, s) {
					t.Errorf("missing %q", s)
				}
			}
			// The Google band opens the footer, open (never in a fold), and is
			// nowhere else: the first screen keeps to its words (owner, 18 Nov).
			foot, band, row1 := strings.Index(body, `<footer class="kf">`), strings.Index(body, `id="google"`), strings.Index(body, `class="row1"`)
			if strings.Count(body, `id="google"`) != 1 || !(foot < band && band < row1) {
				t.Errorf("the Google band doesn't open the footer")
			}
			if strings.Contains(body[foot:], "<details") {
				t.Errorf("the footer folds something away; the Google band must be open")
			}
			if hero := body[strings.Index(body, `id="top"`):strings.Index(body, `<section class="p taf">`)]; strings.Contains(hero, w.Google.Head) {
				t.Errorf("the Google band is back on the first screen")
			}
			// What a Card is, as the example Card's caption.
			if !strings.Contains(body, `<p class="kcap"><b>`+w.Card.Label+`</b> `) || !strings.Contains(body, w.CardLine) {
				t.Errorf("the example Card has no caption")
			}
			if strings.Contains(body, `class="gdata"`) || strings.Contains(body, "/privacy#google") && !strings.Contains(body, "/tafs/privacy#google") {
				t.Errorf("the footer's Google block is back")
			}
			// Every language's page links to every language's, in the stickers and the footer.
			for _, l := range components.SiteLanguages {
				if n := strings.Count(raw, `href="`+components.LangPath(l, "/tafs")+`"`); n != 2 {
					t.Errorf("%d links to %s's page, want 2", n, l)
				}
			}
			if n := strings.Count(raw, `aria-current="page"`); n != 2 {
				t.Errorf("%d links marked as this page, want 2", n)
			}
			// The App Store line is no link until the app is there.
			if regexp.MustCompile(`<a[^>]*>[^<]*` + regexp.QuoteMeta(w.Get.Stickers[1])).MatchString(body) {
				t.Errorf("the App Store line is a link")
			}
			// "Get it" leads to every computer's download, not the phones alone.
			if !regexp.MustCompile(`<a[^>]*href="` + regexp.QuoteMeta(string(w.path("/tafs/download"))) + `"[^>]*>\s*<span>` + regexp.QuoteMeta(w.Get.Stickers[3])).MatchString(raw) {
				t.Errorf("\"Get it\" doesn't link the computers' downloads")
			}
			lower := strings.ToLower(body)
			for _, never := range []string{
				// The three words are French, never "French slang", in any language.
				"slang", "argot", "jerga", "gergo", "gíria", "calão", "żargon",
				// Fonts are the site's own.
				"fonts.googleapis.com", "fonts.gstatic.com",
			} {
				if strings.Contains(lower, never) {
					t.Errorf("says %q", never)
				}
			}
			if lang == "en" {
				return
			}
			// Nothing of the English page is left in a translation.
			for _, en := range []string{tafsEN.H1, tafsEN.Lede, tafsEN.Taf.Big, tafsEN.Different.Small, tafsEN.priceLine(), tafsEN.Tagline, tafsEN.Glosses[2],
				tafsEN.Google.Text, tafsEN.Google.Link} {
				if strings.Contains(body, en) {
					t.Errorf("English left in: %q", en)
				}
			}
		})
	}
}

// The English page says what the approved design says, word for word, with
// its prices from the plans and its models from the app.
func TestTafsEnglishAsDesigned(t *testing.T) {
	body := html.UnescapeString(renderPage(t, "en", "/tafs"))
	for _, s := range []string{
		"The assistant that runs <em>your company's routine work.</em>",
		"Across the apps you already use. You set its limits, the actions that matter wait for your yes, and the AI never sees your passwords or keys.",
		"Acknowledge every support ticket", "Save a customer who cancels", "Customer replies",
		"€12.50 a month to start, with 500 credits, VAT included. Runs on Claude Sonnet 5.5 and Opus 5.5.",
		">€12.50<", "Top up<br>from €11.50", "Heavy use<br>~€160 / month",
		"No answer means no.", "Three French words", "Hand over the taf. Keep the keys in the planque. Kiff.",
		"App Store, coming soon", "We believe in open code. Read it on GitHub.",
		"<title>tafs: AI agents that do your routine work</title>",
		// What tafs does with a Google account, as Google's review of the
		// sign-in asks the home page to say, never more than the app does.
		`<h2 id="google-h">What tafs does with your Google account</h2>`,
		"When you connect a Google app, a taf uses it only within its Card.",
		// Google's automated branding check reads this line on the home page,
		// in Google's own terms; /tafs/privacy §6 says the same.
		"tafs doesn't create or edit images or video: its AI writes text. It never uses Google APIs or Google user data to create, edit or share intimate images of anyone (non-consensual intimate imagery, NCII), or any other image of a person.",
		`<a href="/tafs/privacy#google">How we handle Google data →</a>`,
		`<a href="/tafs/privacy">tafs privacy</a>`, `<a href="/tafs/terms">tafs terms</a>`,
		"© ", " KIFF · <span lang=\"en\">Part of Claude for Startups.</span> KIFF is not affiliated with or endorsed by Anthropic.",
	} {
		if !strings.Contains(body, s) {
			t.Errorf("missing %q", s)
		}
	}
	// A taf sends within its Card, so the page never says nothing is sent without you.
	if strings.Contains(strings.ToLower(body), "nothing is sent without") {
		t.Errorf("the page says nothing is sent without your yes")
	}
}

// A giant word is sized by its width, so a long word in any language fits.
func TestTafsFit(t *testing.T) {
	short, long := tafsFit("taf")["style"].(string), tafsFit("Nicht angreifbar.")["style"].(string)
	if short != "--n:1.98;--nw:1.98" || long != "--n:9.90;--nw:6.60" {
		t.Errorf("fit: %q, %q", short, long)
	}
	if got := tafsRing(strings.Repeat("x", 60), 360)["lengthAdjust"]; got != "spacingAndGlyphs" {
		t.Errorf("a long ring is %v, want squeezed", got)
	}
}

// Google's branding review asks the home page to say, plainly, that the app
// doesn't use Google APIs for non-consensual intimate imagery: every
// language's Google band names both, in Google's terms.
func TestTafsGoogleBandSaysItMakesNoImages(t *testing.T) {
	want := map[string][2]string{
		"en": {"Google APIs", "(non-consensual intimate imagery, NCII)"},
		"de": {"Google-APIs", "(nicht einvernehmliche intime Bilder, NCII)"},
		"fr": {"API Google", "(images intimes non consenties, NCII)"},
		"es": {"API de Google", "(imágenes íntimas no consentidas, NCII)"},
		"it": {"API di Google", "(immagini intime non consensuali, NCII)"},
		"nl": {"Google-API's", "(intieme beelden zonder toestemming, NCII)"},
		"pl": {"API Google", "(intymne obrazy bez zgody, NCII)"},
		"pt": {"APIs do Google", "(imagens íntimas não consentidas, NCII)"},
	}
	for lang, w := range tafsByLang {
		line, ok := want[lang]
		if !ok {
			t.Errorf("%s: no images line to check", lang)
			continue
		}
		for _, s := range line {
			if !strings.Contains(w.Google.Text, s) {
				t.Errorf("%s: the Google band leaves out %q: %q", lang, s, w.Google.Text)
			}
		}
	}
}
