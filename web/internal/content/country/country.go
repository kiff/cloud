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
	"cm": {"en", "fr"},
	"kr": {"en", "ko"},
	"mc": {"en", "fr"},
	"ni": {"en", "es"},
	"sm": {"en", "it"},
	"sv": {"en", "es"},
	"mr": {"en", "ar"},
	"md": {"en", "ro"},
	"rs": {"en", "sr"},
	"al": {"en", "sq"},
	"lb": {"en", "ar"},
	"jo": {"en", "ar"},
	"om": {"en", "ar"},
	"eg": {"en", "ar"},
	"bh": {"en", "ar"},
	"qa": {"en", "ar"},
	"ae": {"en", "ar"},
	"sa": {"en", "ar"},
	"tw": {"en", "zh"},
	"vn": {"en", "vi"},
	"th": {"en", "th"},
	"id": {"en", "id"},
	"tr": {"en", "tr"},
	"bn": {"en"},
	"lk": {"en", "si", "ta"},
	"tj": {"en", "tg"},
}

// Notice is a legal notice served from these drafts outside the "Your
// country" index, such as Japan's seller notice: its path, and its
// languages, English first.
type Notice struct {
	Path  string
	Langs []string
}

// Notices are the notices served, by draft name ("jp-tokushoho" is
// jp-tokushoho.<lang>.md). A notice joins once its draft has no drafting
// mark left.
var Notices = map[string]Notice{
	"jp-tokushoho": {Path: "/legal/japan", Langs: []string{"en", "ja"}},
}

// Names are each served country's name in English, for the index.
var Names = map[string]string{"bn": "Brunei", "lk": "Sri Lanka", "tj": "Tajikistan", "cm": "Cameroon", "kr": "Korea", "mc": "Monaco", "ni": "Nicaragua", "sm": "San Marino", "sv": "El Salvador", "tr": "Türkiye", "id": "Indonesia", "th": "Thailand", "vn": "Vietnam", "tw": "Taiwan", "sa": "Saudi Arabia", "ae": "United Arab Emirates", "qa": "Qatar", "bh": "Bahrain", "eg": "Egypt", "om": "Oman", "jo": "Jordan", "lb": "Lebanon", "mr": "Mauritania", "md": "Moldova", "rs": "Serbia", "al": "Albania"}

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

// GetNotice is a notice in a language, if it's served.
func GetNotice(name, lang string) (Section, bool, error) {
	once.Do(load)
	if loadErr != nil {
		return Section{}, false, loadErr
	}
	s, ok := sections["notice:"+name+"."+lang]
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
	for name, n := range Notices {
		for _, l := range n.Langs {
			s, err := loadOne(name, l)
			if err != nil {
				loadErr = err
				return
			}
			sections["notice:"+name+"."+l] = s
		}
	}
}

// loadOne reads one notice's copy, refusing one with a drafting mark left.
func loadOne(name, lang string) (Section, error) {
	src, err := files.ReadFile(name + "." + lang + ".md")
	if err != nil {
		return Section{}, fmt.Errorf("country: %s.%s: %w", name, lang, err)
	}
	if bytes.Contains(src, []byte("[owner]")) || bytes.Contains(src, []byte("[mac-local]")) {
		return Section{}, fmt.Errorf("country: %s.%s still has a drafting mark", name, lang)
	}
	title := name
	if m := heading.FindSubmatch(src); m != nil {
		title = string(m[1])
	}
	return Section{Country: name, Lang: lang, Title: title, HTML: render(src)}, nil
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
