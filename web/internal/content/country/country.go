// Package country holds the "Your country" sections that both privacy
// policies link to: what a country's law asks beyond them, each in English
// and in the country's language (owner, 20 Nov 2026: English binds unless
// local law says the local text does). The sources are legal-i18n's drafts in
// web/legal-drafts/country, copied here as they are once approved; a test
// keeps each copy identical to its draft.
package country

import (
	"bytes"
	"embed"
	"fmt"
	"html/template"
	"regexp"
	"sort"
	"sync"

	"github.com/gomarkdown/markdown"
	"github.com/gomarkdown/markdown/html"
	"github.com/gomarkdown/markdown/parser"
)

//go:embed *.md
var files embed.FS

// Section is one country's section in one language.
type Section struct {
	Country string // ISO 3166-1, lower case: "kr"
	Lang    string // BCP 47: "en", "ko"
	Title   string // the first heading
	HTML    template.HTML
}

// Served are the countries served, each with its languages, English first.
var Served = map[string][]string{
	"kr": {"en", "ko"},
}

// Names are each served country's name in English, for the index.
var Names = map[string]string{"kr": "Korea"}

var (
	once     sync.Once
	sections map[string]Section
	loadErr  error
	heading  = regexp.MustCompile(`(?m)^# (.+)$`)
)

// Get is a country's section in a language, if it's served.
func Get(country, lang string) (Section, bool, error) {
	once.Do(load)
	if loadErr != nil {
		return Section{}, false, loadErr
	}
	s, ok := sections[country+"."+lang]
	return s, ok, nil
}

// Countries are the served countries, sorted.
func Countries() []string {
	out := make([]string, 0, len(Served))
	for c := range Served {
		out = append(out, c)
	}
	sort.Strings(out)
	return out
}

func load() {
	sections = map[string]Section{}
	for c, langs := range Served {
		for _, l := range langs {
			src, err := files.ReadFile(c + "." + l + ".md")
			if err != nil {
				loadErr = fmt.Errorf("country: %s.%s: %w", c, l, err)
				return
			}
			if bytes.Contains(src, []byte("[owner]")) || bytes.Contains(src, []byte("[mac-local]")) {
				loadErr = fmt.Errorf("country: %s.%s still has a drafting mark", c, l)
				return
			}
			title := c
			if m := heading.FindSubmatch(src); m != nil {
				title = string(m[1])
			}
			sections[c+"."+l] = Section{Country: c, Lang: l, Title: title, HTML: render(src)}
		}
	}
}

// render turns a section into HTML with raw HTML off: anything that isn't
// Markdown (a tag, a comment, a script) is dropped, and links are https or
// mailto only, opened with no opener or referrer.
func render(src []byte) template.HTML {
	p := parser.NewWithExtensions(parser.CommonExtensions | parser.AutoHeadingIDs)
	r := html.NewRenderer(html.RendererOptions{
		Flags: html.CommonFlags | html.SkipHTML | html.Safelink | html.HrefTargetBlank | html.NoopenerLinks | html.NoreferrerLinks,
	})
	// The source is this repository's, reviewed text; raw HTML is dropped above.
	return template.HTML(markdown.Render(p.Parse(src), r))
}
