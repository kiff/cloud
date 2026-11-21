package pages

import (
	"bytes"
	"context"
	"fmt"
	"html"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/kiff/cloud/web/internal/components"
)

// renderPage renders a translated page as a request for its own path
// would, so the layout knows its language.
func renderPage(t *testing.T, lang, path string) string {
	t.Helper()
	page, ok := Translation(lang, path)
	if !ok {
		t.Fatalf("%s %s: no page", lang, path)
	}
	ctx := components.WithCanonicalPath(context.Background(), components.LangPath(lang, path))
	var b bytes.Buffer
	if err := page(OfferLinks{AppURL: "https://apps.example/tafs", PlayURL: "https://play.example/tafs"}).Render(ctx, &b); err != nil {
		t.Fatalf("%s %s: %v", lang, path, err)
	}
	return b.String()
}

var (
	mainRE    = regexp.MustCompile(`(?s)<main[^>]*>(.*)</main>`)
	langRowRE = regexp.MustCompile(`(?s)<nav class="lang-row".*?</nav>`)
	noticeRE  = regexp.MustCompile(`(?s)<p class="translation-notice">.*?</p>`)
	tagRE     = regexp.MustCompile(`<([a-z][a-z0-9]*)\b([^>]*)>`)
	idRE      = regexp.MustCompile(`\bid="([^"]*)"`)
	hrefRE    = regexp.MustCompile(`\bhref="([^"]*)"`)
	scriptRE  = regexp.MustCompile(`(?s)<(script|style)\b.*?</(script|style)>`)
	anyTagRE  = regexp.MustCompile(`<[^>]+>`)
	figureRE  = regexp.MustCompile(`\d+(?:[.,\x{00a0}\x{202f} ]\d+)*`)
)

// blockTags are the elements that give a page its shape. A translation
// keeps every one of them, in order: a missing list item or table row is
// a clause left out. Inline elements (em, strong, br, span) may differ,
// since a language may stress or break a line differently.
var blockTags = map[string]bool{
	"section": true, "article": true, "header": true, "footer": true, "h1": true, "h2": true, "h3": true,
	"h4": true, "h5": true, "h6": true, "p": true, "ul": true, "ol": true, "li": true, "table": true,
	"thead": true, "tbody": true, "tr": true, "th": true, "td": true, "dl": true, "dt": true, "dd": true,
	"details": true, "summary": true, "blockquote": true, "pre": true, "figure": true, "address": true,
}

// cjkDateRE and cjkMonthRE are a full date and a month with its year, as
// Korean and Japanese write them (see shapeOf).
var (
	cjkDateRE  = regexp.MustCompile(`(\d{4})\s*[년年]\s*(\d{1,2})\s*[월月]\s*(\d{1,2})\s*[일日]`)
	cjkMonthRE = regexp.MustCompile(`(\d{4})\s*[년年]\s*\d{1,2}\s*[월月]`)
)

// shape is what a page's translation must share with its English source.
type shape struct {
	ids     []string // every id, in order: the anchors people are sent to
	blocks  []string // every block element, in order
	links   []string // every link target, sorted, with the language prefix taken off
	code    int      // <code> spans: names and values that are never translated
	figures []string // every number in the text, sorted, written without separators
}

// shapeOf reads a rendered page's shape. The language row and the
// translation notice are the layout's, not the page's, so they are left out.
func shapeOf(page, lang string) shape {
	m := mainRE.FindStringSubmatch(page)
	body := page
	if m != nil {
		body = m[1]
	}
	body = langRowRE.ReplaceAllString(body, "")
	body = noticeRE.ReplaceAllString(body, "")
	body = scriptRE.ReplaceAllString(body, "")
	var s shape
	for _, t := range tagRE.FindAllStringSubmatch(body, -1) {
		name, attrs := t[1], t[2]
		if id := idRE.FindStringSubmatch(attrs); id != nil {
			s.ids = append(s.ids, id[1])
		}
		if blockTags[name] {
			s.blocks = append(s.blocks, name)
		}
		if name == "code" {
			s.code++
		}
		if name == "a" {
			if h := hrefRE.FindStringSubmatch(attrs); h != nil {
				s.links = append(s.links, strings.TrimPrefix(h[1], "/"+lang+"/"))
			}
		}
	}
	for i, l := range s.links {
		// "/de/privacy#tafs" lost its prefix as "privacy#tafs": put the slash back.
		if !strings.Contains(l, ":") && !strings.HasPrefix(l, "/") && !strings.HasPrefix(l, "#") {
			s.links[i] = "/" + l
		}
	}
	sort.Strings(s.links)
	// Entities are decoded first: an escaped apostrophe (&#39;) is no figure.
	text := html.UnescapeString(anyTagRE.ReplaceAllString(body, " "))
	// A date in Korean or Japanese form ("2026년 11월 21일", "2026年11月21日")
	// names its month with a figure, where English names it with a word
	// ("21 November 2026"): read it as the English date's figures, the day and
	// the year, so the month isn't an extra figure.
	text = cjkDateRE.ReplaceAllString(text, "$3 $1")
	text = cjkMonthRE.ReplaceAllString(text, "$1")
	for _, f := range figureRE.FindAllString(text, -1) {
		digits := strings.Map(func(r rune) rune {
			if r >= '0' && r <= '9' {
				return r
			}
			return -1
		}, f)
		// "3 000" and "3,000" are one figure, but "9 November 2026" is two:
		// a group after a separator is part of the figure only when it has
		// three digits, as a thousands group does, or follows a decimal mark.
		parts := regexp.MustCompile(`[.,\x{00a0}\x{202f} ]`).Split(f, -1)
		joined := true
		for _, p := range parts[1:] {
			if len(p) != 3 && !(len(parts) == 2 && strings.ContainsAny(f, ".,")) {
				joined = false
			}
		}
		if joined {
			s.figures = append(s.figures, digits)
		} else {
			s.figures = append(s.figures, parts...)
		}
	}
	sort.Strings(s.figures)
	return s
}

