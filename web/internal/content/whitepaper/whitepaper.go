// Package whitepaper embeds the KIFF Cards whitepaper as a
// single markdown file, plus a renderer that converts it to HTML
// for the /whitepaper route.
//
// The whitepaper is original product content maintained in this repository
// under CC BY 4.0. Per the web binary ships hermetic; each revision updates
// the embedded markdown and the record in VERSIONS.md.
package whitepaper

import (
	"bytes"
	_ "embed"
	"html/template"
	"sync"

	"github.com/gomarkdown/markdown"
	"github.com/gomarkdown/markdown/html"
	"github.com/gomarkdown/markdown/parser"
)

//go:embed whitepaper.md
var source []byte

var (
	once      sync.Once
	rendered  template.HTML
	renderErr error
)

// Markdown returns the raw whitepaper bytes. Callers that want the
// rendered HTML should use HTML(); this exists for diagnostics and
// tests.
func Markdown() []byte {
	out := make([]byte, len(source))
	copy(out, source)
	return out
}

// HTML returns the whitepaper rendered to HTML, suitable for embedding
// in a templ template via templ.Raw or {!.HTML}. Rendering is done
// once and memoized; the input is fixed at build time.
func HTML() (template.HTML, error) {
	once.Do(func() {
		rendered, renderErr = render(source)
	})
	return rendered, renderErr
}

// render runs the markdown bytes through gomarkdown with the
// extensions the whitepaper relies on (tables, fenced code, footnotes,
// auto heading IDs, definition lists). Output is HTML5 with no script
// hooks and no inline styles.
func render(src []byte) (template.HTML, error) {
	// Apply the site's punctuation style at render time.
	src = bytes.ReplaceAll(src, []byte(" — "), []byte(", "))
	src = bytes.ReplaceAll(src, []byte("—"), []byte("-"))

	exts := parser.CommonExtensions |
		parser.AutoHeadingIDs |
		parser.Footnotes |
		parser.DefinitionLists |
		parser.HardLineBreak

	p := parser.NewWithExtensions(exts)
	doc := p.Parse(src)

	opts := html.RendererOptions{
		Flags: html.CommonFlags |
			html.HrefTargetBlank | // external links open in a new tab
			html.NoopenerLinks | html.NoreferrerLinks,
	}
	r := html.NewRenderer(opts)

	var buf bytes.Buffer
	buf.Grow(len(src) * 2) // rendered HTML is roughly twice the markdown size
	buf.Write(markdown.Render(doc, r))

	// template.HTML signals to html/template that this string is
	// already-safe HTML and must not be escaped. The input source is
	// maintained in this repository, not user-controlled.
	return template.HTML(buf.String()), nil
}
