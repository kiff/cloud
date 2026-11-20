package handlers_test

import (
	"github.com/kiff/cloud/web/internal/pages"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/kiff/cloud/web/internal/handlers"
)

// llmsFull is /llms-full.txt as cmd/web builds it, from the public site's
// own handlers, built once per test binary.
var llmsFull = sync.OnceValue(func() http.Handler {
	return handlers.LLMsFullText(handlers.LLMsFullTestSite(), slog.New(slog.NewTextHandler(io.Discard, nil)))
})

func llmsFullBody(t *testing.T) string {
	t.Helper()
	rec := httptest.NewRecorder()
	llmsFull().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/llms-full.txt", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	return rec.Body.String()
}

// llmsPagesSection is the "# Pages" part of /llms-full.txt.
func llmsPagesSection(t *testing.T) string {
	t.Helper()
	body := llmsFullBody(t)
	i := strings.Index(body, "\n# Pages\n")
	if i < 0 {
		t.Fatal("llms-full.txt has no # Pages section")
	}
	return body[i:]
}

// llmsPageText is one page's text in the Pages section, from its Source
// line to the next page.
func llmsPageText(t *testing.T, path string) string {
	t.Helper()
	pages := llmsPagesSection(t)
	src := "\nSource: https://kiff.dev" + path + "\n"
	i := strings.Index(pages, src)
	if i < 0 {
		t.Fatalf("llms-full.txt carries no page %s", path)
	}
	text := pages[i+len(src):]
	if j := strings.Index(text, "\n## "); j >= 0 {
		text = text[:j]
	}
	return text
}

// TestLLMsText_ServesCuratedMap asserts /llms.txt serves the curated
// public map as text/plain and covers the three product surfaces
// (framework, kiff-guard, Cloud) the file is meant to make legible.
func TestLLMsText_ServesCuratedMap(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()
	handlers.LLMsText().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/llms.txt", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("Content-Type = %q, want text/plain", ct)
	}
	body := rec.Body.String()
	for _, want := range []string{
		"# KIFF",
		// The opening sentence is the machine-readable positioning: a
		// model that reads only this line has to come away with the
		// instrument, not with a category.
		// KIFF decides everything; the card is the extra gate where the
		// domain asks for one. A model reading only the opening must
		// not come away thinking a card gates every action.
		"KIFF decides every proposed consequential action",
		"Where an action declares `requires_authority`",
		"a card the organization issued to the acting agent",
		"github.com/kiff/kiff",       // the framework
		"github.com/kiff/kiff-guard", // the on-ramp
		"app.kiff.dev",               // Cloud
		"/docs/quickstart",
		"/docs/concepts",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("llms.txt missing %q", want)
		}
	}
}

// TestLLMsText_NeverLeaksPrivateSurfaces guards the one rule that
// matters most: the curated map must list only public pages and must
// never expose the operator/admin surface (/ops) or authenticated
// dashboard routes.
func TestLLMsText_NeverLeaksPrivateSurfaces(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()
	handlers.LLMsText().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/llms.txt", nil))
	body := rec.Body.String()

	for _, banned := range []string{
		"/ops",       // the private admin surface
		"/dashboard", // authenticated app routes
		"/sign-in",
	} {
		if strings.Contains(body, banned) {
			t.Errorf("llms.txt leaks a private surface: %q", banned)
		}
	}
}

// TestLLMsFullText_AssemblesDocsAndWhitepaper asserts /llms-full.txt
// concatenates the curated map, the docs (real content from the
// embedded markdown), and the whitepaper into one text/plain document.
func TestLLMsFullText_AssemblesDocsAndWhitepaper(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()
	llmsFull().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/llms-full.txt", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("Content-Type = %q, want text/plain", ct)
	}
	body := rec.Body.String()
	for _, want := range []string{
		"# KIFF",                           // the curated-map header leads
		"# Documentation",                  // the docs corpus section
		"# Whitepaper",                     // the whitepaper section
		"# Pages",                          // the public pages' text
		"https://kiff.dev/docs/quickstart", // per-page source attribution
	} {
		if !strings.Contains(body, want) {
			t.Errorf("llms-full.txt missing %q", want)
		}
	}
	// Substantially larger than the curated map — it carries real
	// content, not just links.
	if len(body) < 5000 {
		t.Errorf("llms-full.txt is only %d bytes; expected the full docs corpus", len(body))
	}
}

// TestLLMsFullText_NeverLeaksAdminSurface extends the guarantee that
// matters to the full-text file: the only genuinely private surface is
// the operator/admin console (/ops); concatenating the docs must never
// pull it in. (/dashboard and /sign-in are public product surfaces
// that docs prose may legitimately reference, so they're not banned
// here — unlike the curated map, which we author and keep link-free.)
func TestLLMsFullText_NeverLeaksAdminSurface(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()
	llmsFull().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/llms-full.txt", nil))
	if strings.Contains(rec.Body.String(), "/ops") {
		t.Error("llms-full.txt leaks the private admin surface: /ops")
	}
}

