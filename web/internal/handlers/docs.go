package handlers

import (
	"html/template"
	"log/slog"
	"net/http"

	"github.com/kiff/cloud/web/internal/content/docs"
	"github.com/kiff/cloud/web/internal/pages"
)

// Docs serves the public documentation site.
//
//	GET /docs → the quickstart (the <5-minute path)
//	GET /docs/{slug}    → a docs page (concepts + one per framework)
//
// Markdown is vendored in internal/content/docs and rendered through
// templ via embed.FS — the same hermetic pattern /whitepaper uses, so
// there is no Node/Docusaurus toolchain. The narrative is the source
// of truth here; runnable code lives in kiff/kiff-guard and is
// linked, not duplicated.
func Docs(logger *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		slug := r.PathValue("slug")
		if slug == "" {
			slug = docs.IndexSlug
		}
		if to, moved := docs.Moved[slug]; moved {
			http.Redirect(w, r, "/docs/"+to, http.StatusMovedPermanently)
			return
		}

		body, ok, err := docs.HTML(slug)
		if err != nil {
			if logger != nil {
				logger.ErrorContext(r.Context(), "docs render failed",
					slog.String("slug", slug), slog.String("err", err.Error()))
			}
			http.Error(w, "docs temporarily unavailable", http.StatusInternalServerError)
			return
		}
		if !ok {
			http.NotFound(w, r)
			return
		}

		page, _ := docs.Lookup(slug)

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = pages.Docs(buildDocsView(page, body)).Render(r.Context(), w)
	})
}

// buildDocsView assembles the nav (grouped, in display order) plus the
// active page + rendered body for the template.
func buildDocsView(active docs.Page, body template.HTML) pages.DocsView {
	var groups []pages.DocsNavGroup
	for _, g := range docs.Groups() {
		items := docs.PagesIn(g)
		if len(items) == 0 {
			continue
		}
		groups = append(groups, pages.DocsNavGroup{
			Label: string(g),
			Items: items,
		})
	}
	return pages.DocsView{
		Active:    active,
		Body:      body,
		NavGroups: groups,
	}
}
