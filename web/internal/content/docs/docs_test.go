package docs

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/kiff/cloud/web/internal/content/apps"
)

// TestEveryPageHasMarkdown is the load-time invariant: every slug in
// the registry must have a backing markdown file, or load records an
// initErr. requires "every framework has a real page" — this fails the
// build if a registry entry is added without its file.
func TestEveryPageHasMarkdown(t *testing.T) {
	t.Parallel()

	for _, p := range Pages() {
		h, ok, err := HTML(p.Slug)
		if err != nil {
			t.Fatalf("HTML(%q): %v", p.Slug, err)
		}
		if !ok {
			t.Errorf("slug %q has no rendered markdown", p.Slug)
			continue
		}
		if len(strings.TrimSpace(string(h))) < 200 {
			t.Errorf("slug %q rendered suspiciously short (%d bytes) — empty or truncated page", p.Slug, len(h))
		}
		// Every page should render at least one heading.
		if !strings.Contains(string(h), "<h1") {
			t.Errorf("slug %q rendered without an <h1>", p.Slug)
		}
	}
}

// TestAllElevenFrameworksPresent pins the requirement: a real page
// for every one of the eleven named frameworks, plus the two
// start-here pages.
func TestAllElevenFrameworksPresent(t *testing.T) {
	t.Parallel()

	wantFrameworks := []string{
		"agno", "langgraph", "hermes", "openai-agents", "openclaw",
		"pydantic-ai", "google-adk", "microsoft-agent-framework",
		"strands", "haystack", "llamaindex",
	}
	got := map[string]Page{}
	for _, p := range PagesIn(GroupFrameworks) {
		if p.Status == StatusStable {
			continue // the group's guide (How kiff-guard works), not an adapter
		}
		got[p.Slug] = p
	}
	if len(got) != len(wantFrameworks) {
		t.Errorf("framework count: got %d, want %d", len(got), len(wantFrameworks))
	}
	for _, slug := range wantFrameworks {
		if _, ok := got[slug]; !ok {
			t.Errorf("missing framework page: %q", slug)
		}
	}

	// The docs lead with the Card: what it is and what it is for, then
	// what happens around it, then the ways an agent connects.
	for g, want := range map[Group][]string{
		GroupStart:   {"what-kiff-is", "quickstart", "kiff-cards", "cards-in-practice"},
		GroupCards:   {"approvals", "card-limits", "external-approvers"},
		GroupConnect: {"connect-an-agent", "mcp-gateway", "tool-permits", "agent-skill"},
	} {
		var got []string
		for _, p := range PagesIn(g) {
			got = append(got, p.Slug)
		}
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("%s pages: got %v, want %v", g, got, want)
		}
	}
	if p, ok := Lookup("concepts"); !ok || p.Group != GroupFrameworks {
		t.Error("How kiff-guard works should lead the Frameworks group")
	}
}

// TestStatusLabelling guards the verified-vs-preview honesty rule:
// the shipped adapters are verified — including OpenClaw, now that the
// JS guard SDK ships the before_tool_call adapter and it passes the
// ported conformance suite; the start-here pages carry no status badge.
func TestStatusLabelling(t *testing.T) {
	t.Parallel()

	wantVerified := map[string]bool{
		"agno": true, "langgraph": true, "hermes": true, "openai-agents": true,
		"google-adk": true, "pydantic-ai": true, "strands": true,
		"haystack": true, "microsoft-agent-framework": true, "llamaindex": true,
		"openclaw": true,
	}
	for _, p := range PagesIn(GroupFrameworks) {
		if p.Slug == "concepts" && p.Status == StatusStable {
			continue
		}
		if wantVerified[p.Slug] {
			if p.Status != StatusVerified {
				t.Errorf("framework %q: got status %q, want verified", p.Slug, p.Status)
			}
			continue
		}
		if p.Status != StatusPreview {
			t.Errorf("framework %q: got status %q, want preview", p.Slug, p.Status)
		}
	}
	for _, p := range PagesIn(GroupStart) {
		if p.Status != StatusStable {
			t.Errorf("start-here page %q should carry no status, got %q", p.Slug, p.Status)
		}
	}
}

