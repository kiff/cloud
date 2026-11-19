// Package docs embeds the public documentation as a set of vendored
// markdown files and renders them to HTML for the /docs routes.
//
// It is the same rendering pattern the /whitepaper page uses
// (gomarkdown + memoization + embed.FS), extended from a single
// document to a small multi-page site with a nav registry. Per the
// web binary is hermetic Go + templ + native CSS — no Node, no
// Docusaurus, no separate docs toolchain. Docs are markdown checked
// into this package and served by the same binary.
//
// The narrative lives here (the docs are the single source of
// truth); the runnable code lives in the
// public guard repo (kiff/kiff-guard) and is linked, not
// duplicated.
package docs

import (
	"bytes"
	"embed"
	"fmt"
	"html/template"
	"sort"
	"strings"
	"sync"

	"github.com/gomarkdown/markdown"
	"github.com/gomarkdown/markdown/html"
	"github.com/gomarkdown/markdown/parser"

	"github.com/kiff/cloud/web/internal/content/apps"
)

//go:embed pages/*.md
var pagesFS embed.FS

// Status marks how trustworthy a framework page's integration is.
// The issue requires verified-vs-preview to be clearly labelled so
// no page fakes a runnable path that has not been exercised.
type Status string

const (
	// StatusStable is the quickstart / conceptual pages that don't
	// describe a single adapter (no badge rendered).
	StatusStable Status = ""
	// StatusVerified means the adapter ships in kiff-guard and its
	// seam was exercised (live round-trip or conformance suite).
	StatusVerified Status = "verified"
	// StatusPreview means the seam is researched and the page is
	// honest about the integration, but the adapter is not yet
	// shipped/exercised. Never presented as runnable today.
	StatusPreview Status = "preview"
)

// Group is a nav grouping in the docs sidebar.
type Group string

const (
	GroupStart      Group = "Start here"
	GroupCards      Group = "Cards"
	GroupConnect    Group = "Connect"
	GroupTafs       Group = "tafs app"
	GroupProduct    Group = "Domains and evidence"
	GroupFrameworks Group = "Frameworks"
	// GroupReference holds the field lists, signatures and payloads the
	// concept pages link to instead of carrying them.
	GroupReference Group = "Reference"
)

// Page is one docs page: a slug (URL + markdown filename stem), a
// title, its nav group, an integration status, and a one-line blurb
// used on the index and in nav tooltips.
type Page struct {
	Slug   string
	Title  string
	Group  Group
	Status Status
	Blurb  string
}

