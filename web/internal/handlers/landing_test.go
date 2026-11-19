package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestLanding checks that the homepage opens on giving an agent a Card and
// keeps the supporting sections reachable. The old boundary-review
// anchor remains for inbound links.
func TestLanding(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()

	Landing().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want %d", rec.Code, http.StatusOK)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("content-type: got %q, want text/html", ct)
	}

	body := rec.Body.String()
	wants := []string{
		// What KIFF is, and where to use it.
		"Limits for AI agents, <span>set by people.</span>",
		"Give an agent a Card: what it may do, and how much. Inside the Card it works. Past it, the work waits for your approval.",
		`href="/tafs"`, `href="/docs/quickstart"`,

		// How it works, in three steps.
		"How it works", "Set the limits.", "It works inside them.", "You answer the rest.",
		"KIFF checks every call outside the AI, before it reaches your tools.",
		"No answer means no.",

		// Why a Card (the founder's /why, in two lines).
		`id="why"`, "Why a Card", "a Card puts them outside the agent, where its owner can read and change them.", `href="/whitepaper"`,

		// One line of proof.
		"Each guarantee is tied to the test that checks it.", `href="/invariants"`,

		// One call to action: the sample walkthrough, before any sign-in.
		`href="/tafs/download" class="k-btn k-btn-primary">Download`, `href="/delegate"`,
		`href="/pricing"`,

		// The footer says it in one line.
		`<p class="site-footer-tagline">Limits for AI agents, set by people.</p>`,

		// Shared chrome.
		`class="site-nav"`,
		`class="site-logo"`,
		`class="site-nav-cta"`,
		// The nav's button teaches too, on every page: the walkthrough, not
		// sign-in and the Card form.
		`<a href="/delegate" class="site-nav-cta">see how a card works →</a>`,
		`class="site-footer"`,
		`href="/docs"`,

		// Head-level machine hint for the LLM/agent map (so crawlers
		// don't have to rely on the footer link or probe by convention).
		`<link rel="alternate" type="text/plain" href="/llms.txt"`,

		// Stylesheets.
		`href="/static/open-props.min.css"`,
		`href="/static/app.css"`,
		`href="/static/css/fonts.css"`,
		`href="/static/css/site.css?v=20261110-1"`,
		`id="site-nav-open"`,
		`for="site-nav-open"`,
		`href="/static/css/landing.css?v=20261002-1"`,
		`href="/static/css/home.css`,

		// Body carries the paper-theme scope class.
		`<body class="site">`,
	}
	for _, want := range wants {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q", want)
		}
	}

	mustNotContain := []string{
		// Pre- accounting: an approved refund counts toward the totals.
		"listed separately on its statement",
		// Private repo / implementation disclosures stay off the page.
		"github.com/kiff/kiff-cloud",
		"kiff/kiff-cloud",
		// Banned cloud-surface vocabulary on the marketing page.
		"Merkle",
		"kiff-verify",
		"Base Sepolia",
		"SOC 2 certified",
		"self-serve billing portal",
		// Claims the product does not support yet.
		"hundreds or thousands",
		"Every agent gets its own Card",
		// Notary/attestation framing is retired.
		"can't kiff this",
		"standing attestation",
		// Google Fonts must be self-hosted (hermetic, the
		// web-stack design).
		"fonts.googleapis.com",
		"fonts.gstatic.com",
		// The retired airfield metaphor is gone.
		"air traffic control",
	}
	for _, banned := range mustNotContain {
		if strings.Contains(body, banned) {
			t.Errorf("body contains forbidden phrase %q", banned)
		}
	}
}

// TestLandingOrder pins the short page: why KIFF exists, the two ways in,
// what a Card does, the real app with sample data, one plan, then the
// assistants that answer from llms-full.txt.
func TestLandingOrder(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	Landing().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	body := rec.Body.String()

	order := []string{`class="k-hero k-mission"`, `class="k-why k-points"`, `id="why"`, `class="k-proof-line"`, `class="k-offer"`}
	last := -1
	for _, marker := range order {
		i := strings.Index(body, marker)
		if i < 0 {
			t.Fatalf("missing %s", marker)
		}
		if i < last {
			t.Errorf("%s is out of order", marker)
		}
		last = i
	}
}

func TestLandingExampleQueryStillWorks(t *testing.T) {
	t.Parallel()

	for _, q := range []string{"", "?example=refund-agno", "?example=ai-cafe-ops", "?example=nope"} {
		req := httptest.NewRequest(http.MethodGet, "/"+q, nil)
		rec := httptest.NewRecorder()

		Landing().ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Errorf("status for %q: got %d, want 200", q, rec.Code)
		}
	}
}

// TestOpenBetaRetired: the open beta ended on 26 Oct, and the trial with
// credits (pricing phase 2): an account pays from its first day (/pricing).
// No page, header or footer says it's in open beta, free to try, or starts
// with a trial.
func TestOpenBetaRetired(t *testing.T) {
	t.Parallel()
	for path, h := range map[string]http.Handler{"/": Landing()} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		body := strings.ToLower(rec.Body.String())
		for _, retired := range []string{"open beta", "free to try", "free while", "7-day trial", "free week"} {
			if strings.Contains(body, retired) {
				t.Errorf("%s still says %q", path, retired)
			}
		}
	}
}

// The page opens on the mission and names the family, tafs first, with
// links only to public repos.
func TestLandingMissionAndFamily(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	Landing().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	body := rec.Body.String()

	for _, want := range []string{
		"Limits for AI agents,", `href="/tafs"`, `href="https://github.com/kiff/kiff"`,
	} {
		if !strings.Contains(strings.ReplaceAll(body, "&#39;", "'"), want) {
			t.Errorf("missing %q", want)
		}
	}
	if strings.Count(body, "<h1") != 1 {
		t.Errorf("want exactly one h1, got %d", strings.Count(body, "<h1"))
	}
	if strings.Contains(body, "github.com/kiff/kiff-cloud") {
		t.Error("links the private repo")
	}
}
