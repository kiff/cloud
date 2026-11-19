package handlers

import (
	"fmt"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/kiff/cloud/web/internal/components"
)

// llmsPage is a public page llms.txt links that /llms-full.txt carries
// in full: its path on kiff.dev and the name llms.txt gives it.
type llmsPage struct {
	Path, Name string
}

// llmsExcluded is a kiff.dev link in llms.txt that /llms-full.txt does
// not render as a page, and why.
type llmsExcluded struct {
	Path, Reason string
}

// llmsLink matches a markdown link to kiff.dev: [name](https://kiff.dev/…).
var llmsLink = regexp.MustCompile(`\[([^\]]+)\]\((https://kiff\.dev(?:/[^)\s]*)?)\)`)

// llmsFullPages reads the pages /llms-full.txt carries from the curated
// map itself, so llms.txt stays the one list of the public surface. Each
// kiff.dev link is either a page (in the map's order, each once) or
// excluded with a reason; nothing falls between.
func llmsFullPages(llmsTxt string) (pages []llmsPage, excluded []llmsExcluded) {
	seen := map[string]bool{}
	for _, m := range llmsLink.FindAllStringSubmatch(llmsTxt, -1) {
		u, err := url.Parse(m[2])
		if err != nil {
			excluded = append(excluded, llmsExcluded{m[2], "not a valid URL"})
			continue
		}
		p := u.Path
		if p == "" {
			p = "/"
		}
		if seen[p] {
			continue
		}
		seen[p] = true
		if reason := llmsExclusion(p); reason != "" {
			excluded = append(excluded, llmsExcluded{p, reason})
			continue
		}
		pages = append(pages, llmsPage{Path: p, Name: m[1]})
	}
	return pages, excluded
}

// llmsExclusion says why a kiff.dev path is not carried as a page, or ""
// when it is one.
func llmsExclusion(p string) string {
	switch {
	case p == "/docs" || strings.HasPrefix(p, "/docs/"):
		return "the docs are carried from their markdown"
	case p == "/whitepaper", p == "/why":
		return "carried from its markdown"
	case p == "/invariants":
		// The page is generated from the test suite: its text is test
		// names and file paths (api/internal/handlers/ops_…_test.go),
		// which the private-surface guard rightly cannot tell from /ops.
		// llms.txt describes the page.
		return "its text is test names and file paths, not prose"
	case path.Ext(p) != "":
		return "a file, not a page"
	}
	if lang, _, ok := components.SplitLangPath(p); ok && lang != "en" {
		return "a translation; llms-full.txt is English"
	}
	return ""
}

// renderPublicPage asks site for path as an anonymous visitor on kiff.dev
// would, and returns the HTML it serves. Nothing leaves the process.
func renderPublicPage(site http.Handler, p string) (string, error) {
	req := httptest.NewRequest(http.MethodGet, components.SiteBaseURL+p, nil)
	req.Header.Set("Accept", "text/html")
	rec := httptest.NewRecorder()
	site.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		return "", fmt.Errorf("%s answered %d", p, rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		return "", fmt.Errorf("%s is %q, not an HTML page", p, ct)
	}
	return rec.Body.String(), nil
}

// The converter below reads only what a page's <main> holds and writes it
// as plain, markdown-ish text: headings as #, paragraphs and list items as
// lines, a link's text without its href. It is a small tokenizer for the
// HTML this app renders (templ output), not a general HTML parser.

// htmlVoid elements have no end tag.
var htmlVoid = map[string]bool{
	"area": true, "base": true, "br": true, "col": true, "embed": true, "hr": true,
	"img": true, "input": true, "link": true, "meta": true, "source": true,
	"track": true, "wbr": true,
}

// htmlRawText elements hold text that is not markup; their content is
// skipped whole.
var htmlRawText = map[string]bool{"script": true, "style": true, "textarea": true, "title": true}

// htmlSkipped elements are not the page's text: graphics, navigation,
// form choices and inert templates.
var htmlSkipped = map[string]bool{
	"svg": true, "nav": true, "template": true, "noscript": true, "select": true,
	"iframe": true, "video": true, "audio": true, "canvas": true, "object": true,
}