// pages is the ordered registry. Order within a group is the nav
// order. What KIFF is leads the nav; /docs itself serves IndexSlug.
//
// Framework order mirrors the survey + adapter status in
// docs/integration/frameworks/README.md: the Python adapters first,
// then OpenClaw (the TypeScript Gateway). All are verified now that the
// JS guard SDK ships the OpenClaw adapter and passes conformance. Every
// framework named in has a real page.
var pages = []Page{
	{
		Slug:  "what-kiff-is",
		Title: "What KIFF is",
		Group: GroupStart,
		Blurb: "A Card for each agent: what it may do, how much, and until when. KIFF decides each action against it before it runs. What KIFF decides, and what your system still owns.",
	},
	{
		Slug:  "quickstart",
		Title: "Quickstart: give an agent a Card",
		Group: GroupStart,
		Blurb: "Connect an agent and a tool, issue it a Card, and answer its first held call. No code.",
	},
	{
		Slug:  "kiff-cards",
		Title: "KIFF Cards",
		Group: GroupStart,
		Blurb: "What you give one agent: what it may do, under what conditions, how much, and until when, with a statement of every draw and change.",
	},
	{
		Slug:  "cards-in-practice",
		Title: "Cards in practice",
		Group: GroupStart,
		Blurb: "Five examples: a refund agent with a daily budget, plan changes refused past a limit, a new agent started small, a shared budget, and stopping one agent.",
	},
	{
		Slug:  "approvals",
		Title: "Held calls and approvals",
		Group: GroupCards,
		Blurb: "What happens to a call outside the Card: held on Needs you, emailed to the owner, and refused if nobody answers in time. The three answers.",
	},
	{
		Slug:  "card-limits",
		Title: "Limits, warnings and extra room",
		Group: GroupCards,
		Blurb: "The windows a limit counts over, when a day starts, how to change a limit or add room, and the warnings KIFF sends before a limit runs out.",
	},
	{Slug: "external-approvers", Title: "Connect an external approver", Group: GroupCards, Blurb: "A service you run answers held calls on an Ask Card, up to a limit an admin sets. A person can still answer."},
	{
		Slug:  "connect-an-agent",
		Title: "Connect an agent",
		Group: GroupConnect,
		Blurb: "Three ways in: the MCP gateway for agents that call MCP tools, kiff-guard for agents in your own code, or the decision API from any code.",
	},
	{
		Slug:  "mcp-gateway",
		Title: "Use the KIFF MCP gateway",
		Group: GroupConnect,
		Blurb: "Put KIFF between an agent and its remote MCP tools: the API, operation ids, held calls, and what the agent sees.",
	},
	{
		Slug:  "tool-permits",
		Title: "Keep tool keys out of KIFF",
		Group: GroupConnect,
		Blurb: "Choose who holds a connected tool's key. In verify mode the tool checks a permit KIFF signs for each allowed call, and KIFF holds no key.",
	},
	{
		Slug:  "agent-skill",
		Title: "The KIFF skill for coding agents",
		Group: GroupConnect,
		Blurb: "Beta. A SKILL.md for Claude Code, Codex and similar agents: connect a tool through the gateway, propose a Card for the owner, and handle a held call without routing around it.",
	},
	{
		Slug:  "tafs",
		Title: "tafs: how it works",
		Group: GroupTafs,
		Blurb: "What a taf is, its Card, Needs you, the app's screens and Setup, Connect apps, Coach mode, chat, notes and the Record, on iPhone, iPad and Android.",
	},
	{
		Slug:  "tafs-try-a-version",
		Title: "Try a version beside the live one",
		Group: GroupTafs,
		Blurb: "Try a changed taf under its own Card while the live version keeps working, then install it or go back.",
	},
	{
		Slug:  "tafs-approving-on-your-phone",
		Title: "Approving on your phone",
		Group: GroupTafs,
		Blurb: "The two keys on your phone, what asks for Face ID or Touch ID, and what does not.",
	},
	{
		Slug:  "tafs-doors",
		Title: "Doors: an email address or a Slack channel for a taf",
		Group: GroupTafs,
		Blurb: "Give a taf an email address or a Slack channel: who can give it work, who only knocks, and the daily limits.",
	},
	{
		Slug:  "tafs-connect-apps",
		Title: "Connect apps",
		Group: GroupTafs,
		Blurb: "Connecting an app to a taf: what you approve, what the taf can do, app events, the Slack door, disconnecting.",
	},
	{
		Slug:  "tafs-connection-profiles",
		Title: "Connection profiles and Did this happen?",
		Group: GroupTafs,
		Blurb: "How KIFF checks and counts the calls of an app the tafs app connected, and the Did this happen? question for a money call it can't prove.",
	},
	{
		Slug:  "tafs-roles",
		Title: "Who can do what",
		Group: GroupTafs,
		Blurb: "What Viewers, Approvers, Editors and Admins can do with tafs, and who sees which conversations.",
	},
	{
		Slug:  "tafs-halt",
		Title: "Halt and resume",
		Group: GroupTafs,
		Blurb: "Stop one taf or all of them from one screen, what a halt does and does not undo, and how to resume.",
	},
	{
		Slug:  "tafs-credits",
		Title: "Credits and your balance",
		Group: GroupTafs,
		Blurb: "What a run uses in credits, what isn't counted, what happens while your tafs wait, and the pricing details: packs, the spend limit, KIFF actions and VAT.",
	},
	{
		Slug:  "domains",
		Title: "Reusable operational domains",
		Group: GroupProduct,
		Blurb: "What a Card stands on: the state, actions, authority, approvals and evidence your agents share. Build it once; connect or replace agents without rewiring it.",
	},
	{
		Slug:  "framework-quickstart",
		Title: "Build a domain with the framework",
		Group: GroupProduct,
		Blurb: "Scaffold a runnable refund domain with the open-source framework, run it locally, and watch it allow, hold and refuse.",
	},
	{
		Slug:  "decisions",
		Title: "Decisions",
		Group: GroupProduct,
		Blurb: "The proposal → decision flow: what is evaluated, the four outcomes, and how a Card is drawn on.",
	},
	{
		Slug:  "receipts-and-proof",
		Title: "Receipts & proof packs",
		Group: GroupProduct,
		Blurb: "What a decision receipt proves (pre-execution authorization) and what it does not (downstream execution). Export as a proof pack.",
	},
	{
		Slug:  "govern",
		Title: "Govern",
		Group: GroupProduct,
		Blurb: "For company accounts: the evidence view per action in a domain, its observe or enforce posture, and its proof pack.",
	},
	{
		Slug:  "ai-act",
		Title: "EU AI Act and KIFF",
		Group: GroupProduct,
		Blurb: "What KIFF Cloud gives you for Article 12 (logging) and Article 14 (human oversight), and what it doesn't.",
	},
	{
		Slug:  "concepts",
		Title: "How kiff-guard works",
		Group: GroupFrameworks,
		Blurb: "Observe vs enforce, the one-receipt rule, and the two adapter shapes behind every framework page.",
	},
	{
		Slug:   "agno",
		Title:  "Agno",
		Group:  GroupFrameworks,
		Status: StatusVerified,
		Blurb:  "Middleware via tool_hooks. The first adapter; verified live against the cloud decide endpoint.",
	},
	{
		Slug:   "langgraph",
		Title:  "LangGraph / LangChain",
		Group:  GroupFrameworks,
		Status: StatusVerified,
		Blurb:  "Middleware via wrap_tool_call — the same short-circuit LangChain's own ShellAllowListMiddleware uses.",
	},
	{
		Slug:   "hermes",
		Title:  "Hermes Agent (Nous)",
		Group:  GroupFrameworks,
		Status: StatusVerified,
		Blurb:  "Inverted-control via the pre_tool_call plugin hook. Source-verified against hermes-agent on main.",
	},
	{
		Slug:   "openai-agents",
		Title:  "OpenAI Agents SDK",
		Group:  GroupFrameworks,
		Status: StatusVerified,
		Blurb:  "Inverted-control via a tool input guardrail. Verified against openai-agents v0.17.4 + a live model call.",
	},
	{
		Slug:   "google-adk",
		Title:  "Google ADK",
		Group:  GroupFrameworks,
		Status: StatusVerified,
		Blurb:  "Vote via before_tool_callback. Verified against the real google-adk package; passes conformance.",
	},
	{
		Slug:   "pydantic-ai",
		Title:  "Pydantic AI",
		Group:  GroupFrameworks,
		Status: StatusVerified,
		Blurb:  "Vote via the before_tool_execute hook. Verified against the real pydantic-ai package; passes conformance.",
	},
	{
		Slug:   "strands",
		Title:  "Strands Agents",
		Group:  GroupFrameworks,
		Status: StatusVerified,
		Blurb:  "Vote via the BeforeToolCallEvent hook. Verified against the real strands-agents package; passes conformance.",
	},
	{
		Slug:   "haystack",
		Title:  "Haystack Agents",
		Group:  GroupFrameworks,
		Status: StatusVerified,
		Blurb:  "Vote via a ConfirmationStrategy. Verified against the real haystack-ai package; passes conformance.",
	},
	{
		Slug:   "microsoft-agent-framework",
		Title:  "Microsoft Agent Framework",
		Group:  GroupFrameworks,
		Status: StatusVerified,
		Blurb:  "Async middleware via FunctionMiddleware. Verified against the real agent-framework-core package; passes conformance.",
	},
	{
		Slug:   "llamaindex",
		Title:  "LlamaIndex Agents",
		Group:  GroupFrameworks,
		Status: StatusVerified,
		Blurb:  "Middleware via a GuardedAgentWorkflow that overrides the call_tool step. Verified against llama-index-core; passes conformance.",
	},
	{
		Slug:   "openclaw",
		Title:  "OpenClaw",
		Group:  GroupFrameworks,
		Status: StatusVerified,
		Blurb:  "TypeScript Gateway via the before_tool_call hook. Ships in the JS guard SDK; first adapter with a native approval_required.",
	},
	{
		Slug:  "reference-agent-drafts",
		Title: "Agent drafts",
		Group: GroupReference,
		Blurb: "Submit an agent and its Cards for a person to confirm, claim its key, revise its Cards, and test a version before installing it.",
	},
	{
		Slug:  "reference-device-signatures",
		Title: "Device signatures",
		Group: GroupReference,
		Blurb: "How a phone or computer with a device key signs answers and requests, the approval key, and the approval window.",
	},
	{
		Slug:  "reference-webhooks",
		Title: "Webhooks",
		Group: GroupReference,
		Blurb: "Alert targets for held calls and Card warnings: payloads, signing and retries.",
	},
	{
		Slug:  "reference-keys",
		Title: "Keys",
		Group: GroupReference,
		Blurb: "Which key can draw on a Card, the read-only account key, and when keys expire.",
	},
	{
		Slug:  "reference-api-fields",
		Title: "API fields",
		Group: GroupReference,
		Blurb: "The fields an app reads for Cards, held calls and activity, and drafting a Card change from a held call.",
	},
}

