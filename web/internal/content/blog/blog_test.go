package blog

import (
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

// These tests are the CMS.
//
// There is no admin UI and there will not be one: posts are written by
// an assistant following skills/publishing/SKILL.md and committed as
// markdown. Prose in a skill can be skipped; a failing build cannot. So
// every convention the skill describes is asserted here, and the failure
// messages are written to be read by whoever (or whatever) just added a
// post.

var slugRe = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
var tagRe = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

func mustPosts(t *testing.T) []Post {
	t.Helper()
	all, err := AllIncludingDrafts()
	if err != nil {
		t.Fatalf("posts failed to load — the build cannot serve /blog: %v", err)
	}
	return all
}

// Drafts are validated too, so an unpublished post cannot rot.
func TestEveryPostHasCompleteFrontmatter(t *testing.T) {
	all := mustPosts(t)
	published := make(map[string]bool, len(all))
	for _, p := range all {
		if !p.Draft {
			published[p.Slug] = true
		}
	}
	for _, p := range all {
		t.Run(p.Slug, func(t *testing.T) {
			if strings.TrimSpace(p.Title) == "" {
				t.Error("title is required")
			}
			if strings.TrimSpace(p.Summary) == "" {
				t.Error("summary is required — it is the index blurb and the meta description")
			}
			if strings.TrimSpace(p.Author) == "" {
				t.Error("author is required")
			}
			if len(p.Tags) == 0 {
				t.Error("at least one tag is required — decide the taxonomy when writing, not later")
			}
			if p.Kind == "" {
				t.Errorf("kind is required; one of %v", Kinds)
			} else if !p.Kind.Valid() {
				t.Errorf("kind %q is not valid; one of %v — a typo here would create a "+
					"silent one-post category", p.Kind, Kinds)
			}
			if strings.TrimSpace(p.Body) == "" {
				t.Error("body is empty")
			}
			for _, slug := range p.Related {
				if !published[slug] {
					t.Errorf("related slug %q must name a published post — check for a typo or a draft", slug)
				}
			}
		})
	}
}

func TestRelated(t *testing.T) {
	// Initialize the embedded catalog before replacing it with deterministic
	// fixtures. These tests are serial; restore the catalog for other tests.
	mustPosts(t)
	original := posts
	t.Cleanup(func() { posts = original })
	fixture := func(slug, date string, draft bool, tags ...string) Post {
		parsed, err := time.Parse("2006-01-02", date)
		if err != nil {
			t.Fatal(err)
		}
		return Post{Slug: slug, Parsed: parsed, Meta: Meta{Draft: draft, Tags: tags}}
	}
	posts = []Post{
		fixture("current", "2026-01-07", false, "mcp", "governance"),
		fixture("draft", "2026-01-06", true, "mcp", "governance"),
		fixture("recent-one-tag", "2026-01-05", false, "mcp"),
		fixture("recent-two-tags", "2026-01-04", false, "mcp", "governance"),
		fixture("older-two-tags", "2026-01-03", false, "mcp", "governance"),
		fixture("older-one-tag", "2026-01-02", false, "mcp"),
		fixture("editorial", "2026-01-01", false, "unrelated"),
		fixture("unrelated", "2025-12-31", false, "other"),
	}
	for _, tc := range []struct {
		name    string
		related []string
		limit   int
		want    []string
	}{
		{"editorial order and exclusions", []string{"editorial", "current", "draft", "missing", "older-one-tag", "editorial"}, 10,
			[]string{"editorial", "older-one-tag", "recent-two-tags", "older-two-tags", "recent-one-tag"}},
		{"tag score then recency", nil, 10,
			[]string{"recent-two-tags", "older-two-tags", "recent-one-tag", "older-one-tag"}},
		{"editorial limit", []string{"editorial", "older-one-tag"}, 1, []string{"editorial"}},
		{"ranked limit", nil, 2, []string{"recent-two-tags", "older-two-tags"}},
		{"zero limit", []string{"editorial"}, 0, nil},
		{"negative limit", nil, -1, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Drafts must remain excluded even when preview is enabled.
			t.Setenv(PreviewEnv, "true")
			p := posts[0]
			p.Related = tc.related
			got, err := Related(p, tc.limit)
			if err != nil {
				t.Fatal(err)
			}
			var slugs []string
			for _, p := range got {
				slugs = append(slugs, p.Slug)
			}
			if !slices.Equal(slugs, tc.want) {
				t.Errorf("Related = %v, want %v", slugs, tc.want)
			}
		})
	}
}