// "The reality layer for agentic systems" is the one sentence a
// vertically integrated platform answers with "yes, that is what our
// ontology is for" — and then wins, because it has spent years and a
// great deal of money being exactly that. KIFF's defensible claim is
// narrower: an independent boundary that does not ask you to move your
// operational model anywhere first.
//
// This guards the machine-readable surface, which is what other agents
// read when someone asks them what KIFF is.
func TestLLMsTxtDoesNotClaimToBeTheRealityLayer(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()
	handlers.LLMsText().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/llms.txt", nil))
	if strings.Contains(strings.ToLower(rec.Body.String()), "reality layer") {
		t.Error(`llms.txt claims to be "the reality layer" — that is the ontology claim, ` +
			`and it invites a comparison against a platform built to win it. ` +
			`Describe the boundary instead.`)
	}
}

// llms-full.txt is assembled from llms.txt, the docs corpus and the
// whitepaper, so it cannot be edited directly — it is only ever as
// current as those are. This asserts the assembly actually carries the
// card through, which is the reason it had to be regenerated last.
func TestLLMsFullTextCarriesTheCardVocabulary(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()
	llmsFull().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/llms-full.txt", nil))
	body := rec.Body.String()

	for _, want := range []string{
		// From the curated map.
		"KIFF decides every proposed consequential action",
		// From the card reference, which is the docs page a model
		// needs in order to answer what a card is.
		"# KIFF Cards",
		"no_mandate_issued",
		"mandate_limit_reached",
		"can also be **shared** with up to 20 agents",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("llms-full.txt missing %q", want)
		}
	}
	// The product paper and curated map must not revive the retired
	// opening claim.
	if strings.Contains(body, "KIFF is the boundary a consequential action has to cross") {
		t.Error("the retired positioning sentence is still being served to models")
	}
}

// Assistants that fetch llms-full.txt may stop reading early. Everything
// a correct answer needs (what a Card is, how a call is checked, what
// happens outside it, who it is for, what is shipped) must sit in the
// first 8 KB, before the long docs text.
func TestLLMsFullTextAnswersInItsFirst8KB(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()
	llmsFull().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/llms-full.txt", nil))
	body := rec.Body.String()
	if len(body) > 8192 {
		body = body[:8192]
	}
	for _, want := range []string{
		"KIFF gives each AI agent a Card",
		"payment networks cap a payment; KIFF caps the agent",
		"mcp.kiff.dev",
		"POST /v1/proposals/decide",
		"the owner answers",
		"Not sold to consumers using a personal agent",
		"Direction, not product",
		"MIT",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("first 8 KB of llms-full.txt missing %q", want)
		}
	}
}

// The pages' text is the part of llms-full.txt this repo renders rather
// than authors, so it carries the same ban as the curated map: no private
// surface, and nothing of the markup (a link's href, a script, an svg).
// The docs section is not held to it: its markdown links the dashboard
// (TestLLMsFullText_NeverLeaksAdminSurface covers /ops for the file).
func TestLLMsFullText_PagesNeverLeakPrivateSurfaces(t *testing.T) {
	t.Parallel()
	pages := llmsPagesSection(t)
	for _, banned := range []string{
		"/ops", "/dashboard", "/sign-in",
		"href=", "<a ", "<script", "<svg", "</",
	} {
		if strings.Contains(pages, banned) {
			t.Errorf("the Pages section of llms-full.txt contains %q", banned)
		}
	}
}