// rendered caches the parsed HTML per slug, populated once.
var (
	once       sync.Once
	htmlBySlug map[string]template.HTML
	mdBySlug   map[string]string
	bySlug     map[string]Page
	initErr    error

	// The pages that show the apps table, as written (before the directive is
	// expanded), and the apps list's generation they were last rendered for:
	// with planque's proofs on, the list can change while the site runs, so
	// those pages are rendered again when it does (apps.Generation).
	withApps map[string][]byte
	appsGen  uint64
	appsMu   sync.Mutex
)

func load() {
	htmlBySlug = make(map[string]template.HTML, len(pages))
	mdBySlug = make(map[string]string, len(pages))
	bySlug = make(map[string]Page, len(pages))
	withApps = map[string][]byte{}
	appsGen = apps.Generation()
	for _, p := range pages {
		bySlug[p.Slug] = p
		src, err := pagesFS.ReadFile("pages/" + p.Slug + ".md")
		if err != nil {
			initErr = fmt.Errorf("docs: missing markdown for slug %q: %w", p.Slug, err)
			return
		}
		if bytes.Contains(src, []byte(appsDirective)) {
			withApps[p.Slug] = src
		}
		src = expandDirectives(src)
		mdBySlug[p.Slug] = string(src)
		htmlBySlug[p.Slug] = renderMarkdown(src)
	}
}

