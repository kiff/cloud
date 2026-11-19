package handlers

import (
	"log/slog"
	"net/http"

	"github.com/kiff/cloud/web/internal/content/blog"
	"github.com/kiff/cloud/web/internal/pages"
)

// Blog serves the public writing.
//
//	GET /blog             → the index, grouped by year
//	GET /blog/tags/{tag}  → the same index filtered to one tag
//	GET /blog/{slug}      → one post
//
// Posts are markdown with YAML frontmatter in internal/content/blog,
// rendered through templ via embed.FS — the same hermetic pattern
// /docs and /whitepaper use, so there is no CMS and no Node toolchain.
// Adding a post means adding one file; the conventions are documented
// in the publishing skill and enforced by the package's tests.
//
// Drafts are excluded from both routes: an unpublished post 404s rather
// than being reachable by guessing its slug.
func Blog(logger *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		slug := r.PathValue("slug")

		if tag := r.PathValue("tag"); tag != "" {
			posts, err := blog.ByTag(tag)
			if err != nil {
				logBlogErr(r, logger, tag, err)
				http.Error(w, "blog temporarily unavailable", http.StatusInternalServerError)
				return
			}
			// An empty tag view is a dead end dressed as a page, so it
			// 404s rather than rendering "0 posts".
			if len(posts) == 0 {
				http.NotFound(w, r)
				return
			}
			renderIndex(w, r, pages.BlogIndexView{
				Years: blog.ByYear(posts), Tag: tag, Total: len(posts),
			})
			return
		}

		if slug == "" {
			posts, err := blog.All()
			if err != nil {
				logBlogErr(r, logger, "", err)
				http.Error(w, "blog temporarily unavailable", http.StatusInternalServerError)
				return
			}
			renderIndex(w, r, pages.BlogIndexView{
				Years: blog.ByYear(posts), Total: len(posts),
			})
			return
		}

		post, ok, err := blog.Get(slug)
		if err != nil {
			logBlogErr(r, logger, slug, err)
			http.Error(w, "blog temporarily unavailable", http.StatusInternalServerError)
			return
		}
		if !ok {
			http.NotFound(w, r)
			return
		}

		related, err := blog.Related(post, 3)
		if err != nil {
			logBlogErr(r, logger, slug, err)
			http.Error(w, "blog temporarily unavailable", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = pages.BlogPost(pages.BlogPostView{Post: post, Body: blog.HTML(post), Related: related}).Render(r.Context(), w)
	})
}

func renderIndex(w http.ResponseWriter, r *http.Request, view pages.BlogIndexView) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = pages.BlogIndex(view).Render(r.Context(), w)
}

func logBlogErr(r *http.Request, logger *slog.Logger, slug string, err error) {
	if logger == nil {
		return
	}
	logger.ErrorContext(r.Context(), "blog render failed",
		slog.String("slug", slug), slog.String("err", err.Error()))
}