// htmlInline elements continue the line they are in; every other element
// starts and ends a line.
var htmlInline = map[string]bool{
	"a": true, "abbr": true, "b": true, "bdi": true, "bdo": true, "cite": true,
	"code": true, "data": true, "del": true, "dfn": true, "em": true, "i": true,
	"img": true, "ins": true, "kbd": true, "label": true, "mark": true, "q": true,
	"s": true, "samp": true, "small": true, "span": true, "strong": true, "sub": true,
	"sup": true, "time": true, "u": true, "var": true, "wbr": true, "output": true,
	"button": true,
}

type lineKind int

const (
	lineText lineKind = iota
	lineItem
	lineHeading
	linePre
)

type textLine struct {
	kind lineKind
	text string
}

// pageText is the converter's state for one page.
type pageText struct {
	lines   []textLine
	cur     strings.Builder
	kind    lineKind
	level   int  // heading level while kind is lineHeading
	shift   int  // added to every heading level
	joined  bool // the last token ended an inline element
	pre     int  // depth inside <pre>
	skip    string
	skipN   int // nesting of skip inside itself
	inMain  int // depth inside <main>
	mainEnd bool
}

// htmlMainText converts the <main> of a rendered page to plain text. A
// page's <h1> is written at level 1+shift, so the page's own headings
// can sit under the heading that names it.
func htmlMainText(doc string, shift int) string {
	t := &pageText{shift: shift}
	for i := 0; i < len(doc) && !t.mainEnd; {
		if doc[i] != '<' {
			j := strings.IndexByte(doc[i:], '<')
			if j < 0 {
				j = len(doc) - i
			}
			t.text(doc[i : i+j])
			i += j
			continue
		}
		rest := doc[i:]
		switch {
		case strings.HasPrefix(rest, "<!--"):
			i += skipPast(rest, "-->")
		case strings.HasPrefix(rest, "<!"), strings.HasPrefix(rest, "<?"):
			i += skipPast(rest, ">")
		case strings.HasPrefix(rest, "</"):
			n := skipPast(rest, ">")
			t.end(strings.ToLower(strings.TrimSpace(strings.TrimSuffix(rest[2:n], ">"))))
			i += n
		case len(rest) > 1 && isASCIILetter(rest[1]):
			name, attrs, selfClosing, n := readStartTag(rest)
			i += n
			if htmlRawText[name] {
				i += rawTextLen(doc[i:], name)
				continue
			}
			t.start(name, attrs, selfClosing)
		default:
			t.text("<")
			i++
		}
	}
	t.flush()
	return t.join()
}

func isASCIILetter(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }

// skipPast is the length of s up to and including the first end, or all
// of s when there is none.
func skipPast(s, end string) int {
	if j := strings.Index(s, end); j >= 0 {
		return j + len(end)
	}
	return len(s)
}

// readStartTag reads "<name attrs…>" at the start of s, honouring quoted
// attribute values, and returns the tag's lower-cased name, its raw
// attributes, whether it closes itself, and its length.
func readStartTag(s string) (name, attrs string, selfClosing bool, n int) {
	i := 1
	for i < len(s) && s[i] != '>' && s[i] != '/' && s[i] != ' ' && s[i] != '\t' && s[i] != '\n' && s[i] != '\r' {
		i++
	}
	name = strings.ToLower(s[1:i])
	start := i
	var quote byte
	for ; i < len(s); i++ {
		c := s[i]
		if quote != 0 {
			if c == quote {
				quote = 0
			}
			continue
		}
		if c == '"' || c == '\'' {
			quote = c
			continue
		}
		if c == '>' {
			attrs = s[start:i]
			selfClosing = strings.HasSuffix(strings.TrimSpace(attrs), "/")
			return name, attrs, selfClosing, i + 1
		}
	}
	return name, s[start:], false, len(s)
}

// rawTextLen is the length of a raw-text element's content and its end
// tag, starting just after its start tag.
func rawTextLen(s, name string) int {
	lower := strings.ToLower(s)
	j := strings.Index(lower, "</"+name)
	if j < 0 {
		return len(s)
	}
	return j + skipPast(s[j:], ">")
}

// hiddenAttrs reports whether an element is hidden from readers.
func hiddenAttrs(attrs string) bool {
	a := strings.ToLower(attrs)
	if strings.Contains(a, `aria-hidden="true"`) || strings.Contains(a, `aria-hidden='true'`) {
		return true
	}
	for _, f := range strings.FieldsFunc(a, func(r rune) bool { return r == ' ' || r == '\t' || r == '\n' || r == '/' }) {
		if f == "hidden" || strings.HasPrefix(f, "hidden=") {
			return true
		}
	}
	return false
}

