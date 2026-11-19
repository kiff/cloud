package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// serveDocs runs the Docs handler against a path, setting the {slug}
// path value the same way the stdlib mux would for /docs/{slug}.
func serveDocs(t *testing.T, target, slug string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	if slug != "" {
		req.SetPathValue("slug", slug)
	}
	rec := httptest.NewRecorder()
	Docs(nil).ServeHTTP(rec, req)
	return rec
}

// TestDocsIndex: GET /docs (no slug) renders the quickstart inside the
// shared layout, with the docs sidebar.
func TestDocsIndex(t *testing.T) {
	t.Parallel()

	rec := serveDocs(t, "/docs", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want %d", rec.Code, http.StatusOK)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("content-type: got %q, want text/html", ct)
	}

	body := rec.Body.String()
	wants := []string{
		// Shared layout chrome.
		`class="site-nav"`,
		`class="site-footer"`,
		`href="/static/app.css"`,
		`href="/static/css/prose.css"`,
		`href="/static/css/docs.css"`,
		// Docs frame.
		`class="docs"`,
		`class="docs-nav"`,
		// The index leads with the Card: issue one, point the agent
		// at the gateway, answer a held call.
		"Quickstart: give an agent a Card",
		"mcp.kiff.dev/mcp",
		`/docs/approvals`,
		// Nav must list the frameworks + their badges. Every adapter
		// is verified now (OpenClaw shipped in the JS guard SDK), so
		// only the verified badge renders on the public surface.
		`docs-badge-verified`,
		`/docs/agno`,
		`/docs/openclaw`,
		// Copy-to-clipboard island for code blocks. The
		// sanctioned non-htmx JS on the public surface; assert it
		// is wired so the script tag can't silently regress.
		`<script src="/static/js/docs.js" defer></script>`,
	}
	for _, w := range wants {
		if !strings.Contains(body, w) {
			t.Errorf("index body missing %q", w)
		}
	}
}

// TestDocsFrameworkPage: a verified framework page renders with its
// active nav state and the enforce snippet.
func TestDocsFrameworkPage(t *testing.T) {
	t.Parallel()

	rec := serveDocs(t, "/docs/agno", "agno")
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want %d", rec.Code, http.StatusOK)
	}
	body := rec.Body.String()
	wants := []string{
		`class="docs"`,
		// Active nav link for the current page.
		`docs-nav-link-active`,
		// Page content.
		"Agno",
		"observe",
		"enforce",
		"agno_hook",
		// Cross-link to the dashboard journey.
		`/dashboard`,
	}
	for _, w := range wants {
		if !strings.Contains(body, w) {
			t.Errorf("agno page missing %q", w)
		}
	}
}

// A removed page's URL redirects to its replacement instead of letting
// the catch-all homepage answer a dead docs URL with a 200. The five-line
// walkthrough now lives on Connect an agent.
func TestDocsMovedPagesRedirect(t *testing.T) {
	t.Parallel()
	for slug, want := range map[string]string{
		"five-line-integration": "/docs/connect-an-agent",
		"cloud-quickstart":      "/docs/connect-an-agent",
		"protected-controls":    "/docs/domains",
	} {
		rec := serveDocs(t, "/docs/"+slug, slug)
		if rec.Code != http.StatusMovedPermanently || rec.Header().Get("Location") != want {
			t.Errorf("%s: %d → %q, want 301 → %q", slug, rec.Code, rec.Header().Get("Location"), want)
		}
	}
	rec := serveDocs(t, "/docs/connect-an-agent", "connect-an-agent")
	for _, want := range []string{"ask_kiff", "process_refund", "Call the decision API directly", "mcp-gateway"} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("connect-an-agent missing %q", want)
		}
	}
}

// TestDocsVerifiedPageLabelled: a verified page renders the verified
// badge in the nav and claims verified in the body — the honesty rule.
// (All framework adapters are verified now that OpenClaw ships in the
// JS guard SDK; there are no preview pages on the public surface. The
// preview-badge path stays covered by the content-package honesty tests
// and reactivates the moment a preview page is added back.)
func TestDocsVerifiedPageLabelled(t *testing.T) {
	t.Parallel()

	rec := serveDocs(t, "/docs/openclaw", "openclaw")
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want %d", rec.Code, http.StatusOK)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "docs-badge-verified") {
		t.Error("verified page should render a verified badge in the nav")
	}
	if !strings.Contains(strings.ToLower(body), "verified") {
		t.Error("verified page body should say verified")
	}
}

// TestDocsUnknownSlug404: an unknown slug 404s rather than rendering an
// empty page.
func TestDocsUnknownSlug404(t *testing.T) {
	t.Parallel()

	rec := serveDocs(t, "/docs/nope", "nope")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status: got %d, want %d", rec.Code, http.StatusNotFound)
	}
}

// TestDocsAllFrameworksReachable: every framework slug named in is
// reachable through the handler (no 404, real content).
func TestDocsAllFrameworksReachable(t *testing.T) {
	t.Parallel()

	slugs := []string{
		"quickstart", "framework-quickstart", "connect-an-agent", "approvals", "concepts",
		"agno", "langgraph", "hermes", "openai-agents", "openclaw",
		"pydantic-ai", "google-adk", "microsoft-agent-framework",
		"strands", "haystack", "llamaindex",
	}
	for _, slug := range slugs {
		rec := serveDocs(t, "/docs/"+slug, slug)
		if rec.Code != http.StatusOK {
			t.Errorf("slug %q: status %d, want 200", slug, rec.Code)
		}
	}
}

// The limits reference became the card reference. The old path is
// published in llms.txt and linked from blog posts that are dated
// records and must not be rewritten, so it has to resolve rather than
// 404 — the same treatment /boundary-test got when it became
// /on-rails.
func TestDocsLimitsRedirectsToTheCardReference(t *testing.T) {
	t.Parallel()
	h := http.RedirectHandler("/docs/kiff-cards", http.StatusMovedPermanently)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/docs/limits", nil))

	if rr.Code != http.StatusMovedPermanently {
		t.Errorf("status = %d, want 301", rr.Code)
	}
	if loc := rr.Header().Get("Location"); loc != "/docs/kiff-cards" {
		t.Errorf("Location = %q, want /docs/kiff-cards", loc)
	}
}
