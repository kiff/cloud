package handlers

import (
	"log/slog"
	"net/http"

	"github.com/kiff/cloud/web/internal/content/archive"
	"github.com/kiff/cloud/web/internal/pages"
)

// DocsArchive serves the dated documentation archive:
//
//	GET /docs/archive                         → every revision
//	GET /docs/archive/{rev}                   → one revision's pages
//	GET /docs/archive/{rev}/{slug}            → an archived page
//	GET /docs/archive/{rev}/assets/{name}     → an image an archived page shows
//
// Revisions are embedded and verified against their pinned SHA-256 before
// anything is served (internal/content/archive); one that no longer
// matches is an error, never a page.
func DocsArchive(logger *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fail := func(err error) {
			if logger != nil {
				logger.ErrorContext(r.Context(), "docs archive unavailable", slog.String("err", err.Error()))
			}
			http.Error(w, "docs archive temporarily unavailable", http.StatusInternalServerError)
		}
		id := r.PathValue("rev")
		if id == "" {
			revs, err := archive.Revisions()
			if err != nil {
				fail(err)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_ = pages.ArchiveIndex(revs).Render(r.Context(), w)
			return
		}
		rev, ok, err := archive.Lookup(id)
		if err != nil {
			fail(err)
			return
		}
		if !ok {
			http.NotFound(w, r)
			return
		}
		if name := r.PathValue("name"); name != "" {
			a, b, ok := rev.Asset(name)
			if !ok {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", a.ContentType)
			// A revision never changes, so neither does its image.
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			_, _ = w.Write(b)
			return
		}
		slug := r.PathValue("slug")
		if slug == "" {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_ = pages.ArchiveRevision(rev).Render(r.Context(), w)
			return
		}
		p, body, ok := rev.Page(slug)
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = pages.ArchivedPage(rev, p, string(body)).Render(r.Context(), w)
	})
}
