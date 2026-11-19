// Package blog embeds the public writing as markdown files and renders
// them for the /blog routes.
//
// It follows the same shape as the docs and whitepaper packages —
// embed.FS + gomarkdown + memoization, 's hermetic Go/templ binary —
// with one deliberate difference.
//
// Docs carry a hand-maintained Go registry, because their nav order and
// grouping are curated decisions. A blog is chronological: order is
// derived from a date, not chosen. So a post declares its own metadata
// in YAML frontmatter and adding one means adding a single markdown
// file. Nothing to register, no Go to edit, no compile error from a
// missing comma in a slice — which matters because these posts are
// written by an assistant against the publishing skill, not by someone
// navigating a CMS.
//
// What keeps that safe is validation: blog_test.go fails the build on a
// malformed or incomplete post. The convention is documented in the
// skill; the test is what enforces it.
package blog

import (
	"bytes"
	"embed"
	"fmt"
	"html/template"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/gomarkdown/markdown"
	"github.com/gomarkdown/markdown/ast"
	"github.com/gomarkdown/markdown/html"
	"github.com/gomarkdown/markdown/parser"
	"gopkg.in/yaml.v3"
)

//go:embed posts/*.md
var postsFS embed.FS

// DateLayout is the only accepted date format. A fixed layout keeps
// ordering unambiguous and makes a malformed date a build failure
// rather than a silently mis-sorted post.
const DateLayout = "2006-01-02"

// Kind is what sort of writing a post is. It is a label and a filter,
// never its own landing page — an unused kind therefore costs nothing,
// which is why the set can be broader than what gets written today.
type Kind string

const (
	// KindEssay is long-form: a finding, its evidence, what it means.
	KindEssay Kind = "essay"
	// KindNote is a paragraph or two on something small.
	KindNote Kind = "note"
	// KindChangelog is what shipped, dated and factual.
	KindChangelog Kind = "changelog"
	// KindAnnouncement is company or product news.
	KindAnnouncement Kind = "announcement"
	// KindReport is a published audit or assessment — a sample of work
	// delivered, not a piece written about it.
	KindReport Kind = "report"
)

// Kinds is every valid kind, in display order.
var Kinds = []Kind{KindEssay, KindReport, KindNote, KindChangelog, KindAnnouncement}

// Label is the human-readable form used in the UI.
func (k Kind) Label() string {
	switch k {
	case KindEssay:
		return "Essay"
	case KindNote:
		return "Note"
	case KindChangelog:
		return "Changelog"
	case KindAnnouncement:
		return "Announcement"
	case KindReport:
		return "Report"
	}
	return string(k)
}

// Valid reports whether k is one of the known kinds.
func (k Kind) Valid() bool {
	for _, known := range Kinds {
		if k == known {
			return true
		}
	}
	return false
}

// Meta is a post's frontmatter. Every field except Draft and Related is required;
// the test suite rejects a post that omits one.
type Meta struct {
	Title string `yaml:"title"`
	// Kind labels the post and drives filtering. Required, and
	// validated against Kinds so a typo fails the build rather than
	// creating a silent one-post category.
	Kind Kind `yaml:"kind"`
	// Date orders the index. Newest first.
	Date string `yaml:"date"`
	// Summary is the index blurb and the meta description. One
	// sentence — it is the only thing most readers see.
	Summary string `yaml:"summary"`
	// Author appears on the post.
	Author string `yaml:"author"`
	// Tags are lowercase and hyphenated. Each becomes a filtered view
	// at /blog/tags/<tag>, so the taxonomy is decided when a post is
	// written rather than reconstructed later across a dozen files.
	Tags []string `yaml:"tags"`
	// Draft keeps a post out of the index and off its own URL. A
	// drafted post is still validated, so it cannot rot while unpublished.
	Draft bool `yaml:"draft"`
	// Related optionally orders editorial recommendations by published slug.
	Related []string `yaml:"related"`
}

// ReadMinutes is an approximate reading time, including the article's examples.
func (p Post) ReadMinutes() int {
	return max(1, (len(strings.Fields(p.Body))+199)/200)
}

