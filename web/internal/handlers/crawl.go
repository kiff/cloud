package handlers

import (
	"encoding/xml"
	"net/http"
	"strings"

	"github.com/kiff/cloud/web/internal/components"
	"github.com/kiff/cloud/web/internal/content/blog"
	"github.com/kiff/cloud/web/internal/content/docs"
	"github.com/kiff/cloud/web/internal/content/videos"
	"github.com/kiff/cloud/web/internal/pages"
)

// publicPages are the indexable pages outside docs and the blog. Decks,
// spots, /proof/{slug} and /demo stay out: they answer noindex. Private
// surfaces (/dashboard, /ops, sign-in, the signed-in app) never appear.
var publicPages = []string{
	"/", "/delegate", "/pricing", "/proof", "/invariants", "/whitepaper",
	"/security", "/docs", "/blog",
	"/privacy", "/terms", "/acceptable-use", "/legal/dpa", "/subprocessors", "/cookies",
	"/legal/sla", "/support", "/tafs", "/tafs/download", "/tafs/watch", "/tafs/privacy",
	"/tafs/terms",
}

// SitemapURLs is every public URL the sitemap lists, built from the same
// docs registry and blog posts the pages render from, so a new docs page
// or post is listed without editing this file.
func SitemapURLs() []string {
	out := make([]string, 0, 64)
	for _, p := range publicPages {
		if p == pages.WatchPage && !pages.WatchLive {
			continue // not public until the watch is on Google Play
		}
		out = append(out, components.SiteBaseURL+strings.TrimSuffix(p, "/"))
	}
	out[0] = components.SiteBaseURL + "/"
	for _, tr := range TranslatedRoutes() {
		out = append(out, components.SiteBaseURL+tr.Path)
	}
	for _, p := range docs.Pages() {
		out = append(out, components.SiteBaseURL+"/docs/"+p.Slug)
	}
	if posts, err := blog.All(); err == nil {
		for _, p := range posts {
			out = append(out, components.SiteBaseURL+"/blog/"+p.Slug)
		}
	}
	for _, clip := range append(append(videos.Demos(), videos.ProductTours()...), videos.Capsules()...) {
		out = append(out, components.SiteBaseURL+clip.PagePath())
	}
	return out
}

// robotsTxt allows the public site to every crawler, including the AI
// crawlers named explicitly: KIFF wants assistants to answer from its
// own pages (see /llms.txt). The signed-in app is disallowed; noindex
// pages are left crawlable so crawlers can read their noindex header,
// except /demo: its dashboard links every filter and page as a new URL,
// and crawling those spent the crawl the public pages needed.
const robotsTxt = `# kiff.dev — public site. The signed-in app lives at app.kiff.dev.
# For language models: https://kiff.dev/llms.txt and https://kiff.dev/llms-full.txt

User-agent: *
User-agent: GPTBot
User-agent: OAI-SearchBot
User-agent: ChatGPT-User
User-agent: ClaudeBot
User-agent: Claude-SearchBot
User-agent: Claude-User
User-agent: PerplexityBot
User-agent: Perplexity-User
User-agent: Google-Extended
Allow: /
Disallow: /dashboard
Disallow: /ops
Disallow: /sign-in
Disallow: /sign-out
Disallow: /auth/
Disallow: /cards
Disallow: /agents
Disallow: /agent-cards
Disallow: /needs-you
Disallow: /tools
Disallow: /welcome
Disallow: /demo

Sitemap: https://kiff.dev/sitemap.xml
`

// Robots serves GET /robots.txt. Before this route existed the catch-all
// answered it with the landing page, so crawlers got HTML and no rules.
func Robots() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "public, max-age=3600")
		_, _ = w.Write([]byte(robotsTxt))
	})
}

type sitemapURL struct {
	Loc     string        `xml:"loc"`
	LastMod string        `xml:"lastmod,omitempty"`
	Links   []sitemapLink `xml:"xhtml:link"`
}

// sitemapLink names another language's URL for the same page, so a
// search engine shows each reader the page in their language.
type sitemapLink struct {
	Rel      string `xml:"rel,attr"`
	HrefLang string `xml:"hreflang,attr"`
	Href     string `xml:"href,attr"`
}

type sitemapURLSet struct {
	XMLName xml.Name     `xml:"urlset"`
	XMLNS   string       `xml:"xmlns,attr"`
	XHTML   string       `xml:"xmlns:xhtml,attr"`
	URLs    []sitemapURL `xml:"url"`
}

// sitemapAlternates lists a translated page's languages, for every one
// of its URLs: the languages that are served, and English as the default.
func sitemapAlternates(url string) []sitemapLink {
	_, page, ok := components.SplitLangPath(strings.TrimPrefix(url, components.SiteBaseURL))
	if !ok {
		return nil
	}
	var out []sitemapLink
	for _, a := range components.AlternatesOf(page) {
		out = append(out, sitemapLink{Rel: "alternate", HrefLang: components.LangTag(a.Lang), Href: a.URL})
		if a.Lang == "en" {
			out = append(out, sitemapLink{Rel: "alternate", HrefLang: "x-default", Href: a.URL})
		}
	}
	return out
}

// Sitemap serves GET /sitemap.xml from SitemapURLs.
func Sitemap() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		set := sitemapURLSet{XMLNS: "http://www.sitemaps.org/schemas/sitemap/0.9", XHTML: "http://www.w3.org/1999/xhtml"}
		dated := postDates()
		for _, u := range SitemapURLs() {
			set.URLs = append(set.URLs, sitemapURL{Loc: u, LastMod: dated[u], Links: sitemapAlternates(u)})
		}
		body, err := xml.MarshalIndent(set, "", "  ")
		if err != nil {
			http.Error(w, "sitemap unavailable", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/xml; charset=utf-8")
		w.Header().Set("Cache-Control", "public, max-age=3600")
		_, _ = w.Write([]byte(xml.Header))
		_, _ = w.Write(body)
	})
}

// postDates maps each blog post's URL to its date, as the sitemap's
// lastmod. Other pages carry none: a date the page didn't change on would
// only mislead a crawler.
func postDates() map[string]string {
	out := map[string]string{}
	posts, err := blog.All()
	if err != nil {
		return out
	}
	for _, p := range posts {
		if !p.Parsed.IsZero() {
			out[components.SiteBaseURL+"/blog/"+p.Slug] = p.Parsed.Format("2006-01-02")
		}
	}
	return out
}
