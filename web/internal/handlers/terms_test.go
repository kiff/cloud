package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTerms(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodGet, "/terms", nil)
	rec := httptest.NewRecorder()

	Terms().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want %d", rec.Code, http.StatusOK)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("content-type: got %q, want text/html", ct)
	}

	body := rec.Body.String()

	mustContain := []string{
		// Layout shell.
		`class="site-nav"`,
		`class="site-footer"`,
		`href="/static/app.css"`,
		`href="/static/css/prose.css"`,
		`href="/static/css/legal.css"`,
		`class="legal prose"`,

		// Public document badge.
		`class="legal-badge"`,
		"Terms of service",

		// Entity facts.
		"Kiff Agents OÜ",
		"17372313",
		"Tallinn",
		"Estonia",
		"legal@kiff.dev",

		// Every section anchor must render.
		`id="definitions"`,
		`id="agreement"`,
		`id="service"`,
		`id="account"`,
		`id="acceptable-use"`,
		`id="customer-data"`,
		`id="confidentiality"`,
		`id="fees"`,
		`id="term"`,
		`id="warranties"`,
		`id="disclaimer"`,
		`id="liability"`,
		`id="indemnification"`,
		`id="sla"`,
		`id="service-mods"`,
		`id="terms-mods"`,
		`id="force-majeure"`,
		`id="governing-law"`,
		`id="general"`,
		`id="contact"`,

		// Load-bearing contract clauses. If any of these
		// disappear the doc has stopped functioning as a ToS.
		"€100,000",
		"Estonian",
		"Harju County Court",
		"thirty days",
		"twelve months",
		"€100,000",

		// Governing-law consumer carve-out.
		"consumer",

		// §5 points to the Acceptable Use Policy, which holds the list.
		`href="/acceptable-use"`,

		// Cross-references.
		"/privacy",
		"/security",
		"/legal/dpa",
		"/legal/sla",
		// The footer's Trust column links the status page.
		`href="https://status.kiff.dev"`,

		// Paid plans in credits (pricing phase 2): the plans, what's
		// counted, no trial, VAT, cancellation, and the halt as built.
		"€12.50 a month, VAT included", "€10.50 a month for each seat",
		"500 credits a month for each seat, pooled", "500 actions a month for each seat, pooled",
		"a credit costs €0.01 and an action €0.02", "monthly spend limit the account sets (€50 when it starts)",
		"Packs of credits.", "isn't a subscription", "until the next period begins or a pack is added", "other than Coach's setting up",
		"A <em>credit</em> pays", "whose call KIFF refused still used its AI",
		"Approvers and viewers are free on every plan",
		"or whose outcome is unknown",
		"No trial.", "An account pays from its first",
		"EE102924327",
		"reverse-charge",
		"within 14 days of",
		// The owner's answers: bonus credits never expire, and Solo
		// bought in the app is refunded by its store.
		"Bonus credits that KIFF adds to an account", "don't expire either, like packs.",
		"Solo purchases are refunded through the App Store or Google Play, under their terms.",
		"Last updated:</strong> 9 November 2026.",
		// Leaving and switching (EU Data Act, Chapter VI): export any
		// time, at most two months' notice, a thirty-day transition,
		// thirty more days to export, and no charge.
		`id="leaving"`, "never require more than two months' notice",
		"period of thirty days follows",
		"no more than seven months", "at least thirty more days",
		"We don't charge for leaving, switching or exporting",
		// Product liability can't be excluded (Directive (EU) 2024/2853).
		"2024/2853 on liability for defective products",
		"your Cards with every version", "continue as usual until the transition period ends",
		"KIFF and planque have both",
		"most 90 seconds",
		"Coach, and setting tafs up, are included within Coach's",
	}
	for _, want := range mustContain {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q", want)
		}
	}

	// Page-voice rule.
	mustNotContain := []string{
		"Merkle",
		"Sepolia",
		"on-chain",
		"anchor",
		"kiff-cloud",
		"Source of truth",
		"apps/web/internal",
		"pending legal review",
		"lawyer-reviewed",
		"Status:",
		// Export after leaving is a right, not a favour.
		"at our discretion",
		// The beta's wording is gone with paid plans.
		"free of charge",
		"private beta",
		"stops its authority at once",
		// No notice to accounts from before paid plans: they're all ours
		// (the owner). §16's notice of a price increase stays.
		"Accounts from before paid plans",
		// Tasks and the trial gave way to credits (pricing phase 2).
		"7-day trial", "Task packs", "tafs tasks", "20 tasks", "Team:", "on the trial", "AI use is never",
	}
	for _, banned := range mustNotContain {
		if strings.Contains(body, banned) {
			t.Errorf("body contains banned cloud-copy token %q (page voice rule)", banned)
		}
	}
}