func TestReadMinutes(t *testing.T) {
	for _, tc := range []struct{ words, want int }{
		{0, 1}, {1, 1}, {199, 1}, {200, 1}, {201, 2}, {400, 2}, {401, 3},
	} {
		p := Post{Body: strings.Repeat("word \n\t", tc.words)}
		if got := p.ReadMinutes(); got != tc.want {
			t.Errorf("%d words: ReadMinutes = %d, want %d", tc.words, got, tc.want)
		}
	}
}

func TestRenderImagePreviews(t *testing.T) {
	for _, dest := range []string{"/static/image.png", "./image.png", "../image.png", "https://example.com/image.png", "//example.com/image.png"} {
		t.Run(dest, func(t *testing.T) {
			got := string(render("![Screenshot caption](" + dest + ")"))
			for _, want := range []string{
				`src="` + dest + `"`, `alt="Screenshot caption"`,
				`class="blog-image-trigger"`, `popovertarget="blog-image-preview-1"`,
				`popover="auto"`, `role="dialog"`, `popovertargetaction="hide"`,
			} {
				if !strings.Contains(got, want) {
					t.Errorf("render missing %q: %s", want, got)
				}
			}
			if strings.Contains(got, "<a ") || strings.Contains(got, `target="_blank"`) || strings.Count(got, "<img ") != 2 {
				t.Errorf("image should have an inline thumbnail and preview without navigation: %s", got)
			}
		})
	}
	t.Run("existing image link", func(t *testing.T) {
		got := string(render("[![Screenshot caption](/static/image.png)](/original-destination)"))
		if strings.Count(got, "<a ") != 1 || !strings.Contains(got, `href="/original-destination"`) ||
			strings.Contains(got, `href="/static/image.png"`) || strings.Contains(got, "blog-image-trigger") {
			t.Errorf("existing image link was changed or nested: %s", got)
		}
		if !strings.Contains(got, `alt="Screenshot caption"`) {
			t.Errorf("image caption was lost: %s", got)
		}
	})
}

func TestSlugsAndTagsAreWellFormed(t *testing.T) {
	seen := map[string]bool{}
	for _, p := range mustPosts(t) {
		if !slugRe.MatchString(p.Slug) {
			t.Errorf("%s: filename must be lowercase-hyphenated (it becomes the URL)", p.Slug)
		}
		if seen[p.Slug] {
			t.Errorf("%s: duplicate slug", p.Slug)
		}
		seen[p.Slug] = true
		for _, tag := range p.Tags {
			if !tagRe.MatchString(tag) {
				t.Errorf("%s: tag %q must be lowercase-hyphenated", p.Slug, tag)
			}
		}
	}
}

// A summary that runs long stops working as a meta description and gets
// truncated in search results; one that is a fragment reads as a stub.
func TestSummariesAreOneUsefulSentence(t *testing.T) {
	for _, p := range mustPosts(t) {
		n := len(p.Summary)
		if n < 40 {
			t.Errorf("%s: summary is %d chars — too short to say anything (min 40)", p.Slug, n)
		}
		if n > 200 {
			t.Errorf("%s: summary is %d chars — it is a meta description, keep it under 200", p.Slug, n)
		}
	}
}

