package handlers

import (
	"log/slog"
	"net/http"

	"github.com/kiff/cloud/web/internal/components"
	"github.com/kiff/cloud/web/internal/content/proofs"
	"github.com/kiff/cloud/web/internal/content/videos"
	"github.com/kiff/cloud/web/internal/pages"
)

// Proof serves the cookbook proof infographics.
//
//	GET /proof          → the index (one card per proof)
//	GET /proof/{slug}    → one infographic
//
// /proof is the public Cards at work product surface. The legacy
// /proof/{slug} infographics remain shareable pitch artifacts and stay
// noindex; they prove the domain state machine, not a KIFF Card.
func Proof(logger *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")

		slug := r.PathValue("slug")
		if slug == "" {
			_ = pages.ProofIndex(proofs.All()).Render(r.Context(), w)
			return
		}
		w.Header().Set("X-Robots-Tag", "noindex")

		p, ok := proofs.Lookup(slug)
		if !ok {
			http.NotFound(w, r)
			return
		}
		_ = pages.Proof(p).Render(r.Context(), w)
	})
}

// ProofVideo serves a contextual watch/share page for a public gallery clip.
func ProofVideo(logger *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		clip, ok := videos.Lookup(r.PathValue("video"))
		if !ok {
			http.NotFound(w, r)
			return
		}
		ctx := components.WithCanonicalPath(r.Context(), clip.PagePath())
		ctx = components.WithShareImage(ctx, clip.ShareImage(), clip.Title)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := pages.ProofVideo(clip).Render(ctx, w); err != nil {
			logger.Error("render proof video", "slug", clip.Slug, "err", err)
		}
	})
}