// Related returns published recommendations: editorial choices first, then
// shared tags, with newest posts first for ties. Drafts never enter this list,
// even when the local draft-preview mode is enabled.
func Related(p Post, limit int) ([]Post, error) {
	if err := load(); err != nil {
		return nil, err
	}
	if limit <= 0 {
		return nil, nil
	}
	seen := map[string]bool{p.Slug: true}
	var out []Post
	for _, slug := range p.Related {
		for _, candidate := range posts {
			if candidate.Slug == slug && !candidate.Draft && !seen[slug] {
				out = append(out, candidate)
				seen[slug] = true
			}
		}
		if len(out) == limit {
			return out, nil
		}
	}
	tags := make(map[string]bool, len(p.Tags))
	for _, tag := range p.Tags {
		tags[tag] = true
	}
	type ranked struct {
		post  Post
		score int
	}
	var candidates []ranked
	for _, candidate := range posts {
		if candidate.Draft || seen[candidate.Slug] {
			continue
		}
		score := 0
		for _, tag := range candidate.Tags {
			if tags[tag] {
				score++
			}
		}
		if score > 0 {
			candidates = append(candidates, ranked{candidate, score})
		}
	}
	sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].score > candidates[j].score })
	for _, candidate := range candidates {
		out = append(out, candidate.post)
		if len(out) == limit {
			break
		}
	}
	return out, nil
}

// TocFallbackCSS highlights the clicked anchor in browsers without native
// scroll tracking. IDs come from Headings' alphanumeric/dash sanitization;
// quoted selectors cannot introduce HTML or CSS from the article text.
func TocFallbackCSS(headings []Heading) string {
	var css strings.Builder
	css.WriteString("@supports not ((scroll-target-group: auto) and selector(a:target-current)) {")
	for _, h := range headings {
		fmt.Fprintf(&css, ".blog-post-layout:has(.prose [id=%q]:target) .blog-toc a[href=%q]{color:var(--fg);background:var(--blog-toc-active-bg);border-inline-start-color:var(--blog-accent);font-weight:600;}", h.ID, "#"+h.ID)
	}
	css.WriteString("}")
	return css.String()
}

// Post is one rendered article.
type Post struct {
	Meta
	// Slug is the URL segment and the markdown filename stem.
	Slug string
	// Parsed is Date resolved through DateLayout.
	Parsed time.Time
	// Body is the markdown after the frontmatter block.
	Body string
}

var (
	once   sync.Once
	posts  []Post
	loaded error

	htmlOnce sync.Map // slug -> template.HTML
)

// PreviewEnv, when set to "1" or "true", makes drafts visible on /blog
// and at their own URL.
//
// It exists because the publishing workflow asks the author to read the
// rendered page before committing, and without it that is impossible for
// the one state every post passes through. Local only — never set it on
// the deployed box, or unpublished writing becomes public.
const PreviewEnv = "KIFF_BLOG_PREVIEW_DRAFTS"

// PreviewDrafts reports whether drafts should be served.
func PreviewDrafts() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(PreviewEnv)))
	return v == "1" || v == "true"
}

// All returns every post that should be served, newest first. Drafts are
// excluded unless PreviewDrafts is on.
func All() ([]Post, error) {
	if err := load(); err != nil {
		return nil, err
	}
	preview := PreviewDrafts()
	out := make([]Post, 0, len(posts))
	for _, p := range posts {
		if preview || !p.Draft {
			out = append(out, p)
		}
	}
	return out, nil
}

// AllIncludingDrafts returns every post. Used by the validation suite so
// a draft cannot silently rot while it is unpublished.
func AllIncludingDrafts() ([]Post, error) {
	if err := load(); err != nil {
		return nil, err
	}
	return append([]Post(nil), posts...), nil
}

// TagCount is one tag and how many posts carry it.
type TagCount struct {
	Tag   string
	Count int
}

// Tags returns every tag across served posts, most used first, then
// alphabetical. It powers the index's tag directory: the taxonomy should
// be visible rather than implied.
func Tags() ([]TagCount, error) {
	all, err := All()
	if err != nil {
		return nil, err
	}
	counts := map[string]int{}
	for _, p := range all {
		for _, t := range p.Tags {
			counts[t]++
		}
	}
	out := make([]TagCount, 0, len(counts))
	for tag, n := range counts {
		out = append(out, TagCount{Tag: tag, Count: n})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Tag < out[j].Tag
	})
	return out, nil
}