// TestPreviewPagesAreHonest: a preview page must not present itself as
// a shipped/runnable integration. It must say "preview" and "not
// shipped" (or "not yet shipped"), and must NOT claim "verified".
func TestPreviewPagesAreHonest(t *testing.T) {
	t.Parallel()

	for _, p := range PagesIn(GroupFrameworks) {
		if p.Status != StatusPreview {
			continue
		}
		h, _, err := HTML(p.Slug)
		if err != nil {
			t.Fatalf("HTML(%q): %v", p.Slug, err)
		}
		body := strings.ToLower(string(h))
		if !strings.Contains(body, "preview") {
			t.Errorf("preview page %q never says 'preview'", p.Slug)
		}
		if !strings.Contains(body, "not yet shipped") && !strings.Contains(body, "not shipped") {
			t.Errorf("preview page %q does not flag that the adapter is not shipped", p.Slug)
		}
	}
}

// TestQuickstartInstallIsHonest: the quickstart now leads with the
// framework scaffold path, so its install must be the real, working `go
// install.../kiff@latest` command and the scaffold command that
// actually ships (kiff new -scenario refund). It must NOT show a bare
// `pip install kiff-guard`: the scaffold path needs no guard, and the
// guard on-ramp lives on the connect-an-agent page.
func TestQuickstartInstallIsHonest(t *testing.T) {
	t.Parallel()

	h, _, err := HTML("framework-quickstart")
	if err != nil {
		t.Fatalf("HTML(framework-quickstart): %v", err)
	}
	body := string(h)
	if !strings.Contains(body, "go install github.com/kiff/kiff/cmd/kiff@latest") {
		t.Error("quickstart should use the real go install command for the scaffold path")
	}
	if !strings.Contains(body, "kiff new -scenario refund") {
		t.Error("quickstart should scaffold via the shipped `kiff new -scenario refund` command")
	}
	if strings.Contains(body, "pip install kiff-guard") {
		t.Error("quickstart must not show a bare `pip install kiff-guard` (the guard on-ramp lives on connect-an-agent)")
	}
}

// TestIndexSlugExists: the /docs index slug must resolve.
func TestIndexSlugExists(t *testing.T) {
	t.Parallel()

	if _, ok := Lookup(IndexSlug); !ok {
		t.Fatalf("IndexSlug %q is not in the registry", IndexSlug)
	}
}

// TestNoDuplicateSlugs catches a copy-paste registry mistake.
func TestNoDuplicateSlugs(t *testing.T) {
	t.Parallel()

	seen := map[string]bool{}
	for _, s := range sortedSlugs() {
		if seen[s] {
			t.Errorf("duplicate slug: %q", s)
		}
		seen[s] = true
	}
}

// TestUnknownSlug: HTML/Lookup report not-found for an unknown slug
// (the handler maps that to a 404).
func TestUnknownSlug(t *testing.T) {
	t.Parallel()

	if _, ok, _ := HTML("does-not-exist"); ok {
		t.Error("HTML reported ok for an unknown slug")
	}
	if _, ok := Lookup("does-not-exist"); ok {
		t.Error("Lookup reported ok for an unknown slug")
	}
}

// TestProductConceptPagesPresent pins the product-documentation pages:
// they exist in the Product group, in registry order, and carry no
// status badge (they are concept pages, not adapters).
//
// Order matters here — it is the reading order of the system. Limits
// follows Govern because Govern answers "may this action run?", the
// question every other surface answers in some form, and Limits answers
// the one no single action can.
func TestProductConceptPagesPresent(t *testing.T) {
	t.Parallel()

	want := []string{
		"domains", "framework-quickstart", "decisions",
		"receipts-and-proof", "govern", "ai-act",
	}
	inOrder := PagesIn(GroupProduct)
	got := map[string]Page{}
	var slugs []string
	for _, p := range inOrder {
		got[p.Slug] = p
		slugs = append(slugs, p.Slug)
	}
	if len(got) != len(want) {
		t.Errorf("product page count: got %d, want %d", len(got), len(want))
	}
	for _, slug := range want {
		p, ok := got[slug]
		if !ok {
			t.Errorf("missing product page: %q", slug)
			continue
		}
		if p.Status != StatusStable {
			t.Errorf("product page %q should carry no status badge, got %q", slug, p.Status)
		}
	}
	// The comment above claims the order is the reading order, so check
	// it rather than only membership.
	if len(slugs) == len(want) {
		for i := range want {
			if slugs[i] != want[i] {
				t.Errorf("product nav order: got %v, want %v", slugs, want)
				break
			}
		}
	}
}

