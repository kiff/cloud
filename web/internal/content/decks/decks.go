// Package decks embeds the pitch decks as vendored markdown and
// renders them to HTML for the /deck routes.
//
// Same hermetic pattern as /docs and /whitepaper (gomarkdown +
// memoization + embed.FS), per the web binary is hermetic Go + templ
// + native CSS, no Node, no slide toolchain. A deck is one markdown
// file whose slides are separated by a thematic break (a line that
// is exactly "---"). Each slide renders to its own page; navigation
// is plain prev/next links (?slide=N), server-side, no JS, the same
// URL-contract discipline as the landing example tabs.
//
// Decks live on kiff.dev (one binary, no subdomain of their own):
//
//	GET /deck/{slug}            → slide 1
//	GET /deck/{slug}?slide=N    → slide N (1-based)
//
// Voice rules: property-led, not crypto-led; no metric
// without a source; honest about what is shipped. The decks_test.go
// banned-token guard enforces the first rule.
package decks

import (
	"bytes"
	"embed"
	"fmt"
	"html/template"
	"regexp"
	"strings"
	"sync"

	"github.com/gomarkdown/markdown"
	"github.com/gomarkdown/markdown/html"
	"github.com/gomarkdown/markdown/parser"
)

//go:embed slides/*.md
var slidesFS embed.FS

// Deck is one pitch deck: a slug (URL + markdown filename stem), a
// title, the audience it is shaped for, and a version + last-updated
// stamp shown in the slide footer so a forwarded link always says
// how current it is.
type Deck struct {
	Slug     string
	Title    string
	Audience string // "Investors", "Sales", "General"
	Version  string // e.g. "v0.1"
	Updated  string // ISO date, e.g. "2026-06-05"
	Blurb    string // one line, used on the deck index
}

// decks is the ordered registry. Order is the index order. Sales is
// first: it is the deck the discovery motion needs most.
var decks = []Deck{
	{
		Slug:     "sales",
		Title:    "KIFF: decide once, enforced every time",
		Audience: "Sales",
		Version:  "v0.7",
		Updated:  "2026-09-28",
		Blurb:    "As agent actions get cheap, approving each one becomes the cost. A buyer conversation on moving people from approving actions to deciding authority: one card, changed live by the owner, correct under load, with a signed record, then observe before enforcing.",
	},
	{
		Slug:     "investors",
		Title:    "KIFF: authority for a fleet of agents",
		Audience: "Investors",
		Version:  "v0.6",
		Updated:  "2026-09-28",
		Blurb:    "Pete builds agents for clients. Twenty agents later, every limit lives in code and every incident calls him. Anne shows him cards the owner controls; he tests them on incidents, busy weeks and new models, and changes how he builds.",
	},
	{
		Slug:     "general",
		Title:    "KIFF: a card for every agent",
		Audience: "General",
		Version:  "v1.0",
		Updated:  "2026-10-02",
		Blurb:    "Agents now refund, book, buy and cancel. The payment rails cap one payment; KIFF caps the agent: every consequential action, over time, with the owner answering what falls outside. What a Card says, how a held call reaches the owner, a recorded demo, three ways to connect, and where the Card is and is not the final authority.",
	},
}

// slideBreak matches a thematic-break line (exactly "---", optional
// trailing whitespace) used as the slide separator.
var slideBreak = regexp.MustCompile(`(?m)^---\s*$`)

var (
	once         sync.Once
	slidesBySlug map[string][]template.HTML
	bySlug       map[string]Deck
	initErr      error
)

func load() {
	slidesBySlug = make(map[string][]template.HTML, len(decks))
	bySlug = make(map[string]Deck, len(decks))
	for _, d := range decks {
		bySlug[d.Slug] = d
		src, err := slidesFS.ReadFile("slides/" + d.Slug + ".md")
		if err != nil {
			initErr = fmt.Errorf("decks: missing markdown for slug %q: %w", d.Slug, err)
			return
		}
		chunks := slideBreak.Split(string(src), -1)
		var rendered []template.HTML
		for _, chunk := range chunks {
			if strings.TrimSpace(chunk) == "" {
				continue
			}
			rendered = append(rendered, renderMarkdown([]byte(chunk)))
		}
		if len(rendered) == 0 {
			initErr = fmt.Errorf("decks: slug %q has no slides", d.Slug)
			return
		}
		slidesBySlug[d.Slug] = rendered
	}
}

// Decks returns the ordered registry (for the index).
func Decks() []Deck {
	once.Do(load)
	out := make([]Deck, len(decks))
	copy(out, decks)
	return out
}

// Lookup returns a deck's metadata by slug, and whether it exists.
func Lookup(slug string) (Deck, bool) {
	once.Do(load)
	d, ok := bySlug[slug]
	return d, ok
}

// Slides returns the rendered slides for a deck. The bool is false
// for an unknown slug (the handler maps that to a 404).
func Slides(slug string) ([]template.HTML, bool, error) {
	once.Do(load)
	if initErr != nil {
		return nil, false, initErr
	}
	s, ok := slidesBySlug[slug]
	return s, ok, nil
}

// renderMarkdown mirrors the docs/whitepaper renderer with one
// deliberate difference: MathJax is dropped from the extension set.
// parser.CommonExtensions enables MathJax, which treats paired `$`
// as inline-math delimiters, so a slide that says "$250 → $100"
// renders as "\(250 → \)100". Decks quote dollar amounts, never math,
// so we spell the extensions out and omit MathJax. Raw HTML is passed
// through (no html.SkipHTML) so a slide can embed rich layout -
// clickable cards, a receipt mini-view, a diagram. External links
// open in a new tab. Input is vendored, not user-controlled.
func renderMarkdown(src []byte) template.HTML {
	exts := parser.NoIntraEmphasis |
		parser.Tables |
		parser.FencedCode |
		parser.Autolink |
		parser.Strikethrough |
		parser.SpaceHeadings |
		parser.HeadingIDs |
		parser.BackslashLineBreak |
		parser.DefinitionLists |
		parser.AutoHeadingIDs |
		parser.Footnotes

	p := parser.NewWithExtensions(exts)
	doc := p.Parse(src)

	opts := html.RendererOptions{
		Flags: html.CommonFlags | html.HrefTargetBlank | html.NoopenerLinks | html.NoreferrerLinks,
	}
	r := html.NewRenderer(opts)

	var buf bytes.Buffer
	buf.Grow(len(src) * 2)
	buf.Write(markdown.Render(doc, r))
	return template.HTML(buf.String())
}