// Pages returns the ordered page registry (for nav + the index).
func Pages() []Page {
	once.Do(load)
	out := make([]Page, len(pages))
	copy(out, pages)
	return out
}

// Lookup returns a page's metadata by slug, and whether it exists.
func Lookup(slug string) (Page, bool) {
	once.Do(load)
	p, ok := bySlug[slug]
	return p, ok
}

// HTML returns the rendered markdown for a slug. The bool is false
// for an unknown slug (the handler maps that to a 404).
func HTML(slug string) (template.HTML, bool, error) {
	once.Do(load)
	if initErr != nil {
		return "", false, initErr
	}
	freshApps()
	appsMu.Lock()
	defer appsMu.Unlock()
	h, ok := htmlBySlug[slug]
	return h, ok, nil
}

// freshApps renders the pages that show the apps table again when the apps
// list has changed since they were rendered.
func freshApps() {
	gen := apps.Generation()
	appsMu.Lock()
	defer appsMu.Unlock()
	if gen == appsGen {
		return
	}
	for slug, src := range withApps {
		src = expandDirectives(src)
		mdBySlug[slug] = string(src)
		htmlBySlug[slug] = renderMarkdown(src)
	}
	appsGen = gen
}

// Markdown returns the raw markdown source for a slug, and whether it
// exists. Used to assemble the full-text /llms-full.txt corpus from
// the same embedded files the HTML pages render from, so it can't
// drift from the docs.
func Markdown(slug string) (string, bool, error) {
	once.Do(load)
	if initErr != nil {
		return "", false, initErr
	}
	freshApps()
	appsMu.Lock()
	defer appsMu.Unlock()
	md, ok := mdBySlug[slug]
	return md, ok, nil
}