// A published post dated in the future is almost always a typo, and it
// sorts above everything real until that date passes.
func TestPublishedPostsAreNotFutureDated(t *testing.T) {
	tomorrow := time.Now().UTC().AddDate(0, 0, 1)
	for _, p := range mustPosts(t) {
		if p.Draft {
			continue
		}
		if p.Parsed.After(tomorrow) {
			t.Errorf("%s: dated %s, which is in the future — mark it draft: true until it ships",
				p.Slug, p.Date)
		}
	}
}

// The title renders as the h1, so a post that opens with its own h1
// shows the title twice.
func TestBodyDoesNotRepeatTheTitleAsH1(t *testing.T) {
	for _, p := range mustPosts(t) {
		if strings.HasPrefix(strings.TrimSpace(p.Body), "# ") {
			t.Errorf("%s: body starts with an h1 — the title comes from frontmatter, "+
				"start the body at ## or with prose", p.Slug)
		}
	}
}

// Every post is drafted by an assistant; the AI;DR is the one place
// that says so instead of leaving it implicit. It must be the first
// thing in the body — a reader deciding whether to keep going should
// not have to scroll past a lede to find it.
var aidrRe = regexp.MustCompile(`(?s)^>\s*\*\*AI;DR:\*\*\s+\S`)

func TestBodyOpensWithAIDR(t *testing.T) {
	for _, p := range mustPosts(t) {
		body := strings.TrimSpace(p.Body)
		if !aidrRe.MatchString(body) {
			t.Errorf("%s: body must open with a blockquote AI;DR — "+
				`"> **AI;DR:** <one or two sentences>" — before any other content`, p.Slug)
		}
	}
}

func TestDraftsAreExcludedFromTheIndex(t *testing.T) {
	t.Setenv(PreviewEnv, "")
	all := mustPosts(t)
	published, err := All()
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	for _, p := range published {
		if p.Draft {
			t.Errorf("%s: draft leaked into the published index", p.Slug)
		}
	}
	drafts := 0
	for _, p := range all {
		if p.Draft {
			drafts++
		}
	}
	if len(all)-drafts != len(published) {
		t.Errorf("published count %d does not match non-draft count %d", len(published), len(all)-drafts)
	}
}

// Preview is opt-in and local. A deployed box with the variable unset
// must never serve a draft, so the default is asserted explicitly.
func TestDraftPreviewIsOffByDefault(t *testing.T) {
	t.Setenv(PreviewEnv, "")
	if PreviewDrafts() {
		t.Fatal("draft preview must default to off")
	}
	for _, on := range []string{"1", "true", "TRUE"} {
		t.Setenv(PreviewEnv, on)
		if !PreviewDrafts() {
			t.Errorf("%s should enable preview", on)
		}
	}
	t.Setenv(PreviewEnv, "yes")
	if PreviewDrafts() {
		t.Error("only 1/true enable preview; anything else is off")
	}
}

func TestPostsAreOrderedNewestFirst(t *testing.T) {
	t.Setenv(PreviewEnv, "")
	published, err := All()
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	for i := 1; i < len(published); i++ {
		if published[i-1].Parsed.Before(published[i].Parsed) {
			t.Errorf("%s (%s) sorts before %s (%s) — index must be newest first",
				published[i-1].Slug, published[i-1].Date, published[i].Slug, published[i].Date)
		}
	}
}

func TestRenderedHTMLIsMemoizedAndNonEmpty(t *testing.T) {
	for _, p := range mustPosts(t) {
		first := HTML(p)
		if strings.TrimSpace(string(first)) == "" {
			t.Errorf("%s: rendered to empty html", p.Slug)
		}
		if HTML(p) != first {
			t.Errorf("%s: render is not stable across calls", p.Slug)
		}
	}
}