// ByTag returns served posts carrying tag, newest first.
func ByTag(tag string) ([]Post, error) {
	all, err := All()
	if err != nil {
		return nil, err
	}
	out := make([]Post, 0, len(all))
	for _, p := range all {
		for _, t := range p.Tags {
			if t == tag {
				out = append(out, p)
				break
			}
		}
	}
	return out, nil
}

// Year groups posts under a single heading on the index.
type Year struct {
	Year  int
	Posts []Post
}

// ByYear groups posts newest year first. Grouping happens inside the one
// index page rather than across /blog/<year> archives, so the structure
// reads correctly at three posts and still at sixty.
func ByYear(in []Post) []Year {
	var out []Year
	for _, p := range in {
		y := p.Parsed.Year()
		if n := len(out); n > 0 && out[n-1].Year == y {
			out[n-1].Posts = append(out[n-1].Posts, p)
			continue
		}
		out = append(out, Year{Year: y, Posts: []Post{p}})
	}
	return out
}

// Get returns one published post by slug.
func Get(slug string) (Post, bool, error) {
	all, err := All()
	if err != nil {
		return Post{}, false, err
	}
	for _, p := range all {
		if p.Slug == slug {
			return p, true, nil
		}
	}
	return Post{}, false, nil
}

// HTML renders a post's body, memoized per slug.
func HTML(p Post) template.HTML {
	if cached, ok := htmlOnce.Load(p.Slug); ok {
		return cached.(template.HTML)
	}
	rendered := render(p.Body)
	htmlOnce.Store(p.Slug, rendered)
	return rendered
}

// Heading is one entry in a post's table of contents: the visible text
// and the fragment HTML() actually rendered for it.
type Heading struct {
	Level int // 2 or 3 — h1 is the page title, h4+ is too fine for a TOC
	Text  string
	ID    string
}

var headingLineRe = regexp.MustCompile(`^(#{2,3})\s+(.+?)\s*$`)

// Headings extracts the post's ##/### structure for the contents nav.
// IDs are generated with the same algorithm gomarkdown's AutoHeadingIDs
// uses (parser.sanitizeHeadingID + dedup-by-suffix), so a TOC link and
// the anchor render() produced always agree without parsing the
// rendered HTML back out.
func Headings(body string) []Heading {
	var out []Heading
	inFence := false
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		m := headingLineRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		out = append(out, Heading{Level: len(m[1]), Text: stripInlineMarkdown(m[2])})
	}
	taken := map[string]bool{}
	for i := range out {
		id := sanitizeHeadingID(out[i].Text)
		base := id
		for n := 1; taken[id]; n++ {
			id = base + "-" + strconv.Itoa(n)
		}
		taken[id] = true
		out[i].ID = id
	}
	return out
}

// stripInlineMarkdown removes the inline emphasis/code/link markup a
// heading may carry, since the TOC renders text, not HTML.
func stripInlineMarkdown(s string) string {
	s = regexp.MustCompile(`\[([^\]]*)\]\([^)]*\)`).ReplaceAllString(s, "$1")
	s = regexp.MustCompile("[`*_]").ReplaceAllString(s, "")
	return s
}

// sanitizeHeadingID mirrors gomarkdown parser.sanitizeHeadingID: letters
// and numbers lowercased, every other run collapsed to a single dash,
// with no dash before the first alnum rune.
func sanitizeHeadingID(text string) string {
	var b []rune
	dash := false
	for _, r := range text {
		switch {
		case unicode.IsLetter(r) || unicode.IsNumber(r):
			if dash && len(b) > 0 {
				b = append(b, '-')
			}
			dash = false
			b = append(b, unicode.ToLower(r))
		default:
			dash = true
		}
	}
	if len(b) == 0 {
		return "empty"
	}
	return string(b)
}