// TestProductGroupInNavOrder: the Card groups come first, domains and
// evidence after them, and the framework adapters last.
func TestProductGroupInNavOrder(t *testing.T) {
	t.Parallel()

	want := []Group{GroupStart, GroupCards, GroupConnect, GroupTafs, GroupProduct, GroupFrameworks, GroupReference}
	got := Groups()
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("nav order: got %v, want %v", got, want)
	}
	for _, g := range got {
		if len(PagesIn(g)) == 0 {
			t.Errorf("group %q has no pages", g)
		}
	}
}

// TestProductPagesRespectHonestyBoundaries: the core product truths
// must be stated and the banned overclaims must be absent. This is the
// docs-side guard against the same drift the voice rules and the
// commercial canon warn about.
func TestProductPagesRespectHonestyBoundaries(t *testing.T) {
	t.Parallel()

	// "decision, not the runner" is the load-bearing framing, and what
	// it defends is the separation rather than the word that used to
	// carry it: KIFF decides, the customer's system executes. The
	// what-kiff-is page must say so explicitly.
	h, _, err := HTML("what-kiff-is")
	if err != nil {
		t.Fatalf("HTML(what-kiff-is): %v", err)
	}
	body := strings.ToLower(string(h))
	if !strings.Contains(body, "decision, not the runner") {
		t.Error("what-kiff-is must state KIFF is the decision, not the runner")
	}
	if !strings.Contains(body, "does **not** execute your action") &&
		!strings.Contains(body, "does <strong>not</strong> execute your action") {
		t.Error("what-kiff-is must still say plainly that KIFF does not execute the action")
	}

	// No product page may use the banned public-surface crypto jargon.
	banned := []string{"merkle", "sepolia", "on-chain", "blockchain"}
	for _, slug := range []string{
		"domains", "what-kiff-is", "decisions",
		"receipts-and-proof", "govern",
		"quickstart", "approvals", "connect-an-agent", "framework-quickstart",
	} {
		h, _, err := HTML(slug)
		if err != nil {
			t.Fatalf("HTML(%q): %v", slug, err)
		}
		b := strings.ToLower(string(h))
		for _, term := range banned {
			if strings.Contains(b, term) {
				t.Errorf("product page %q contains banned term %q", slug, term)
			}
		}
	}
}

// The /docs index leads with the Card (the cards-for-any-agent design,
// the commercial canon): connect a tool, issue a Card, point the agent
// at the gateway, and answer a held call. It must not lead with
// scaffolding a domain.
func TestQuickstartLeadsWithTheCard(t *testing.T) {
	t.Parallel()
	if IndexSlug != "quickstart" {
		t.Fatalf("index slug %q", IndexSlug)
	}
	h, _, err := HTML("quickstart")
	if err != nil {
		t.Fatal(err)
	}
	body := string(h)
	for _, want := range []string{"Give the agent a Card", "mcp.kiff.dev/mcp", "Needs you", "Approve once", "/docs/approvals"} {
		if !strings.Contains(body, want) {
			t.Errorf("quickstart missing %q", want)
		}
	}
	if strings.Contains(body, "kiff new") {
		t.Error("the quickstart scaffolds a domain; that belongs on framework-quickstart")
	}
}

// Removed pages redirect to a page that exists and is not itself moved.
func TestMovedSlugsLandOnPages(t *testing.T) {
	t.Parallel()
	for from, to := range Moved {
		if _, ok := Lookup(from); ok {
			t.Errorf("%q is both a page and moved", from)
		}
		if _, ok := Lookup(to); !ok {
			t.Errorf("%q moves to %q, which is not a page", from, to)
		}
	}
}

