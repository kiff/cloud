package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The pages that spell out the Terms: acceptable use, subprocessors and
// cookies. Each must render in the legal layout, keep its load-bearing
// facts, and keep to the page-voice rule.
func TestLegalPages(t *testing.T) {
	t.Parallel()

	cases := []struct {
		path    string
		handler http.Handler
		want    []string
	}{
		{"/acceptable-use", AcceptableUse(), []string{
			"Acceptable use policy",
			`id="unlawful"`, `id="unauthorized"`, `id="fraud"`, `id="third-party"`,
			`id="bypass"`, `id="security-testing"`, `id="spam"`, `id="illegal-content"`,
			`id="malware"`, `id="other-tenants"`, `id="competing"`,
			"child sexual abuse", "Reverse-engineer",
			`id="enforcement"`, `id="reporting"`,
			"refuse", "suspend", "end",
			"or when people or money are at immediate risk",
			"security@kiff.dev", "hello@kiff.dev",
			`href="/terms"`,
		}},
		{"/subprocessors", Subprocessors(), []string{
			"Subprocessors",
			"Amazon Web Services", "Clerk", "Stripe", "Resend", "Apple", "Anthropic",
			"us-east-1", "eu-west-1", "official list",
			"Apple, Google or GitHub",
			"It receives no",
			"Standard Contractual Clauses",
			`href="/legal/dpa#sub-processors"`,
		}},
		{"/cookies", Cookies(), []string{
			"Cookie policy",
			"kiff_session", "kiff_flash", "kiff_cb_attempts",
			"24 hours", "5 minutes", "<code>.kiff.dev</code>",
			`href="https://clerk.com/docs/guides/how-clerk-works/cookies"`,
			"no analytics cookies, no advertising cookies",
			"kiff-theme",
		}},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			t.Parallel()
			rec := httptest.NewRecorder()
			tc.handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.path, nil))
			if rec.Code != http.StatusOK {
				t.Fatalf("status: got %d, want %d", rec.Code, http.StatusOK)
			}
			body := rec.Body.String()
			want := append([]string{
				`class="site-footer"`, `class="legal prose"`, `class="legal-badge"`,
				`href="/acceptable-use"`, `href="/subprocessors"`, `href="/cookies"`,
			}, tc.want...)
			for _, s := range want {
				if !strings.Contains(body, s) {
					t.Errorf("body missing %q", s)
				}
			}
			for _, banned := range []string{
				"Merkle", "Sepolia", "on-chain", "anchor", "kiff-cloud",
				"apps/web/internal", "legal review", "Status:",
				"Bedrock", "GitLab",
			} {
				if strings.Contains(body, banned) {
					t.Errorf("body contains banned token %q", banned)
				}
			}
		})
	}
}
