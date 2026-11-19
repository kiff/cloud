package handlers

import (
	"log/slog"
	"net/http"
	"strconv"

	"github.com/kiff/cloud/web/internal/content/decks"
	"github.com/kiff/cloud/web/internal/pages"
)

// Deck serves the public pitch decks.
//
//	GET /deck            → the deck index (one link per audience)
//	GET /deck/{slug}     → slide 1 of that deck
//	GET /deck/{slug}?slide=N → slide N (1-based)
//	GET /deck/{slug}?print=1 → all slides stacked for PDF export
//
// Markdown is vendored in internal/content/decks and rendered through
// templ via embed.FS — the same hermetic pattern /docs and
// /whitepaper use, so there is no slide toolchain. One slide per
// page, prev/next are plain links, no JS.
//
// Decks are noindex: they are shareable artifacts for a specific
// reader, not pages we want surfaced in search.
func Deck(logger *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Robots-Tag", "noindex")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")

		slug := r.PathValue("slug")
		if slug == "" {
			_ = pages.DeckIndex(pages.DeckIndexView{Decks: decks.Decks()}).Render(r.Context(), w)
			return
		}

		slides, ok, err := decks.Slides(slug)
		if err != nil {
			if logger != nil {
				logger.ErrorContext(r.Context(), "deck render failed",
					slog.String("slug", slug), slog.String("err", err.Error()))
			}
			http.Error(w, "deck temporarily unavailable", http.StatusInternalServerError)
			return
		}
		if !ok {
			http.NotFound(w, r)
			return
		}

		deck, _ := decks.Lookup(slug)
		total := len(slides)

		// ?print renders the whole deck stacked on one page so the
		// browser's own print dialog (⌘P) exports it to PDF — no JS,
		// no slide toolchain.
		if r.URL.Query().Has("print") {
			_ = pages.DeckPrint(pages.DeckPrintView{Deck: deck, Slides: slides}).Render(r.Context(), w)
			return
		}

		// 1-based slide number; clamp out-of-range to the ends so a
		// forwarded ?slide=N link never 404s or panics.
		n := 1
		if raw := r.URL.Query().Get("slide"); raw != "" {
			if parsed, perr := strconv.Atoi(raw); perr == nil {
				n = parsed
			}
		}
		if n < 1 {
			n = 1
		}
		if n > total {
			n = total
		}

		view := pages.DeckSlideView{
			Deck:   deck,
			Body:   slides[n-1],
			Number: n,
			Total:  total,
		}
		_ = pages.DeckSlide(view).Render(r.Context(), w)
	})
}
