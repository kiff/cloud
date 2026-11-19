package pages

import (
	"context"
	"strings"
	"testing"

	"github.com/kiff/cloud/web/internal/content/blog"
)

// A post's summary is its index blurb and its meta description both. If
// the post page falls back to the site defaults, every shared article
// link shows the site tagline instead of the article, which is invisible
// in the markdown and only shows up in someone's link preview.
func TestBlogPostUsesItsOwnSummaryForShareMetadata(t *testing.T) {
	t.Parallel()

	const summary = "The specific finding a reader should see before opening the article."
	view := BlogPostView{
		Post: blog.Post{
			Meta: blog.Meta{
				Title:   "A boundary report",
				Summary: summary,
				Date:    "2026-09-05",
				Author:  "Gabriel Sarmiento",
				Kind:    blog.KindReport,
			},
		},
	}

	var out strings.Builder
	if err := BlogPost(view).Render(context.Background(), &out); err != nil {
		t.Fatalf("render blog post: %v", err)
	}
	html := out.String()

	for _, want := range []string{
		`<meta name="description" content="` + summary + `">`,
		`<meta property="og:description" content="` + summary + `">`,
		`<meta property="og:type" content="article">`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("rendered post missing %q", want)
		}
	}
	if strings.Contains(html, "Your agents can have different memories") {
		t.Error("post page fell back to the site-wide description")
	}
}