// The card facts each page owes a reader, pinned per page.
//
// Without this the docs block can be declared finished again while any
// of them quietly disappears: three of these pages were re-anchored for
// the card rebrand and had said nothing about a card at all, which is
// the state this protects against returning to.
func TestCardFactsSurviveOnThePagesThatOweThem(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		slug  string
		facts map[string]string // fact → why this page owes it
	}{
		{
			slug: "what-kiff-is",
			facts: map[string]string{
				// Limits to know. A reader building on cards has to
				// meet all three limits here or nowhere.
				"constrains; it never confers": "a card must not read as a grant",
				"requires_authority":           "a card is required only where the action says so",
				"not when it":                  "a draw happens at authorization, not execution",
			},
		},
		{
			slug: "concepts",
			facts: map[string]string{
				// Guard is the seam, not the authority.
				"requires_authority":                   "the card is the extra gate on the decide path",
				"neither issues nor administers cards": "issuing is a management action, not something Guard does",
			},
		},
		{
			slug: "decisions",
			facts: map[string]string{
				// The decision flow has to show where the card sits and
				// what it can say.
				"no_mandate_issued":     "the refusal when nothing has been issued",
				"mandate_limit_reached": "the refusal at the ceiling",
				"requires_authority":    "an action without it is decided without a card",
				"authorized":            "the draw is recorded at authorization",
			},
		},
	} {
		h, _, err := HTML(tc.slug)
		if err != nil {
			t.Fatalf("HTML(%s): %v", tc.slug, err)
		}
		body := string(h)
		for fact, why := range tc.facts {
			if !strings.Contains(body, fact) {
				t.Errorf("%s no longer says %q — %s", tc.slug, fact, why)
			}
		}
	}
}

// TestAgentSkillPageDoesNotReadAKeyFromTheEnvironment: the plugin asks for
// the gateway key through Claude Code; the page must not say it reads an
// environment variable.
func TestAgentSkillPageDoesNotReadAKeyFromTheEnvironment(t *testing.T) {
	t.Parallel()
	md, ok, err := Markdown("agent-skill")
	if err != nil || !ok {
		t.Fatalf("Markdown(agent-skill): ok=%v err=%v", ok, err)
	}
	if strings.Contains(md, "KIFF_GATEWAY_KEY") {
		t.Error("agent-skill.md still says the plugin reads KIFF_GATEWAY_KEY")
	}
}

// The apps tafs connects are named in one place, the apps package. The
// Connect apps page renders that list: every app is on it, and none that
// is coming reads as working.
func TestConnectAppsPageRendersTheAppsList(t *testing.T) {
	t.Parallel()
	md, ok, err := Markdown("tafs-connect-apps")
	if err != nil || !ok {
		t.Fatalf("Markdown(tafs-connect-apps): ok=%v err=%v", ok, err)
	}
	if strings.Contains(md, appsDirective) {
		t.Fatal("the apps directive was not expanded")
	}
	for _, a := range apps.All() {
		var row string
		for _, line := range strings.Split(md, "\n") {
			if strings.HasPrefix(line, "| "+a.Name+" |") {
				row = line
				break
			}
		}
		if row == "" {
			t.Errorf("tafs-connect-apps does not list %s", a.Name)
			continue
		}
		if !strings.HasSuffix(row, "| "+a.Label()+" |") {
			t.Errorf("%s: row %q, but its status is %q", a.Name, row, a.Status)
		}
		if a.Status == apps.StatusNotYet && !strings.Contains(row, "| — |") {
			t.Errorf("%s is not yet, but its row names a way to connect: %q", a.Name, row)
		}
	}
	h, _, _ := HTML("tafs-connect-apps")
	if !strings.Contains(string(h), "<table>") {
		t.Error("the apps list did not render as a table")
	}
}

// No docs page says a coming app works.
func TestNoPageSaysAComingAppWorks(t *testing.T) {
	t.Parallel()
	for _, p := range Pages() {
		md, _, _ := Markdown(p.Slug)
		for _, line := range strings.Split(md, "\n") {
			for _, a := range apps.Coming() {
				if strings.Contains(line, a.Name) && strings.Contains(strings.ToLower(line), "works") {
					t.Errorf("%s says %s works, but it is coming: %q", p.Slug, a.Name, line)
				}
			}
		}
	}
}

var docsLink = regexp.MustCompile(`href="/docs/([a-z0-9-]+)(?:#([^"]+))?"`)

