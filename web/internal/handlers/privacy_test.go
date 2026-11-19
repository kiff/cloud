package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPrivacy(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodGet, "/privacy", nil)
	rec := httptest.NewRecorder()

	Privacy().ServeHTTP(rec, req)

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
		"Privacy policy",

		// Entity disclosure (Estonia, registry data). These are the
		// load-bearing facts; if any of them disappear the page
		// stops functioning as a privacy notice.
		"Kiff Agents OÜ",
		"17372313",
		"EE102924327",
		"Tallinn",
		"Estonia",
		"privacy@kiff.dev",
		"legal@kiff.dev",

		// Every section anchor must render.
		`id="who-we-are"`,
		`id="scope"`,
		`id="roles"`,
		`id="categories"`,
		`id="lawful-bases"`,
		`id="sub-processors"`,
		`id="transfers"`,
		`id="retention"`,
		// Deleting an account: in the app, or by email for a web account,
		// with the same 30-day grace window the support page names.
		"An account made on the web is deleted",
		"keep its data for a grace window of 30 days, in case",
		`id="rights"`,
		`id="cookies"`,
		`id="security"`,
		`id="children"`,
		`id="automated"`,
		`id="changes"`,
		`id="contact"`,

		// GDPR articles named explicitly. Keeps the page legible
		// to a regulator's eye and makes drift visible.
		"Article 6",
		"Article 12(3)",
		"Article 22",
		"Article 33",

		// Cookie disclosure. The three strictly-necessary cookies are
		// the entire reason no consent banner exists; if a name
		// disappears, the disclosure has drifted. Each must match the
		// cookie the code sets (session.CookieName,
		// session.FlashCookieName, the sign-in callback's guard).
		"kiff_session",
		"kiff_flash",
		"kiff_cb_attempts",
		"strictly necessary",

		// Lead supervisory authority — the link a complainant
		// follows.
		"Andmekaitse Inspektsioon",
	}
	for _, want := range mustContain {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q", want)
		}
	}

	// Page voice rule: cloud surface copy is
	// property-led, not crypto-led. Implementation-detail
	// vocabulary stays out of the rendered page; it belongs in
	// the design RFCs.
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
	}
	for _, banned := range mustNotContain {
		if strings.Contains(body, banned) {
			t.Errorf("body contains banned cloud-copy token %q (page voice rule)", banned)
		}
	}
}