func load() error {
	once.Do(func() {
		entries, err := postsFS.ReadDir("posts")
		if err != nil {
			loaded = fmt.Errorf("read posts dir: %w", err)
			return
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
				continue
			}
			raw, err := postsFS.ReadFile("posts/" + e.Name())
			if err != nil {
				loaded = fmt.Errorf("read %s: %w", e.Name(), err)
				return
			}
			p, err := parse(strings.TrimSuffix(e.Name(), ".md"), raw)
			if err != nil {
				loaded = fmt.Errorf("%s: %w", e.Name(), err)
				return
			}
			posts = append(posts, p)
		}
		// Newest first; ties broken by slug so ordering is deterministic.
		sort.SliceStable(posts, func(i, j int) bool {
			if posts[i].Parsed.Equal(posts[j].Parsed) {
				return posts[i].Slug < posts[j].Slug
			}
			return posts[i].Parsed.After(posts[j].Parsed)
		})
	})
	return loaded
}

// parse splits a `---`-delimited frontmatter block from the body.
func parse(slug string, raw []byte) (Post, error) {
	text := strings.ReplaceAll(string(raw), "\r\n", "\n")
	if !strings.HasPrefix(text, "---\n") {
		return Post{}, fmt.Errorf("missing frontmatter: a post must open with a --- delimited block")
	}
	rest := text[len("---\n"):]
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return Post{}, fmt.Errorf("unterminated frontmatter: no closing ---")
	}
	var meta Meta
	if err := yaml.Unmarshal([]byte(rest[:end]), &meta); err != nil {
		return Post{}, fmt.Errorf("frontmatter is not valid yaml: %w", err)
	}
	body := strings.TrimPrefix(rest[end+len("\n---"):], "\n")

	parsed, err := time.Parse(DateLayout, meta.Date)
	if err != nil {
		return Post{}, fmt.Errorf("date %q is not %s", meta.Date, DateLayout)
	}
	return Post{Meta: meta, Slug: slug, Parsed: parsed, Body: strings.TrimSpace(body)}, nil
}

func render(md string) template.HTML {
	exts := parser.CommonExtensions | parser.AutoHeadingIDs | parser.Footnotes
	doc := parser.NewWithExtensions(exts).Parse([]byte(md))
	// Enlarge only unlinked Markdown images. Existing editorial image links keep
	// their destination and raw HTML/SVG remains untouched.
	var images []*ast.Image
	ast.WalkFunc(doc, func(node ast.Node, entering bool) ast.WalkStatus {
		if image, ok := node.(*ast.Image); ok && entering {
			linked := false
			for parent := image.GetParent(); parent != nil; parent = parent.GetParent() {
				if _, ok := parent.(*ast.Link); ok {
					linked = true
					break
				}
			}
			if !linked && len(image.Destination) > 0 {
				images = append(images, image)
			}
		}
		return ast.GoToNext
	})
	renderer := html.NewRenderer(html.RendererOptions{
		// CommonFlags deliberately does not skip HTML: posts embed
		// inline SVG diagrams, which must pass through untouched.
		Flags: html.CommonFlags | html.HrefTargetBlank | html.NoopenerLinks | html.NoreferrerLinks,
	})
	usedIDs := make(map[string]bool)
	for _, heading := range Headings(md) {
		usedIDs[heading.ID] = true
	}
	imageNumber := 0
	for _, image := range images {
		parent := image.GetParent()
		var id string
		for {
			imageNumber++
			id = fmt.Sprintf("blog-image-preview-%d", imageNumber)
			if !usedIDs[id] {
				usedIDs[id] = true
				break
			}
		}
		// Use the normal renderer for both copies, preserving escaped URLs,
		// alt text and titles. All surrounding markup is fixed or numeric.
		img := markdown.Render(image, renderer)
		markup := fmt.Sprintf(`<button type="button" class="blog-image-trigger" popovertarget="%s" popovertargetaction="show" title="Enlarge image">%s</button><span id="%s" class="blog-image-preview" popover="auto" role="dialog" aria-label="Image preview"><button type="button" class="blog-image-close" popovertarget="%s" popovertargetaction="hide" autofocus>Close ×</button>%s</span>`, id, img, id, id, img)
		preview := &ast.HTMLSpan{Leaf: ast.Leaf{Literal: []byte(markup)}}
		children := parent.GetChildren()
		for i, child := range children {
			if child == image {
				children[i] = preview
				break
			}
		}
		parent.SetChildren(children)
		preview.SetParent(parent)
	}
	return template.HTML(bytes.TrimSpace(markdown.Render(doc, renderer)))
}
