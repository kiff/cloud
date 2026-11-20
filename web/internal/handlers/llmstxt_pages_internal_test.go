package handlers

import (
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/kiff/cloud/web/internal/content/llmstxt"
)

// LLMsFullTestSite is the public site as cmd/web mounts and wraps it, for
// the routes llms.txt links: the same handler constructors, given what an
// unconfigured local server gives them, behind the same middleware.
// Exported for the handlers_test package.
func LLMsFullTestSite() http.Handler {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	var offer OfferLinks
	mux := http.NewServeMux()
	mux.Handle("GET /", AppHome("", nil, Landing()))
	mux.Handle("GET /delegate", Delegate())
	mux.Handle("GET /invariants", Invariants())
	mux.Handle("GET /blog/{slug}", Blog(logger))
	mux.Handle("GET /proof", Proof(logger))
	mux.Handle("GET /proof/videos/{video}", ProofVideo(logger))
	mux.Handle("GET /security", Security())
	mux.Handle("GET /pricing", Pricing(PricingLinks{Offer: offer}))
	mux.Handle("GET /privacy", Privacy())
	mux.Handle("GET /terms", Terms())
	mux.Handle("GET /legal/dpa", DPA())
	mux.Handle("GET /legal/sla", SLA())
	mux.Handle("GET /acceptable-use", AcceptableUse())
	mux.Handle("GET /subprocessors", Subprocessors())
	mux.Handle("GET /privacy/countries", CountryIndex())
	mux.Handle("GET /cookies", Cookies())
	mux.Handle("GET /support", Support())
	mux.Handle("GET /tafs", Tafs(offer))
	mux.Handle("GET /tafs/download", Download(offer, false))
	mux.Handle("GET /tafs/watch", TafsWatch())
	mux.Handle("GET /tafs/privacy", TafsPrivacy())
	mux.Handle("GET /tafs/terms", TafsTerms())
	return SameOriginOnly(NormalizeTypography(CanonicalAppHost("")(CanonicalPath(TrailingSlash(mux)))))
}

// Every page llms.txt links renders and lands in /llms-full.txt; none is
// quietly left out.
func TestBuildLLMsFull_RendersEveryLinkedPage(t *testing.T) {
	t.Parallel()
	_, failed := buildLLMsFull(LLMsFullTestSite())
	for _, err := range failed {
		t.Errorf("page left out of llms-full.txt: %v", err)
	}
}

// Each kiff.dev link in llms.txt is either a page llms-full.txt renders or
// excluded with a reason, and the exclusions are only the ones meant: the
// docs, the whitepaper and /why (carried from their markdown), files, and
// /invariants (test names and file paths, see llmsExclusion).
func TestLLMsFullPages_EveryLinkIsAPageOrExcluded(t *testing.T) {
	t.Parallel()
	content := llmstxt.Content()
	pages, excluded := llmsFullPages(content)

	unique := map[string]bool{}
	for _, m := range llmsLink.FindAllStringSubmatch(content, -1) {
		p := strings.TrimPrefix(m[2], "https://kiff.dev")
		if p == "" {
			p = "/"
		}
		unique[p] = true
	}
	if got := len(pages) + len(excluded); got != len(unique) {
		t.Errorf("%d pages + %d excluded = %d, want the %d distinct kiff.dev links in llms.txt", len(pages), len(excluded), got, len(unique))
	}
	for _, e := range excluded {
		if e.Reason == "" {
			t.Errorf("%s is excluded without a reason", e.Path)
		}
		ok := e.Path == "/whitepaper" || e.Path == "/why" || strings.HasPrefix(e.Path, "/docs/") ||
			e.Path == "/llms-full.txt" || strings.HasPrefix(e.Path, "/skills/") || e.Path == "/invariants"
		if !ok {
			t.Errorf("%s is excluded (%s); only docs, the whitepaper, /why and files are", e.Path, e.Reason)
		}
	}
	if len(pages) == 0 || pages[0].Path != "/" {
		t.Fatalf("pages = %v, want llms.txt's order, starting with /", pages)
	}
	for _, p := range pages {
		if strings.HasPrefix(p.Path, "/docs") || p.Path == "/why" || p.Path == "/whitepaper" {
			t.Errorf("%s is carried as a page and again from its markdown", p.Path)
		}
	}
}

func TestHTMLMainText(t *testing.T) {
	t.Parallel()
	if got := htmlMainText("<main><h1>A</h1><h2>B</h2><h6>C</h6></main>", 2); got != "### A\n\n#### B\n\n###### C" {
		t.Errorf("shifted headings = %q", got)
	}
	for _, broken := range []string{"<main>a</", "<main>a<", "<main><p", "<main>x</main"} {
		_ = htmlMainText(broken, 0) // must not panic on truncated markup
	}
	doc := `<!DOCTYPE html><html><head><title>T &amp; t</title><style>main{x:1}</style></head><body>
<a class="skip-link" href="#main">Skip to content</a>
<nav><a href="/sign-in">Sign in</a></nav>
<main id="main" tabindex="-1">
  <nav class="lang-row"><a href="/de/tafs">Deutsch</a></nav>
  <!-- a comment <p>not text</p> -->
  <h1>One   <em>title</em></h1>
  <p>Read the <a href="https://kiff.dev/dashboard">pricing&nbsp;page</a> &mdash; it&#39;s short.</p>
  <script>if (a < b && c) { document.write("<p>no</p>") }</script>
  <svg viewBox="0 0 1 1"><text>no svg</text></svg>
  <span aria-hidden="true">→ <span>hidden too</span></span>
  <div hidden><p>not shown</p></div>
  <ul><li>First</li><li><p>Second</p></li></ul>
  <h2>Table</h2>
  <table><tr><th>Plan</th><th>Price</th></tr><tr><td>Solo</td><td>€12.50</td></tr></table>
  <pre><code>kiff verify
  --domain x</code></pre>
  <div><main class="lp-app-main"><p>Inner main</p></main><p>After inner</p></div>
  <p><b>Solo</b><span>€12.50</span><b>x</b>a <a href="/m">month</a>.</p>
  <img src="/x.png" alt="no alt"><br>
  <button type="button">Approve once</button>
</main>
<footer><p>Footer text</p></footer>
</body></html>`
	got := htmlMainText(doc, 0)
	want := "# One title\n\n" +
		"Read the pricing page — it's short.\n\n" +
		"- First\n- Second\n\n" +
		"## Table\n\n" +
		"Plan | Price\n\n" +
		"Solo | €12.50\n\n" +
		"```\nkiff verify\n  --domain x\n```\n\n" +
		"Inner main\n\n" +
		"After inner\n\n" +
		"Solo €12.50 x a month.\n\n" +
		"Approve once"
	if got != want {
		t.Errorf("htmlMainText =\n%s\n\nwant\n%s", got, want)
	}
	for _, banned := range []string{"Skip to content", "Sign in", "Deutsch", "not text", "no svg", "hidden too", "not shown", "Footer", "/dashboard", "http", "document.write"} {
		if strings.Contains(got, banned) {
			t.Errorf("htmlMainText kept %q", banned)
		}
	}
}