// firstDiff says where two lists part, for a message a translator can act on.
func firstDiff(a, b []string) string {
	for i := 0; i < len(a) || i < len(b); i++ {
		var x, y string
		if i < len(a) {
			x = a[i]
		}
		if i < len(b) {
			y = b[i]
		}
		if x != y {
			from := i - 3
			if from < 0 {
				from = 0
			}
			return fmt.Sprintf("at %d: English %q, translation %q (after %v)", i, x, y, a[from:i])
		}
	}
	return ""
}

// multisetDiff lists what one sorted list has that the other lacks.
func multisetDiff(a, b []string) (onlyA, onlyB []string) {
	count := map[string]int{}
	for _, x := range a {
		count[x]++
	}
	for _, y := range b {
		count[y]--
	}
	for k, n := range count {
		for ; n > 0; n-- {
			onlyA = append(onlyA, k)
		}
		for ; n < 0; n++ {
			onlyB = append(onlyB, k)
		}
	}
	sort.Strings(onlyA)
	sort.Strings(onlyB)
	return
}

// TestTranslationsKeepTheEnglishPage holds every translated page to its
// English source: the same anchors in the same order, the same block
// structure, the same links, the same figures. Nothing added and nothing
// left out is what this checks by machine; the wording is read by a person.
// A later change to an English page fails here until its translations follow.
func TestTranslationsKeepTheEnglishPage(t *testing.T) {
	for _, lang := range TranslatedLanguages() {
		for _, path := range components.TranslatedPages {
			if _, ok := translations[lang][path]; !ok {
				continue
			}
			t.Run(lang+path, func(t *testing.T) {
				en := shapeOf(renderPage(t, "en", path), "en")
				html := renderTranslation(t, lang, path)
				tr := shapeOf(html, lang)
				if d := firstDiff(en.ids, tr.ids); d != "" {
					t.Errorf("anchors differ %s", d)
				}
				if d := firstDiff(en.blocks, tr.blocks); d != "" {
					t.Errorf("structure differs %s", d)
				}
				if a, b := multisetDiff(en.links, tr.links); len(a)+len(b) > 0 {
					t.Errorf("links differ: only in English %v, only in the translation %v", a, b)
				}
				if en.code != tr.code {
					t.Errorf("<code> spans: English %d, translation %d", en.code, tr.code)
				}
				if a, b := multisetDiff(en.figures, tr.figures); len(a)+len(b) > 0 {
					t.Errorf("figures differ: only in English %v, only in the translation %v", a, b)
				}
				// A link to a page that has this language goes to it, never to English.
				for _, h := range hrefRE.FindAllStringSubmatch(langRowRE.ReplaceAllString(noticeRE.ReplaceAllString(mainOf(html), ""), ""), -1) {
					page := h[1]
					if i := strings.IndexAny(page, "#?"); i >= 0 {
						page = page[:i]
					}
					if components.HasPage(lang, page) && !HeldBackTranslations[page] {
						t.Errorf("link %q goes to the English page: write %q", h[1], components.LangPath(lang, h[1]))
					}
				}
				// A link to an English-only page the reader may follow says so,
				// and so does one to a page this language doesn't have.
				englishOnly := []string{"/pricing", "/whitepaper", "/legal/sla"}
				for _, p := range components.TranslatedPages {
					if !components.HasPage(lang, p) {
						englishOnly = append(englishOnly, p)
					}
				}
				for _, only := range englishOnly {
					n := strings.Count(mainOf(html), `href="`+only+`"`) + strings.Count(mainOf(html), `href="`+only+`#`)
					if n > 0 && strings.Count(html, components.InEnglish(lang)) < n {
						t.Errorf("%d link(s) to %s, an English page: each is followed by %q", n, only, components.InEnglish(lang))
					}
				}
				if !strings.Contains(html, `<html lang="`+lang+`"`) {
					t.Errorf("the page isn't marked as %s", lang)
				}
				if !components.HasNoNotice(path) && !strings.Contains(html, components.TranslationNoticeText(lang)) {
					t.Errorf("no notice that the English version governs")
				}
			})
		}
	}
}

