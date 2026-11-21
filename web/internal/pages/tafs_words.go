package pages

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/a-h/templ"
	"github.com/kiff/cloud/web/internal/components"
)

// /tafs, the product page (owner-approved design, 13 Nov 2026): nine
// full-colour screens, each one giant word and its stickers, then the kiff
// footer. One page, rendered by tafsPage in each of the eight languages tafs
// speaks: a language is its words below, never its own markup, so every
// language has the same screens, anchors and figures.

// tafsScreen is one screen's words. Not every screen uses every field.
type tafsScreen struct {
	// Big is the screen's heading, Small the line under it.
	Big, Small string
	// Giant is the screen's giant word; Idx names the screen in its corner.
	Giant, Idx string
	// Ring runs around the screen's round badge.
	Ring string
	// Stickers are the screen's stickers, in the order the page places them.
	Stickers []string
}

// tafsCard is the example Card and the approval it asks for, on screen 5.
type tafsCard struct {
	// Label names the Card and the approval for a screen reader.
	Label, AskLabel string
	Title, Signed   string
	// Rows are the Card's three lines: its label (Does, Asks, Never), then
	// the line.
	Rows [3][2]string
	// The approval: its kicker, the question, why it asks, and the answers.
	Needs, Question, Why, No, Yes string
}

// tafsWords are the page's words in one language.
type tafsWords struct {
	// Cost writes money and numbers as the language does, and has the
	// not-affiliated line (tafs_cost.go).
	Cost tafsCostWords

	Title, Description string
	// The header and the page's chrome.
	Skip, Menu, Languages, Pricing, Download, Scroll string

	// 1: the hero. Jobs are its four stickers; YesNo is the badge's word.
	H1, H1Em, Lede, HeroRing, YesNo string
	Jobs                            [4]string

	// 2: what a taf is. Stickers: the tag's two lines.
	Taf tafsScreen
	// 3: the problem. Stickers: the tag, then the pill; Stop is the badge's word.
	Problem tafsScreen
	Stop    string
	// 4: what it does, in three lines: each a lead and its coloured word.
	Does      tafsScreen
	DoesLines [3][2]string
	// 5: how it's different, with the example Card.
	Different tafsScreen
	Card      tafsCard
	// CardLine says what a Card is: the caption above the example Card.
	CardLine string
	// 6: the names. Big is the kicker; Stickers: the pill. Glosses say what
	// taf, planque and kiff mean, in that order. The three words stay.
	Names   tafsScreen
	Glosses [3]string
	// 7: why we exist. Stickers: the four pills.
	Why tafsScreen
	// 8: the price. Small takes Solo's price, its credits, then the two
	// models. Stickers: the top-up tag's two lines (the second takes the
	// smallest pack's price), then heavy use's two.
	Price tafsScreen
	// 9: the app. Stickers: Google Play's kicker, the App Store's line, the
	// GitHub line, and the line to every computer's download (/tafs/download).
	Get tafsScreen

	// The footer: its line, its three column heads, and the links.
	// TafsPrivacy and TafsTerms name tafs's own documents.
	Tagline, Legal                                       string
	OpenCode, Docs, Security, Whitepaper, Privacy, Terms string
	TafsPrivacy, TafsTerms                               string
	// Google is the footer's band on what tafs does with a connected Google
	// account, for Google's review of the sign-in: open on every language's
	// page, never in a fold, and off the first screen (owner, 18 Nov 2026).
	Google tafsGoogle
}

// tafsGoogle is the footer's Google band: its heading, its line, and the
// link to /tafs/privacy#google. Every fact in Text is true of the app today:
// a taf uses a connected Google app only within its Card, and tafs's AI
// writes text and makes no image of anyone. The image line is in Google's
// own terms (non-consensual intimate imagery, NCII), as Google's branding
// check asks of the home page, and is the same line as /tafs/privacy's §6.
type tafsGoogle struct {
	Head, Text, Link string
}

// lang is the language's code: "en", "de", ...
func (w tafsWords) lang() string { return w.Cost.Lang }

// inEnglish marks a link to a page only in English, on a translated page.
func (w tafsWords) inEnglish() string {
	if w.lang() == "en" {
		return ""
	}
	return " " + components.InEnglish(w.lang())
}

// path is a page's path in this language, or as it is if it's English only.
func (w tafsWords) path(p string) templ.SafeURL {
	return templ.SafeURL(components.LangPath(w.lang(), p))
}

