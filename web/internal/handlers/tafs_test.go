package handlers

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

// /tafs is the product page: nine screens, each one giant word, then the
// kiff footer, with nothing it can't keep.
func TestTafs(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodGet, "/tafs", nil)
	rec := httptest.NewRecorder()
	Tafs(OfferLinks{PlayURL: "https://play.google.com/store/apps/details?id=dev.kiff.tafs"}).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want %d", rec.Code, http.StatusOK)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("content-type: got %q, want text/html", ct)
	}
	body := rec.Body.String()
	for _, want := range []string{
		"/static/css/fonts.css", "/static/css/tafs.css", "/static/js/tafs.js?v=", "/static/img/tafs/tafs-mascot-128.png?v=",
		// The screens people are sent to, and the header's links to them.
		`id="top"`, `id="pricing"`, `id="download"`, `href="#pricing"`, `href="#download"`,
		"The assistant that runs", "A taf is a job you give to an agent.", "No answer means no.",
		// The price and the models, then the store and the code.
		"€12.50 a month to start, with 500 credits, VAT included. Runs on Claude Sonnet 5.5.",
		`href="https://play.google.com/store/apps/details?id=dev.kiff.tafs"`, "App Store, coming soon",
		`href="https://github.com/kiff/tafs"`,
		// The footer.
		`href="/docs"`, `href="/security"`, `href="/whitepaper"`, `href="/privacy"`, `href="/terms"`,
		"Part of Claude for Startups.", "KIFF is not affiliated with or endorsed by Anthropic.",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q", want)
		}
	}
	// Its own header and footer, not the site's; no language menu; no
	// font from anyone else.
	for _, never := range []string{
		`class="site-nav"`, `class="site-footer"`, "<details", "<select", "fonts.googleapis.com", "fonts.gstatic.com",
		// Nothing the product or the Terms don't back, and nothing of a closed test.
		"closed test", "Closed test", "invitation", "Invitation", "Ask for access", "early access", "waitlist",
		"free during the beta", "Free during the beta", "7-day trial", "7 days free", "Testing is free", "test mode",
		"NEEDS BUILDING", "founder", "any MCP server", "unhackable", "SOC 2", "SSO", "Mac app",
		"€29 ", "€29<", "14.50", "€4.99", "Team plan", "/dashboard/checkout", "First month pack", "French slang",
		// No competitor is named.
		"Zapier", "n8n", "Make.com", "Lindy", "OpenAI", "ChatGPT", "Copilot", "Gemini", "Manus",
	} {
		if strings.Contains(body, never) {
			t.Errorf("claims %q", never)
		}
	}
	if strings.Contains(strings.ToLower(body), "teammates") {
		t.Errorf("the page calls tafs teammates")
	}
	for _, lead := range regexp.MustCompile(`(?s)<(title|h1|h2|h3)\b.*?</(title|h1|h2|h3)>|<meta name="description"[^>]*>`).FindAllString(body, -1) {
		for _, phrase := range []string{"eammate", "AI assistant"} {
			if strings.Contains(lead, phrase) {
				t.Errorf("%q leads in %q", phrase, lead)
			}
		}
	}
}

// Google's Play badge and links show only with Play's link.
func TestTafsPlayLink(t *testing.T) {
	t.Parallel()

	const play = "https://play.google.com/store/apps/details?id=example"
	for _, withPlay := range []bool{true, false} {
		o := OfferLinks{}
		if withPlay {
			o.PlayURL = play
		}
		rec := httptest.NewRecorder()
		Tafs(o).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/tafs", nil))
		body := rec.Body.String()
		for _, s := range []string{`href="` + play + `"`, `class="gpb"`, "google-play-badge.png"} {
			if strings.Contains(body, s) != withPlay {
				t.Errorf("with Play's link %v: %q shown %v", withPlay, s, !withPlay)
			}
		}
		// The store line says where tafs is, link or not.
		if !strings.Contains(body, "Android: on Google Play in 33 countries and regions.") {
			t.Errorf("with Play's link %v: no store line", withPlay)
		}
	}
}
