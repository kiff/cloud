package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPricing(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodGet, "/pricing", nil)
	rec := httptest.NewRecorder()

	Pricing(PricingLinks{}).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want %d", rec.Code, http.StatusOK)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("content-type: got %q, want text/html", ct)
	}

	body := rec.Body.String()

	mustContain := []string{
		// Shared chrome + the homepage design system.
		`class="site-nav"`,
		`class="site-footer"`,
		`href="/static/css/home.css"`,
		`class="k-home"`,

		// The plans in a few lines (owner, 13 Nov 2026: two phone screens).
		`id="plans"`,
		"One account covers the tafs app and your own agents. A credit pays for the AI a run uses. No free plan, no trial.",
		"Solo", "€12.50", "a month, VAT included. 500 credits a month, then packs. In the tafs app.",
		"Company", "€10.50", "a seat a month, plus VAT. 500 credits a seat, pooled, then €0.01 a credit up to a limit you set. On kiff.dev.",
		"Committed volume on an order form.", "Talk to Gabriel",
		// /on-rails lives here now (owner, 13 Nov 2026).
		"We can also get your first agent action live with you: we observe it on your real traffic, then you issue its Card.",
		"Coach for about 4 new jobs a month, runs that fail on our side and tries that send nothing are free.",
		"Your tafs stop at what you have.", "The framework and kiff-guard are MIT.",
		// The detail lives in the docs.
		`href="/docs/tafs-credits#pricing-details"`,
	}
	for _, want := range mustContain {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q", want)
		}
	}

	// Each fact once, and no section the page dropped.
	page := body[strings.Index(body, "</head>"):]
	for _, once := range []string{"No free plan, no trial", "about 4 new jobs a month"} {
		if n := strings.Count(page, once); n != 1 {
			t.Errorf("%q is said %d times, want once", once, n)
		}
	}
	for _, gone := range []string{`id="rules"`, `id="packs"`, `id="faq"`, "Is there a free trial?"} {
		if strings.Contains(body, gone) {
			t.Errorf("still has %q", gone)
		}
	}

	mustNotContain := []string{
		// Not built, or not the rule.
		"SSO", "limit_exceeded", "Becomes Solo", "billed monthly to your company", "bill your company",
		// The v1 offer replaces the beta and "every checked call counts".
		"free during the beta", "Free during the beta", "open beta",
		"1,000 checked calls", "whatever the answer",
		"$500",
		// A credit is €0.01 only past Company's plan, not for everyone.
		"1 credit is €0.01",
		// Test-mode calls count like any other (PM).
		"test mode", "Testing is free",
		// History and people aren't by plan.
		"days of history", "A year of history", "Several approvers",
		// Team is per person (owner): no flat €29, approvers are seats.
		"€29 ", "€29<", "€29,", "€29/", "14.50",
		// Tasks, task packs and the trial gave way to credits (pricing
		// phase 2); no checkout is linked while credits have none live.
		"7-day trial", "free week", "tafs task", "task pack", "Pico", "Vonk", "€4.99", "12 months",
		"/dashboard/checkout", "Team", "half the plan's monthly price", "ceiling_reached",
		"Pay when something gets done", "Buy now", "buy now",
		// KIFF actions are Company's alone (the owner's choice A): Solo is tafs, in credits.
		"500 KIFF actions, then", "500 KIFF actions a person",
		// Monthly only in v1.
		"Yearly", "yearly", "two months free",
		// Claims the product doesn't make.
		"any MCP server", "at once", "immediately", "unhackable", "SOC 2",
		// Implementation jargon.
		"Merkle", "Sepolia", "on-chain", "kiff-verify", "Base mainnet",
		// Self-hosted fonts only.
		"fonts.googleapis.com", "fonts.gstatic.com",
		// The prices before the owner's rise of 8 November 2026.
		"€10.99", "€8.99",
		// The pack prices before the owner's volume discount of 10 November 2026.
		"€34.50", "€115.00",
	}
	for _, banned := range mustNotContain {
		if strings.Contains(body, banned) {
			t.Errorf("body contains banned token %q", banned)
		}
	}

	// No app link until one is configured.
	if strings.Contains(body, "Download on the App Store") || strings.Contains(body, "TestFlight") {
		t.Error("an app button without an app link")
	}

	// Superseded per-control / legacy-plan framing must not reappear.
	mustNotContainOldPricing := []string{
		"Launch Pilot",
		"protected controls for money-moving",
		"production protected control",
		"One Protected Control.",
		"Builder",
		"Agency / Team",
		"per workspace",
		"Pay when KIFF protects",
		// Legacy tier names and a link into the private repo.
		"Cloud Growth",
		"Free Developer",
		"github.com/kiff/kiff-cloud",
		// The per-task price and the owner's ceiling, retired by
		// packs.
		"€0.39",
		"set your ceiling",
	}
	for _, banned := range mustNotContainOldPricing {
		if strings.Contains(body, banned) {
			t.Errorf("body contains superseded pricing phrase %q", banned)
		}
	}
}

func TestPricing_ConfiguredIntakeLinksRender(t *testing.T) {
	t.Parallel()

	links := PricingLinks{
		Offer: OfferLinks{
			AppURL: "https://testflight.apple.com/join/abc123",
		},
		LaunchPilotCheckoutURL: "https://cal.com/kiff/growth-access",
		TeamCheckoutURL:        "https://cal.com/kiff/scale",
		BusinessIntakeURL:      "https://cal.com/kiff/enterprise",
	}

	req := httptest.NewRequest(http.MethodGet, "/pricing", nil)
	rec := httptest.NewRecorder()

	Pricing(links).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want %d", rec.Code, http.StatusOK)
	}

	body := rec.Body.String()

	mustContain := []string{
		// The intake URL now drives the boundary-review booking. The
		// founder mailto stays alongside it rather than being replaced:
		// at this stage a direct line to a person is the offer, and a
		// booking link is the lower-friction path to the same place.
		`href="https://cal.com/kiff/enterprise"`,
		`href="https://testflight.apple.com/join/abc123"`,
		"Try the app on TestFlight",
		"Book a 30-minute review",
		"gabriel@kiff.dev",
	}
	for _, want := range mustContain {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q (configured-URL path)", want)
		}
	}

	mustNotContainHrefs := []string{
		// The tier CTAs are gone with the tiers. The site footer still
		// carries hello@kiff.dev, so this bans the specific tier subjects
		// rather than the address.
		`mailto:hello@kiff.dev?subject=KIFF%20Cloud%20Growth%20access`,
		`mailto:hello@kiff.dev?subject=KIFF%20Cloud%20Scale%20commitment`,
		`mailto:hello@kiff.dev?subject=KIFF%20Enterprise`,
	}
	for _, banned := range mustNotContainHrefs {
		if strings.Contains(body, banned) {
			t.Errorf("body still renders fallback mailto CTA %q despite intake URL being configured", banned)
		}
	}
}