// tafsGoogleURL is tafs's privacy policy's Google section, in English.
const tafsGoogleURL = "/tafs/privacy#google"

// legalPath is one of tafs's own documents (its privacy policy or terms,
// with any anchor) in this language where it is served in it, else in
// English. legalInEnglish is the marker that then follows its link.
func (w tafsWords) legalPath(p string) templ.SafeURL {
	if w.legalServed(p) {
		return w.path(p)
	}
	return templ.SafeURL(p)
}

func (w tafsWords) legalInEnglish(p string) string {
	if w.legalServed(p) {
		return ""
	}
	return w.inEnglish()
}

func (w tafsWords) legalServed(p string) bool {
	page := p
	if i := strings.IndexAny(page, "#?"); i >= 0 {
		page = page[:i]
	}
	return components.TranslationServed(w.lang(), page)
}

// priceLine is screen 8's line: Solo's price and credits, and the model (Coach and tafs both run on it).
func (w tafsWords) priceLine() string {
	return fmt.Sprintf(w.Price.Small, w.Cost.money(TafsSoloCents), w.Cost.number(TafsSoloCredits), TafsWorkModel)
}

// topUp is the top-up tag's second line, with the smallest pack's price.
func (w tafsWords) topUp() string {
	return fmt.Sprintf(w.Price.Stickers[1], w.Cost.money(TafsPackCents[TafsSoloCredits]))
}

// year is the footer's copyright year.
func tafsYear() string { return fmt.Sprint(time.Now().Year()) }

// tafsFit sizes a giant word to the screen: its width in ems, all of it on
// one line (--n) and its widest word (--nw), as the stylesheet reads them,
// so a long word in any language fits instead of running off the screen.
func tafsFit(lines ...string) templ.Attributes {
	var n, nw float64
	for _, s := range lines {
		line := 0.0
		for _, word := range strings.Fields(s) {
			ww := tafsWidth(word)
			nw = max(nw, ww)
			if line > 0 {
				line += tafsWidth(" ")
			}
			line += ww
		}
		n = max(n, line)
	}
	return templ.Attributes{"style": fmt.Sprintf("--n:%.2f;--nw:%.2f", n, nw)}
}

// tafsWidth is a run of upper-case heavy type's width in ems.
func tafsWidth(s string) float64 {
	w := 0.0
	for _, r := range strings.ToUpper(s) {
		switch {
		case strings.ContainsRune(" .,:;!'’I", r):
			w += .33
		case strings.ContainsRune("MW", r):
			w += .86
		default:
			w += .66
		}
	}
	return w
}

// tafsRing fits a badge's ring text to its circle: the text is spaced out
// to the circle's length, and squeezed when it would run past it.
func tafsRing(s string, length int) templ.Attributes {
	adjust := "spacing"
	if float64(utf8.RuneCountInString(s))*9.2 > float64(length) {
		adjust = "spacingAndGlyphs"
	}
	return templ.Attributes{"textLength": fmt.Sprint(length), "lengthAdjust": adjust}
}

// tafsLangSticker places screen 1's language stickers, in the site's
// language order, each its own colour and drift.
func tafsLangSticker(i int) templ.Attributes {
	colours := []string{"var(--white)", "var(--lemon)", "var(--blue);--t:#fff", "var(--straw);--t:#fff",
		"var(--mint)", "var(--orange)", "var(--grape);--t:#fff", "var(--red);--t:#fff"}
	tilt, y := []int{-6, 5, -9, 7, -4, 8, -7, 4}, []int{80, 87, 79, 86, 78, 86, 79, 86}
	i %= len(colours)
	return templ.Attributes{
		"style":  fmt.Sprintf("--x:%d%%;--y:%d%%;--r0:%ddeg;--c:%s", 46+6*i, y[i], tilt[i], colours[i]),
		"data-x": fmt.Sprintf("%.1f", .3+.1*float64(i)),
		"data-r": fmt.Sprint(8 - 16*(i%2)),
	}
}

// tafsCurrent marks the link to the page being read.
func tafsCurrent(w tafsWords, lang string) templ.Attributes {
	if lang == w.lang() {
		return templ.Attributes{"aria-current": "page"}
	}
	return templ.Attributes{}
}

// doesFit sizes screen 4's three lines as one giant word.
func (w tafsWords) doesFit() templ.Attributes {
	var lines []string
	for _, l := range w.DoesLines {
		lines = append(lines, l[0]+" "+l[1])
	}
	return tafsFit(lines...)
}
