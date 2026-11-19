// Package videos is the hand-maintained manifest for the /videos gallery.
//
// The heavy media (mp4 tiers + poster) lives in a public-read S3 bucket, not
// in the repo or the Go binary — this package holds only the small metadata
// the page renders from, plus the URL helpers that point at S3.
//
// The gallery has two groups, in this priority order:
//  1. Demos          — an AI agent working an end-to-end task, KIFF deciding
//     each consequential action before it runs.
//  2. Product tours   — short walkthroughs of the KIFF Cloud UI itself.
//
// Framing rule: lead with the problem/situation a team recognizes, not the
// feature name. The video is the proof it's handled.
package videos

import "net/url"

// BaseURL is the public-read S3 origin for the optimized web bundles.
// Per clip: <BaseURL>/<slug>/{demo-360p,demo-480p,demo-540p,demo-720p}.mp4
// and poster.jpg. A CDN can front this later by moving one constant.
const BaseURL = "https://kiff-videos-eu-929018226542.s3.eu-west-1.amazonaws.com"

// Clip is one gallery entry. Title is the problem in the reader's words;
// Lead is the situation; Shows is what the video actually demonstrates.
type Clip struct {
	Slug            string
	Title           string
	Lead            string
	Shows           string
	Language        string
	LanguageLabel   string
	Portrait        bool
	DurationSeconds int
	AssetRevision   string
}

// Revised media needs a distinct URL: the public bucket caches for a year.
func (c Clip) asset(file string) string {
	uri := BaseURL + "/" + c.Slug + "/" + file
	if c.AssetRevision != "" {
		uri += "?v=" + url.QueryEscape(c.AssetRevision)
	}
	return uri
}
func (c Clip) Poster() string         { return c.asset("poster.jpg") }
func (c Clip) Src540() string         { return c.asset("demo-540p.mp4") }
func (c Clip) Src720() string         { return c.asset("demo-720p.mp4") }
func (c Clip) Src360() string         { return c.asset("demo-360p.mp4") }
func (c Clip) Src480() string         { return c.asset("demo-480p.mp4") }
func (c Clip) Captions() string       { return c.asset("captions.vtt") }
func (c Clip) Subtitles() string      { return c.asset("captions.srt") }
func (c Clip) SrcPortrait() string    { return c.asset("demo-1280p.mp4") }
func (c Clip) SocialDownload() string { return c.asset("demo-1920p.mp4") }
func (c Clip) ShortDownload() string {
	if c.Portrait {
		return c.SocialDownload()
	}
	return c.asset("demo-1080p.mp4")
}
func (c Clip) FormatLabel() string {
	if c.Portrait {
		return "Portrait · 1080 × 1920"
	}
	return "Landscape · 1920 × 1080"
}
func (c Clip) ShareImage() string {
	if c.DurationSeconds > 0 {
		return c.asset("cover.jpg")
	}
	return c.Poster()
}

func (c Clip) PagePath() string { return "/proof/videos/" + c.Slug }
func (c Clip) PageURL() string  { return "https://kiff.dev" + c.PagePath() }
func (c Clip) WhatsAppURL() string {
	return "https://wa.me/?text=" + url.QueryEscape(c.Title+"\n"+c.PageURL())
}
func (c Clip) LinkedInURL() string {
	return "https://www.linkedin.com/sharing/share-offsite/?url=" + url.QueryEscape(c.PageURL())
}

// Lookup accepts only clips in the public gallery, never arbitrary asset URLs.
func Lookup(slug string) (Clip, bool) {
	for _, clip := range append(append(Demos(), ProductTours()...), Capsules()...) {
		if clip.Slug == slug {
			return clip, true
		}
	}
	return Clip{}, false
}

// Capsules are illustrated short films. Recorded MCP evidence remains in Demos.
func Capsules() []Clip {
	return []Clip{
		{
			Slug: "customer-ops-portrait-en", Title: "Your agent handles the work. You keep control.",
			Lead:     "Connect your tools, give the agent a Card, and let routine credits through while larger requests wait for you. The whole flow in 58 seconds, made for your phone.",
			Shows:    "Illustrated customer-support scenario with English narration: a €500 per-credit limit and €3,000 daily budget; €180 allowed, €1,400 held, a payment-terms change refused, and one-time approval without raising the limit. Fictional customers and amounts, no live tool calls. KIFF must be the agent’s only route to the protected tools. See the longer recordings above for real MCP calls.",
			Language: "en", LanguageLabel: "English", Portrait: true, DurationSeconds: 58,
			AssetRevision: "captions-20261007",
		},
		{
			Slug: "customer-ops-landscape-en", Title: "The whole system, in one picture.",
			Lead:     "Follow the tools, the agent’s Card and the three decision paths in a 58-second narrated map. The landscape version for presentations and larger screens.",
			Shows:    "The same fictional customer-support scenario as the portrait film, with the isometric map leading the narration. €180 allowed, €1,400 held, a payment-terms change refused, then one-time approval. No live tool calls; real MCP evidence remains in the longer recordings above. KIFF must be the agent’s only route to the protected tools.",
			Language: "en", LanguageLabel: "English", DurationSeconds: 58,
			AssetRevision: "captions-20261007",
		},
	}
}