// Every link from one docs page to another lands on a page, and on a
// heading that page has.
func TestDocsLinksResolve(t *testing.T) {
	t.Parallel()
	ids := map[string]map[string]bool{}
	idRe := regexp.MustCompile(`id="([^"]+)"`)
	for _, p := range Pages() {
		h, _, _ := HTML(p.Slug)
		ids[p.Slug] = map[string]bool{}
		for _, m := range idRe.FindAllStringSubmatch(string(h), -1) {
			ids[p.Slug][m[1]] = true
		}
	}
	for _, p := range Pages() {
		h, _, _ := HTML(p.Slug)
		for _, m := range docsLink.FindAllStringSubmatch(string(h), -1) {
			slug, anchor := m[1], m[2]
			if _, ok := Lookup(slug); !ok {
				if _, moved := Moved[slug]; !moved {
					t.Errorf("%s links to /docs/%s, which is not a page", p.Slug, slug)
				}
				continue
			}
			if anchor != "" && !ids[slug][anchor] {
				t.Errorf("%s links to /docs/%s#%s, which has no such heading", p.Slug, slug, anchor)
			}
		}
	}
}

// /pricing links /docs/tafs-credits#pricing-details for the detail it
// leaves out, so that heading and the facts under it stay.
func TestCreditsPageCarriesThePricingDetails(t *testing.T) {
	t.Parallel()
	h, _, err := HTML("tafs-credits")
	if err != nil {
		t.Fatal(err)
	}
	body := string(h)
	for _, want := range []string{
		`id="pricing-details"`,
		"€11.49", "€29.99", "€84.99", "never expire",
		"€0.01 a credit", "€50 to start", "80%", "100%", "48 hours",
		"€0.02 an action", "reverse charge", "order form", "kiff.dev",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("tafs-credits missing %q", want)
		}
	}
}

// The docs never sell a free plan, a trial or an open beta, and never
// use the certification vocabulary, not even to deny it.
func TestDocsVoice(t *testing.T) {
	t.Parallel()
	banned := regexp.MustCompile(`(?i)\bfree (plan|account|tier|trial)\b|\bopen[- ]beta\b|\bfree to try\b|complian|certif|\btrial card|\b7-day trial\b|\(free\)`)
	for _, p := range Pages() {
		md, _, _ := Markdown(p.Slug)
		if m := banned.FindString(md); m != "" {
			t.Errorf("%s says %q", p.Slug, m)
		}
	}
}

// With planque's proofs on, the apps list can change while the site runs:
// the Connect apps page follows it on its next read, with no restart. Not
// parallel: it changes the apps list, and puts today's back before the
// parallel tests run.
func TestConnectAppsPageFollowsPlanquesProofs(t *testing.T) {
	answer := `{"proved": {"stripe": {"at": "2026-11-13", "how": "key"}}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(answer)) }))
	defer srv.Close()
	src := &apps.ProvedSource{Base: srv.URL}
	row := func(name string) string {
		md, _, _ := Markdown("tafs-connect-apps")
		for _, line := range strings.Split(md, "\n") {
			if strings.HasPrefix(line, "| "+name+" |") {
				return line
			}
		}
		return ""
	}
	if got := row("Gmail"); !strings.HasSuffix(got, "| Connects with a sign-in |") {
		t.Fatalf("before: Gmail's row is %q", got)
	}
	before, _, _ := HTML("tafs-connect-apps")
	if err := src.Read(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := row("Gmail"); !strings.HasSuffix(got, "| Coming |") {
		t.Errorf("unproved, Gmail's row is %q, want Coming", got)
	}
	if h, _, _ := HTML("tafs-connect-apps"); h == before || !strings.Contains(string(h), "<td>Gmail</td>\n<td>Sign-in</td>\n<td>Coming</td>") {
		t.Errorf("the HTML didn't follow the new list")
	}
	// planque's proofs today: the list as apps.json has it, for the tests after.
	answer = `{"proved": {"stripe": {"at": "2026-11-13", "how": "key"}, "qonto": {"at": "2026-11-13", "how": "key"},
	  "shippo": {"at": "2026-11-13", "how": "key"}, "intercom": {"at": "2026-11-13", "how": "key"}, "zendesk": {"at": "2026-11-13", "how": "key"},
	  "pipedrive": {"at": "2026-11-13", "how": "key"}, "github": {"at": "2026-11-13", "how": "key"}, "gmail": {"at": "2026-11-13", "how": "oauth"},
	  "typeform": {"at": "2026-11-16", "how": "oauth"}, "googlecalendar": {"at": "2026-11-16", "how": "oauth"}, "googledrive": {"at": "2026-11-16", "how": "oauth"}}}`
	if err := src.Read(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := row("Gmail"); !strings.HasSuffix(got, "| Connects with a sign-in |") {
		t.Errorf("proved again, Gmail's row is %q", got)
	}
}