// Moved maps a removed page's slug to the page that replaced it, so
// old links land somewhere useful instead of a 404.
var Moved = map[string]string{
	"protected-controls":    "domains",
	"five-line-integration": "connect-an-agent",
	"cloud-quickstart":      "connect-an-agent",
	// The shipped/not-shipped ledger fell behind the product; its limits
	// that still hold live on What KIFF is.
	"whats-real-today": "what-kiff-is",
	// Tasks gave way to credits (pricing phase 2).
	"tafs-tasks-and-packs": "tafs-credits",
}

// IndexSlug is the slug served at /docs (the quickstart).
const IndexSlug = "quickstart"

// Groups returns the nav groups in display order. Kept explicit so
// the sidebar order is stable regardless of map iteration.
func Groups() []Group {
	return []Group{GroupStart, GroupCards, GroupConnect, GroupTafs, GroupProduct, GroupFrameworks, GroupReference}
}

// PagesIn returns the pages in a group, in registry order.
func PagesIn(g Group) []Page {
	once.Do(load)
	var out []Page
	for _, p := range pages {
		if p.Group == g {
			out = append(out, p)
		}
	}
	return out
}

// appsDirective is the line a page uses where the apps tafs connects
// belong. No page names them by hand: the list comes from the apps
// package, so a page never says an app works before it does.
const appsDirective = "<!-- apps -->"

// expandDirectives replaces each directive with what it stands for,
// before the markdown is rendered or served as text.
func expandDirectives(src []byte) []byte {
	if !bytes.Contains(src, []byte(appsDirective)) {
		return src
	}
	return bytes.ReplaceAll(src, []byte(appsDirective), []byte(appsTable()))
}

// appsTable is the apps tafs connects as a markdown table, the ones
// that connect with an API key first.
func appsTable() string {
	var b strings.Builder
	b.WriteString("| App | Connects with | Status |\n|---|---|---|\n")
	for _, a := range apps.All() {
		with := "—"
		switch a.Connects {
		case apps.ConnectsKey:
			with = "API key"
		case apps.ConnectsSignIn:
			with = "Sign-in"
		}
		fmt.Fprintf(&b, "| %s | %s | %s |\n", a.Name, with, a.Label())
	}
	return strings.TrimSuffix(b.String(), "\n")
}

// renderMarkdown mirrors the whitepaper renderer: gomarkdown with the
// same extension set and HTML5 output, no script hooks, external
// links open in a new tab. Input is vendored, not user-controlled.
func renderMarkdown(src []byte) template.HTML {
	exts := parser.CommonExtensions |
		parser.AutoHeadingIDs |
		parser.Footnotes |
		parser.DefinitionLists

	p := parser.NewWithExtensions(exts)
	doc := p.Parse(src)

	opts := html.RendererOptions{
		Flags: html.CommonFlags | html.HrefTargetBlank | html.NoopenerLinks | html.NoreferrerLinks,
	}
	r := html.NewRenderer(opts)

	var buf bytes.Buffer
	buf.Grow(len(src) * 2)
	buf.Write(markdown.Render(doc, r))
	return template.HTML(buf.String())
}

// sortedSlugs is a test/diagnostic helper: the slugs in sorted order.
func sortedSlugs() []string {
	once.Do(load)
	out := make([]string, 0, len(bySlug))
	for s := range bySlug {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}