// CardsAtWork is the narrated recording of the verified Refund Card run
// (docs/evidence/cards-at-work/refund-card-2026-09-22), embedded on /proof
// and listed first in the gallery.
var CardsAtWork = Clip{
	Slug:  "cards-at-work",
	Title: "Let a support agent issue refunds, up to a ceiling you set",
	Lead:  "You want the agent to finish routine refunds without a person re-clicking each one, and never past an amount you control.",
	Shows: "A synthetic test run of the Refund Card on KIFF's production services, no customer data: three refunds authorized against its balance, requests with no card or larger than what remained refused, the owner raising the ceiling, and the next attempt stopped once the card was revoked. Every decision has its own signed receipt.",
}

// Demos: an agent doing real work, governed live. The priority group.
func Demos() []Clip {
	return []Clip{
		CardsAtWork,
		{
			Slug:          "mcp-from-zero-en",
			Title:         "Your agent can act. You decide how far.",
			Lead:          "€20 runs. €80 waits for your approval. Connect your agent through KIFF MCP and watch your Card control what happens next.",
			Shows:         "English narration and real Codex MCP calls: connect a server, configure its tools, issue Cards and configure the agent key off camera. A €20 refund runs within a €50 limit; an €80 request waits for the owner and runs only after approval and a retry of the same operation. Lower the Card to €10, then watch a new €20 request wait. KIFF production gateway, Stripe test mode only. Setup footage reused from the French recording; new calls, approval and Card update recorded 6 October 2026.",
			Language:      "en",
			LanguageLabel: "English",
		},
		{
			Slug:          "mcp-from-zero-fr",
			Title:         "Votre agent agit. Vous fixez les limites.",
			Lead:          "20 € passent. 80 € attendent votre accord. Connectez votre agent via KIFF MCP et voyez votre Card décider de la suite.",
			Shows:         "French narration and real Codex MCP calls: connect the server and tools, issue Cards and configure the agent key off camera. A €20 refund runs; an €80 request waits for approval. The approved result is re-read with the same operation ID after its first execution was missed by the recording; Stripe confirms no duplicate refund. Lower the Card from €50 to €10, then watch a new €20 request wait. KIFF production gateway, Stripe test mode only; recorded 6 October 2026.",
			Language:      "fr",
			LanguageLabel: "Français",
		},
		{
			Slug:  "refunds-through-kiff",
			Title: "A support agent refunds on its own, and the big one waits for you",
			Lead:  "You want the agent to finish routine refunds by itself, larger ones to wait for you, and the agent never to hold the payment credential.",
			Shows: "A support operator asks an Agno agent (Claude on Amazon Bedrock) to process refunds. Its tools reach Stripe (test mode) only through the KIFF MCP gateway. A €24 refund is inside its Card and goes through; an €86 refund is over the €50 limit, so KIFF holds it and nothing reaches Stripe. The owner approves it in KIFF, and the agent, which was waiting in the same conversation, finishes the refund once. Then the Card's record and the agent's page. Real model, real gateway, Stripe test mode; recorded 2 October 2026.",
		},
		{
			Slug:  "prod-guardian",
			Title: "An AI agent works a live production incident",
			Lead:  "You want an agent that can mitigate an incident — roll back, scale, fail over — without doing something irreversible under pressure.",
			Shows: "The on-call agent rolls back a bad deploy (allowed), a region failover is held for a human to approve, and \"drop the database\" is refused outright — each decided against the service's live state, every one on the record.",
		},
		{
			Slug:  "agent-scan-cloud",
			Title: "See what an agent can reach, then watch KIFF hold the line",
			Lead:  "The moment an agent is wired to real systems it can trigger consequential actions; you need to see the blast radius and know the wrong move is stopped.",
			Shows: "A scan surfaces every action the agent's code can reach, then live: the safe fix runs, a high-blast-radius action waits for a human, and a schema migration mid-incident is blocked — the move that has wiped live databases this year.",
		},
	}
}

// ProductTours: short walkthroughs of the KIFF Cloud product surface.
func ProductTours() []Clip {
	return []Clip{
		{
			Slug:  "connect-agent",
			Title: "Bring an agent you already run under KIFF",
			Lead:  "You have an agent making tool calls today and want it governed — without a rewrite.",
			Shows: "From the Runtimes page: mint a key, drop in the language-agnostic call, and the agent shows up as a connected, governed runtime — with the open-source adapters on GitHub.",
		},
		{
			Slug:  "build",
			Title: "Author a governed domain from your rules",
			Lead:  "Turn refund limits, approval thresholds and state rules — today scattered across services — into one versioned contract your agents decide against.",
			Shows: "Using the kiff-domains skill so your coding agent writes the kiff.yaml, then validating and activating a real refunds domain in the dashboard.",
		},
		{
			Slug:  "govern",
			Title: "The control room: every decision in one place",
			Lead:  "When an agent acts on production, you need to see what it proposed and how each call was decided — not hope it behaved.",
			Shows: "The Govern view: decision counts by outcome and a state-aware decisions table where a refusal reads as decided against the entity's real state, ending on a signed receipt.",
		},
		{
			Slug:  "activity-evidence",
			Title: "\"What did the agent do?\" — answered with proof",
			Lead:  "Weeks after an incident, compliance asks exactly what happened and what stopped it. \"Trust me\" isn't an answer.",
			Shows: "The per-domain activity trail and a signed, tamper-evident receipt: the action, the state it was checked against, and the verdict — recorded before anything ran.",
		},
	}
}