// languagesWith lists the translated languages that have path
// (components.HasPage): every site language, and a language that has some
// pages only where it lists path. A site language that lacks it fails
// TestEveryTranslatedLanguageHasEveryPage.
func languagesWith(path string) []string {
	var out []string
	for _, lang := range TranslatedLanguages() {
		if components.HasPage(lang, path) {
			out = append(out, lang)
		}
	}
	return out
}

func mainOf(html string) string {
	if m := mainRE.FindStringSubmatch(html); m != nil {
		return m[1]
	}
	return html
}

// renderTranslation renders a translation whether or not it is held back.
func renderTranslation(t *testing.T, lang, path string) string {
	t.Helper()
	page := translations[lang][path]
	ctx := components.WithCanonicalPath(context.Background(), components.LangPath(lang, path))
	var b bytes.Buffer
	if err := page(OfferLinks{AppURL: "https://apps.example/tafs", PlayURL: "https://play.example/tafs"}).Render(ctx, &b); err != nil {
		t.Fatalf("%s %s: %v", lang, path, err)
	}
	return b.String()
}

// TestTheAnchorsTheAppsLinkToExist: the apps open /terms#tafs and
// /privacy#tafs, and the store listing /privacy#delete-account.
func TestTheAnchorsTheAppsLinkToExist(t *testing.T) {
	for path, ids := range map[string][]string{"/terms": {"tafs"}, "/privacy": {"tafs", "delete-account"}} {
		html := renderPage(t, "en", path)
		for _, id := range ids {
			if !strings.Contains(html, `id="`+id+`"`) {
				t.Errorf("%s has no #%s", path, id)
			}
		}
	}
}

// TestEveryTranslatedLanguageHasEveryPage: a site language is every
// translated page or none, so a reader never falls from one language into
// another; a language that has some of them (Korean, Japanese: the legal texts) has
// each one it lists, and links to the rest as English pages.
func TestEveryTranslatedLanguageHasEveryPage(t *testing.T) {
	for _, lang := range TranslatedLanguages() {
		for _, path := range components.TranslatedPages {
			if !components.HasPage(lang, path) {
				continue
			}
			if _, ok := translations[lang][path]; !ok {
				t.Errorf("%s has no %s", lang, path)
			}
		}
	}
}

// TestTheShapeReaderSeesWhatMatters checks the checker on a small page.
func TestTheShapeReaderSeesWhatMatters(t *testing.T) {
	en := shapeOf(`<main><section id="a"><h2>Fees</h2><p>Up to €1,240.50 on 9 November 2026, see <a href="/privacy#tafs">privacy</a> and <code>x</code>.</p><ul><li>30 days</li></ul></section></main>`, "en")
	de := shapeOf(`<main><nav class="lang-row"><a href="/privacy">English</a></nav><section id="a"><h2>Gebühren</h2><p>Bis zu 1.240,50 € am 9. November 2026, siehe <a href="/de/privacy#tafs">Datenschutz</a> und <code>x</code>.</p><ul><li>30 Tage</li></ul></section></main>`, "de")
	if d := firstDiff(en.blocks, de.blocks); d != "" {
		t.Errorf("blocks: %s", d)
	}
	if a, b := multisetDiff(en.links, de.links); len(a)+len(b) > 0 {
		t.Errorf("links: %v %v", a, b)
	}
	if a, b := multisetDiff(en.figures, de.figures); len(a)+len(b) > 0 {
		t.Errorf("figures: %v %v (en %v, de %v)", a, b, en.figures, de.figures)
	}
	// Korean and Japanese write a date's month as a figure: it reads as the English date.
	for lang, page := range map[string]string{
		"ko": `<main><section id="a"><h2>요금</h2><p>2026년 11월 9일에 최대 €1,240.50, <a href="/ko/privacy#tafs">개인정보</a> 및 <code>x</code>.</p><ul><li>30일</li></ul></section></main>`,
		"ja": `<main><section id="a"><h2>料金</h2><p>2026年11月9日に最大€1,240.50、<a href="/ja/privacy#tafs">プライバシー</a>と<code>x</code>。</p><ul><li>30日</li></ul></section></main>`,
		"et": `<main><section id="a"><h2>Tasud</h2><p>Kuni 1 240,50 € 9. novembril 2026, vt <a href="/et/privacy#tafs">privaatsus</a> ja <code>x</code>.</p><ul><li>30 päeva</li></ul></section></main>`, // a month's name: no extra figure
	} {
		if a, b := multisetDiff(en.figures, shapeOf(page, lang).figures); len(a)+len(b) > 0 {
			t.Errorf("%s date: figures %v %v", lang, a, b)
		}
	}
	short := shapeOf(`<main><section id="a"><h2>Gebühren</h2><p>Bis zu 1.240 €.</p></section></main>`, "de")
	if firstDiff(en.blocks, short.blocks) == "" {
		t.Errorf("a dropped list wasn't seen")
	}
	if a, _ := multisetDiff(en.figures, short.figures); len(a) == 0 {
		t.Errorf("a dropped figure wasn't seen")
	}
}
