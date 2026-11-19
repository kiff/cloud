package handlers

import (
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/kiff/cloud/web/internal/components"
	"github.com/kiff/cloud/web/internal/content/docs"
	"github.com/kiff/cloud/web/internal/content/llmstxt"
	"github.com/kiff/cloud/web/internal/content/whitepaper"
	"github.com/kiff/cloud/web/internal/pages"
)

// LLMsText serves GET /llms.txt — the curated, token-efficient map of
// KIFF's public surface for LLMs and AI agents (llmstxt.org). Served
// as text/plain markdown from the embedded file; no auth, no JS.
// Fitting for a product whose own thesis is that agentic systems
// deserve a legible operational layer: the site is legible to the
// agents that read it.
func LLMsText() http.Handler {
	body := []byte(llmsContent())
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "public, max-age=3600")
		_, _ = w.Write(body)
	})
}

// LLMsFullText serves GET /llms-full.txt — the full-text companion to
// /llms.txt for single-fetch ingestion. It is assembled once from the
// SAME sources the public pages render from (the docs markdown, the
// whitepaper, /why, and the public pages llms.txt links, rendered by
// site), so it cannot drift from the site. Only public content is
// included; no operator/admin or dashboard surface.
//
// site is the server's own handler, asked for each page as an anonymous
// visitor would; nothing goes over the network. A page that does not
// render is left out and logged.
func LLMsFullText(site http.Handler, logger *slog.Logger) http.Handler {
	full, failed := buildLLMsFull(site)
	for _, err := range failed {
		logger.Warn("llms-full.txt: page left out", slog.String("err", err.Error()))
	}
	body := []byte(full)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "public, max-age=3600")
		_, _ = w.Write(body)
	})
}

// buildLLMsFull concatenates the curated map, every public docs page's
// raw markdown (in nav order), the whitepaper, /why, and the text of the
// public pages llms.txt links into one document. Sourced from
// docs.Markdown / whitepaper.Markdown / why.Markdown and from each page
// as site renders it, so it stays in lockstep with what the site
// actually serves. It returns the pages it could not render.
func buildLLMsFull(site http.Handler) (string, []error) {
	var b strings.Builder

	// Lead with the curated map so a single fetch carries both the
	// orientation and the full text.
	b.WriteString(llmsContent())
	b.WriteString("\n\n---\n\n# Full text\n\n")
	b.WriteString("The complete text of the KIFF docs, the whitepaper and the public pages follows, for single-fetch ingestion. Source of truth: https://kiff.dev/docs, https://kiff.dev/whitepaper and each page's own address.\n")

	b.WriteString("\n\n---\n\n# Documentation\n")
	for _, p := range docs.Pages() {
		md, ok, err := docs.Markdown(p.Slug)
		if err != nil || !ok {
			continue
		}
		b.WriteString("\n\n## " + p.Title + "\n")
		b.WriteString("Source: https://kiff.dev/docs/" + p.Slug + "\n\n")
		b.WriteString(strings.TrimSpace(md))
		b.WriteString("\n")
	}

	b.WriteString("\n\n---\n\n# Whitepaper\n")
	b.WriteString("Source: https://kiff.dev/whitepaper\n\n")
	b.WriteString(strings.TrimSpace(string(whitepaper.Markdown())))
	b.WriteString("\n")

	// The public pages llms.txt links, in its order: each page's <main>
	// as the site renders it in English, as plain text, its headings
	// under the page's own "##".
	var failed []error
	pages, _ := llmsFullPages(llmsContent())
	b.WriteString("\n\n---\n\n# Pages\n")
	for _, p := range pages {
		doc, err := renderPublicPage(site, p.Path)
		if err != nil {
			failed = append(failed, err)
			continue
		}
		text := htmlMainText(doc, 2)
		if text == "" {
			failed = append(failed, fmt.Errorf("%s has no text in its <main>", p.Path))
			continue
		}
		b.WriteString("\n\n## " + p.Name + "\n")
		b.WriteString("Source: " + components.SiteBaseURL + p.Path + "\n\n")
		b.WriteString(text)
		b.WriteString("\n")
	}

	return b.String(), failed
}

// llmsContent is llms.txt as the site serves it: the embedded file, less the line for /tafs/watch while it isn't live.
func llmsContent() string {
	content := llmstxt.Content()
	if pages.WatchLive {
		return content
	}
	link := "(" + components.SiteBaseURL + pages.WatchPage + ")"
	lines := strings.Split(content, "\n")
	kept := lines[:0]
	for _, l := range lines {
		if !strings.Contains(l, link) {
			kept = append(kept, l)
		}
	}
	return strings.Join(kept, "\n")
}