// Inline SVG is how diagrams ship — the renderer must not strip it.
func TestRendererPassesInlineHTMLThrough(t *testing.T) {
	got := string(render("before\n\n<svg><circle r=\"1\"/></svg>\n\nafter"))
	if !strings.Contains(got, "<svg>") {
		t.Fatalf("inline svg was stripped; diagrams would silently vanish:\n%s", got)
	}
}

func TestTagsAreCountedAndSorted(t *testing.T) {
	t.Setenv(PreviewEnv, "1") // count the draft too, so the test has data
	tags, err := Tags()
	if err != nil {
		t.Fatalf("Tags: %v", err)
	}
	if len(tags) == 0 {
		t.Fatal("no tags found across posts")
	}
	for i := 1; i < len(tags); i++ {
		prev, cur := tags[i-1], tags[i]
		if prev.Count < cur.Count {
			t.Errorf("tags must sort by count desc: %v before %v", prev, cur)
		}
		if prev.Count == cur.Count && prev.Tag > cur.Tag {
			t.Errorf("equal counts must sort alphabetically: %q before %q", prev.Tag, cur.Tag)
		}
	}
}

// Every tag must lead somewhere. A tag directory listing a filter that
// returns nothing is worse than no directory.
func TestEveryTagHasAtLeastOnePost(t *testing.T) {
	t.Setenv(PreviewEnv, "1")
	tags, err := Tags()
	if err != nil {
		t.Fatalf("Tags: %v", err)
	}
	for _, tc := range tags {
		posts, err := ByTag(tc.Tag)
		if err != nil {
			t.Fatalf("ByTag(%q): %v", tc.Tag, err)
		}
		if len(posts) != tc.Count {
			t.Errorf("tag %q: directory says %d posts, filter returns %d",
				tc.Tag, tc.Count, len(posts))
		}
	}
}

func TestByTagIsEmptyForUnknownTag(t *testing.T) {
	posts, err := ByTag("no-such-tag")
	if err != nil {
		t.Fatalf("ByTag: %v", err)
	}
	if len(posts) != 0 {
		t.Errorf("unknown tag returned %d posts", len(posts))
	}
}

func TestByYearGroupsNewestFirstWithoutLosingPosts(t *testing.T) {
	in := []Post{
		{Slug: "c", Parsed: time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)},
		{Slug: "b", Parsed: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
		{Slug: "a", Parsed: time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)},
	}
	years := ByYear(in)
	if len(years) != 2 {
		t.Fatalf("want 2 year groups, got %d", len(years))
	}
	if years[0].Year != 2026 || years[1].Year != 2025 {
		t.Errorf("years must be newest first, got %d then %d", years[0].Year, years[1].Year)
	}
	total := 0
	for _, y := range years {
		total += len(y.Posts)
	}
	if total != len(in) {
		t.Errorf("grouping lost posts: %d in, %d out", len(in), total)
	}
}

func TestHeadingsAreExtractedSluggedAndDeduped(t *testing.T) {
	body := "> **AI;DR:** x\n\n" +
		"## First Section\n\nsome prose\n\n" +
		"```\n## not a heading, it's a fence\n```\n\n" +
		"### A Sub-Point\n\n" +
		"## First Section\n"
	got := Headings(body)
	want := []Heading{
		{Level: 2, Text: "First Section", ID: "first-section"},
		{Level: 3, Text: "A Sub-Point", ID: "a-sub-point"},
		{Level: 2, Text: "First Section", ID: "first-section-1"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d headings, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("heading %d: got %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestKindLabelsAreHumanReadable(t *testing.T) {
	for _, k := range Kinds {
		if !k.Valid() {
			t.Errorf("%q is in Kinds but not Valid()", k)
		}
		if k.Label() == "" || k.Label() == string(k) && k != "" && strings.ToLower(k.Label()) != string(k) {
			t.Errorf("%q has no distinct label", k)
		}
	}
	if Kind("nonsense").Valid() {
		t.Error("an unknown kind must not validate")
	}
}