// Each public page llms.txt links is carried, in llms.txt's order, with a
// sentence that is on it today, so a page that silently drops out (or
// renders empty) fails here. A page added to llms.txt needs a line here.
func TestLLMsFullText_CarriesEachPublicPage(t *testing.T) {
	t.Parallel()
	want := []struct{ path, phrase string }{
		{"/", "Give an agent a Card: what it may do, and how much."},
		{"/tafs", "A taf is a job you give to an agent."},
		{"/tafs/download", "Get tafs"},
		{"/tafs/watch", "Approving is never done on the watch."},
		{"/tafs/privacy", "including the Limited Use requirements."},
		{"/tafs/terms", "KIFF's Terms apply to anything this page doesn't cover."},
		{"/proof/videos/mcp-from-zero-en", "Connect your agent through KIFF MCP and watch your Card control what happens next."},
		{"/proof/videos/mcp-from-zero-fr", "Connectez votre agent via KIFF MCP"},
		{"/proof/videos/customer-ops-portrait-en", "The whole flow in 58 seconds, made for your phone."},
		{"/proof/videos/customer-ops-landscape-en", "The landscape version for presentations and larger screens."},
		{"/proof", "A synthetic test run on KIFF’s production services, with no customer data."},
		{"/delegate", "A skill is what an agent can do. A Card is how far you let it go"},
		{"/pricing", "One account covers the tafs app and your own agents."},
		{"/blog/from-a-refund-tool-to-a-governed-mcp-agent", "Build a Stripe test-mode MCP tool, connect it to KIFF"},
		{"/blog/tafs-ai-teammates-inside-limits-you-set", "tafs turns a routine job you describe in a sentence into an AI teammate"},
		{"/blog/tafs-from-one-sentence-to-a-refund", "refunded a €0.50 test payment"},
		{"/blog/a-refund-desk-for-shopify-shops-part-2", "The refund desk from part 1, run on a Shopify development store"},
		{"/blog/an-approved-refund-can-still-fail", "KIFF records the tool’s response beside its authorization decision"},
		{"/security", "If a claim here cannot be verified by an external party, we name it as a roadmap item rather than a guarantee."},
		{"/privacy", "This policy explains how KIFF Cloud processes personal data"},
		{"/privacy/countries", "Some countries' laws ask for more"},
		{"/terms", "These terms govern use of KIFF Cloud, including tafs."},
		{"/acceptable-use", "This policy is part of the Terms of Service (§5)"},
		{"/legal/dpa", "This is a published template, not a signed contract."},
		{"/subprocessors", "The companies Kiff Agents OÜ uses to run KIFF Cloud and tafs"},
		{"/cookies", "Which cookies kiff.dev and app.kiff.dev set, what each one is for"},
		{"/legal/sla", "This agreement is part of the Terms of Service (§14)"},
		{"/support", "For KIFF and for tafs, KIFF's app for iPhone, iPad and Android."},
	}
	if !pages.WatchLive {
		// Held back until the watch is on Google Play (TestTafsWatchIsHeldBackUntilLive).
		kept := want[:0]
		for _, w := range want {
			if w.path != pages.WatchPage {
				kept = append(kept, w)
			}
		}
		want = kept
	}
	var got []string
	for _, line := range strings.Split(llmsPagesSection(t), "\n") {
		if p, ok := strings.CutPrefix(line, "Source: https://kiff.dev"); ok {
			got = append(got, p)
		}
	}
	if len(got) != len(want) {
		t.Errorf("the Pages section carries %d pages %v, want %d", len(got), got, len(want))
	}
	for i, w := range want {
		if i < len(got) && got[i] != w.path {
			t.Errorf("page %d is %s, want %s (llms.txt's order)", i, got[i], w.path)
		}
		if text := llmsPageText(t, w.path); !strings.Contains(text, w.phrase) {
			t.Errorf("%s in llms-full.txt is missing %q", w.path, w.phrase)
		}
	}
}

// The /tafs text is generated from the page's English words file, not
// written for llms-full.txt: every screen's heading and line in
// tafs_words_en.go is in it, as the page shows it. Reads the source with
// go/parser so the test cannot drift from it.
func TestLLMsFullText_TafsTextComesFromItsWordsFile(t *testing.T) {
	t.Parallel()
	file, err := parser.ParseFile(token.NewFileSet(), "../pages/tafs_words_en.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	// The screens' fields the page renders as written (Price's line is a
	// format, filled in with the prices).
	fields := map[string][]string{"Taf": {"Big", "Small"}, "Problem": {"Big", "Small"}, "Different": {"Big", "Small"},
		"Why": {"Big"}, "Price": {"Big"}, "Get": {"Big", "Small"}}
	var words []string
	ast.Inspect(file, func(n ast.Node) bool {
		kv, ok := n.(*ast.KeyValueExpr)
		if !ok {
			return true
		}
		key, ok := kv.Key.(*ast.Ident)
		if !ok || fields[key.Name] == nil {
			return true
		}
		screen, ok := kv.Value.(*ast.CompositeLit)
		if !ok {
			return true // the Card's Why is a line, not a screen
		}
		for _, elt := range screen.Elts {
			f, ok := elt.(*ast.KeyValueExpr)
			if !ok || !slices.Contains(fields[key.Name], f.Key.(*ast.Ident).Name) {
				continue
			}
			if lit, ok := f.Value.(*ast.BasicLit); ok {
				if s, err := strconv.Unquote(lit.Value); err == nil && strings.TrimSpace(s) != "" {
					words = append(words, s)
				}
			}
		}
		return false
	})
	if len(words) < 6 {
		t.Fatalf("read %d strings from tafs_words_en.go, want the screens' headings", len(words))
	}
	text := strings.Join(strings.Fields(llmsPageText(t, "/tafs")), " ")
	for _, w := range words {
		// As the site serves it: its typography pass turns a spaced dash
		// into a comma and any other into a hyphen.
		w = strings.ReplaceAll(strings.ReplaceAll(w, " — ", ", "), "—", "-")
		if w = strings.Join(strings.Fields(w), " "); !strings.Contains(text, w) {
			t.Errorf("/tafs in llms-full.txt is missing %q from tafs_words_en.go", w)
		}
	}
}
