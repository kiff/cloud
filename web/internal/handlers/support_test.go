package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The support page the App Store listing links to (kiff/tafs#185): every
// section renders with a way to reach a person, reply times match the SLA,
// and nothing on it offers a purchase, which Apple's rules on metadata
// forbid outside the US storefront.
func TestSupport(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodGet, "/support", nil)
	rec := httptest.NewRecorder()
	Support().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want %d", rec.Code, http.StatusOK)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("content-type: got %q, want text/html", ct)
	}
	body := rec.Body.String()

	for _, want := range []string{
		`class="site-footer"`,
		`class="legal prose"`,
		`id="write"`, `id="status"`, `id="tafs"`, `id="data"`, `id="security"`,
		`href="mailto:hello@kiff.dev"`,
		`href="mailto:privacy@kiff.dev"`,
		`href="mailto:security@kiff.dev"`,
		`href="mailto:legal@kiff.dev"`,
		`href="https://status.kiff.dev"`,
		`href="/legal/sla#support"`,
		// The SLA's reply times (§5), by reference: no plan is named.
		`<a href="/legal/sla#support">service-level agreement</a>`,
		// Deleting an account, in the app and on the web.
		"<em>Delete account</em>",
		"30-day grace window",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q", want)
		}
	}
	// The page's own text, not the site's navigation, which links to the
	// pricing page as every page does.
	start := strings.Index(body, `<article class="legal prose">`)
	end := strings.Index(body, "</article>")
	if start < 0 || end < start {
		t.Fatalf("no article in the page")
	}
	article := body[start:end]
	for _, banned := range []string{
		"€", "$", "/month", "a month", "Solo", "Team", "plan's", "Buy", "buy", "Upgrade", "upgrade", "checkout", "/pricing", "trial",
		"Merkle", "Sepolia", "on-chain", "anchor", "kiff-cloud", "apps/web/internal",
	} {
		if strings.Contains(article, banned) {
			t.Errorf("the page contains banned token %q", banned)
		}
	}
}
