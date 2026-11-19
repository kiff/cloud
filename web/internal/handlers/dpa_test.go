package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDPA(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodGet, "/legal/dpa", nil)
	rec := httptest.NewRecorder()

	DPA().ServeHTTP(rec, req)

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
		`class="legal prose"`,

		// Public document badge.
		`class="legal-badge"`,
		"DPA template",

		// Entity facts (Processor side).
		"Kiff Agents OÜ",
		"17372313",
		"EE102924327",
		"Tallinn",

		// Every section / annex anchor must render.
		`id="parties"`,
		`id="definitions"`,
		`id="scope"`,
		`id="subject-matter"`,
		`id="nature-purpose"`,
		`id="categories"`,
		`id="instructions"`,
		`id="processor-obligations"`,
		`id="confidentiality"`,
		`id="sub-processors"`,
		`id="transfers"`,
		`id="security"`,
		`id="breach-notification"`,
		`id="data-subject-requests"`,
		`id="audit"`,
		`id="return-deletion"`,
		`id="liability"`,
		`id="term"`,
		`id="governing-law"`,
		`id="annex-1"`,
		`id="annex-2"`,
		`id="annex-3"`,
		// kiff/tafs#167: what each call acted on, kept 30 days.
		"What each call acted on",
		"for example a payment id",

		// Article 28 GDPR clause-by-clause language. Without
		// these the DPA stops functioning under Article 28(3).
		"Article 28",
		"Article 32",
		"Article 33",
		"Article 82",
		"Standard Contractual Clauses",
		"Module 2",
		"Module 3",
		"seventy-two (72)",

		// Sub-processor Annex, pointing to the official list.
		"Amazon Web Services",
		"Clerk",
		`href="/subprocessors"`,
		"receives summary hashes only and no Personal Data",

		// §6 default categories, numbering without a gap, and the
		// audit wording.
		"Records of agent actions",
		"Chats with tafs",
		"<h2>10. Sub-processors</h2>",
		"<h2>19. Governing law</h2>",
		"no SOC 2 or ISO 27001 report yet",

		// Customer placeholder fields stay visible until a
		// signed instance fills them in.
		"[CUSTOMER LEGAL NAME]",
		"[ADDRESS]",
		"[REGISTRY CODE]",

		// Cross-references.
		"/security",
		"/privacy",
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
		"do not sign",
		"Coinbase",
		"<h2>20.",
		"pending external legal review",
		"Status:",
		// Annex 2 states what is done, not what is planned.
		"planned third-party assessment",
	}
	for _, banned := range mustNotContain {
		if strings.Contains(body, banned) {
			t.Errorf("body contains banned cloud-copy token %q (page voice rule)", banned)
		}
	}
}