func (t *pageText) start(name, attrs string, selfClosing bool) {
	if t.skip != "" {
		if name == t.skip && !selfClosing {
			t.skipN++
		}
		return
	}
	if name == "main" {
		t.inMain++
		return
	}
	if t.inMain == 0 {
		return
	}
	if htmlSkipped[name] || hiddenAttrs(attrs) {
		if !selfClosing && !htmlVoid[name] {
			t.skip, t.skipN = name, 1
		}
		return
	}
	if t.pre > 0 {
		if name == "br" {
			t.cur.WriteByte('\n')
		}
		return
	}
	switch {
	case name == "pre":
		t.flush()
		t.pre++
		t.kind = linePre
	case len(name) == 2 && name[0] == 'h' && name[1] >= '1' && name[1] <= '6':
		t.flush()
		t.kind, t.level = lineHeading, int(name[1]-'0')
	case name == "li":
		t.flush()
		t.kind = lineItem
	case name == "td" || name == "th":
		if strings.TrimSpace(t.cur.String()) != "" {
			t.cur.WriteString(" | ")
		}
	case name == "img":
	case htmlInline[name]:
		// Two elements side by side read as two words: "<b>Plan</b><span>€10</span>".
		if t.joined && !strings.HasSuffix(t.cur.String(), " ") {
			t.cur.WriteByte(' ')
		}
	default:
		t.flush()
	}
}

func (t *pageText) end(name string) {
	if t.skip != "" {
		if name == t.skip {
			t.skipN--
			if t.skipN == 0 {
				t.skip = ""
			}
		}
		return
	}
	if name == "main" {
		if t.inMain > 0 {
			t.inMain--
			if t.inMain == 0 {
				t.mainEnd = true
			}
		}
		return
	}
	if t.inMain == 0 {
		return
	}
	if name == "pre" {
		if t.pre > 0 {
			t.pre--
		}
		if t.pre == 0 {
			t.flush()
			t.kind = lineText
		}
		return
	}
	if t.pre > 0 || name == "td" || name == "th" {
		return
	}
	if htmlInline[name] {
		t.joined = true
		return
	}
	t.flush()
	if name == "li" || len(name) == 2 && name[0] == 'h' && name[1] >= '1' && name[1] <= '6' {
		t.kind, t.level = lineText, 0
	}
}

func (t *pageText) text(s string) {
	if t.inMain == 0 || t.skip != "" {
		return
	}
	s = html.UnescapeString(s)
	// A word right after an element is a new word: "<b>€12.50</b>a month".
	// Punctuation stays attached: "<a>Pricing</a>."
	if r, _ := utf8.DecodeRuneInString(s); t.joined && (unicode.IsLetter(r) || unicode.IsDigit(r)) && !strings.HasSuffix(t.cur.String(), " ") {
		t.cur.WriteByte(' ')
	}
	t.joined = false
	t.cur.WriteString(s)
}

// flush ends the current line, collapsing its whitespace (a <pre> block
// keeps its own lines).
func (t *pageText) flush() {
	t.joined = false
	raw := t.cur.String()
	if strings.TrimSpace(raw) == "" {
		// Nothing to end: an <li> or heading whose text sits in a
		// nested block keeps its kind for that text.
		t.cur.Reset()
		return
	}
	t.cur.Reset()
	kind, level := t.kind, t.level
	t.kind, t.level = lineText, 0
	if kind == linePre {
		if s := strings.Trim(raw, "\n"); strings.TrimSpace(s) != "" {
			t.lines = append(t.lines, textLine{linePre, "```\n" + s + "\n```"})
		}
		return
	}
	s := strings.Join(strings.Fields(raw), " ")
	if s == "" || s == "|" {
		return
	}
	switch kind {
	case lineHeading:
		s = strings.Repeat("#", min(level+t.shift, 6)) + " " + s
	case lineItem:
		s = "- " + s
	}
	t.lines = append(t.lines, textLine{kind, s})
}

// join writes the lines, a blank line between blocks and none inside a list.
func (t *pageText) join() string {
	var b strings.Builder
	for i, l := range t.lines {
		if i > 0 {
			b.WriteByte('\n')
			if !(l.kind == lineItem && t.lines[i-1].kind == lineItem) {
				b.WriteByte('\n')
			}
		}
		b.WriteString(l.text)
	}
	return b.String()
}
