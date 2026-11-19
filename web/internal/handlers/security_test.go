package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSecurity(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodGet, "/security", nil)
	rec := httptest.NewRecorder()

	Security().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want %d", rec.Code, http.StatusOK)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("content-type: got %q, want text/html", ct)
	}

	body := rec.Body.String()

	// Layout is shared with landing/whitepaper — topbar, footer,
	// and shared CSS must still render.
	mustContain := []string{
		`class="site-nav"`,
		`class="site-footer"`,
		`href="/static/app.css"`,
		`href="/static/css/security.css"`,

		// Page-level shell.
		`class="security"`,
		"What KIFF Cloud signs",

		// All six page sections must render.
		`id="posture"`,
		`id="artifacts"`,
		`id="subprocessors"`,
		`id="incident"`,
		`id="data-we-process"`,
		`id="roadmap"`,
		// kiff/tafs#167: the resource value is hashed by KIFF and kept
		// by tafs for 30 days, never more of the call's input.
		"acts on (such as a payment id)",
		"the identifier of what it acted on (for",

		// "What KIFF enforces" section. The enforce/shadow mode
		// distinction must stay on the page: a security reviewer
		// needs to see that observe-only is a deliberate, opt-in
		// posture, not a gap. If these drift, the page no longer
		// matches the shipped mode behaviour.
		`id="enforcement"`,
		"What KIFF enforces",
		"Observe — advisory-only, in place",
		"never observe-only by accident",

		// Trust-at-a-glance summary (buyer-facing top block). The
		// status vocabulary must stay honest: PCI is out of scope
		// (no cardholder data), HIPAA not in scope today, GDPR in
		// place. If these drift the page overclaims.
		`id="trust"`,
		"Trust at a glance",
		"out of scope",
		"What we can hand a security reviewer today",

		// Data-classification subsections. Every promise on this
		// page must point to a real identifier; if these drift,
		// the policy is no longer the source of truth.
		`id="identifiers"`,
		`id="parameters"`,
		`id="never-persisted"`,
		`id="public-trail"`,
		`id="deletion"`,
		`id="people-data"`,
		`id="terminology"`,
		`id="not-today"`,

		// The honest-roadmap discipline. If this section disappears,
		// the page has drifted toward marketing copy.
		"What we don't yet have",
		"SOC 2 Type I",
		"Cyber-insurance partnership",

		// Sub-processors row content.
		"Amazon Web Services",
		"Clerk",
		// /subprocessors is the official list.
		`href="/subprocessors"`,

		// Incident contact: the page is useless to a security
		// researcher without one.
		"security@kiff.dev",
		// A written commitment to security updates (Directive (EU) 2024/2853).
		`id="updates"`, "We provide security updates for as long as we offer the",

		// Honest-copy regression guards. The page used to claim
		// AWS KMS-backed per-tenant keys, an hourly signing
		// service account, and an internal-change audit trail
		// accessible to compliance auditors. None of those are
		// shipped. The wording below replaces them; if a future
		// edit drifts back toward the overclaims, these
		// assertions fail loudly.
		`<dt>Signing-wallet custody</dt>`,
		`Per-tenant signing-key custody`,
		`is on the roadmap`,
		`<strong>Per-tenant signing-key custody</strong>`,
		`<strong>KMS-backed signing</strong>`,
		`<strong>Self-serve tenant deletion</strong>`,
		`<strong>Audit retention enforcement</strong>`,
	}
	for _, want := range mustContain {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q", want)
		}
	}

	// Honest-copy banned-phrase guards. These specific phrases
	// were the ones the page used to claim and that the
	// deployed code does not back. If they drift back into the
	// page, a technical buyer asking for the artifact will
	// catch us — fail the build instead.
	mustNotContainHonestyDrift := []string{
		// "Per-tenant signing keys live in AWS KMS"
		"signing keys live in AWS KMS",
		// Bedrock runs only in demo scripts, not in production.
		"Amazon Bedrock",
		// "hourly signing job runs under a dedicated service account"
		"hourly signing job",
		// "internal audit trail accessible to compliance auditors"
		"accessible to compliance auditors",
	}
	for _, banned := range mustNotContainHonestyDrift {
		if strings.Contains(body, banned) {
			t.Errorf("body contains overclaim phrase %q (honest-copy regression guard)", banned)
		}
	}

	// Page voice rule: cloud surface copy is
	// property-led, not crypto-led. None of these vocabulary tokens
	// should appear in user-facing copy. Implementation detail
	// belongs in the RFCs.
	mustNotContain := []string{
		"Merkle",
		"Sepolia",
		// "anchor" appears in audit-related verbs in design docs;
		// keep it out of the page copy as well.
		"anchor",
		"on-chain",
	}
	for _, banned := range mustNotContain {
		if strings.Contains(body, banned) {
			t.Errorf("body contains banned cloud-copy token %q (page voice rule)", banned)
		}
	}
}
