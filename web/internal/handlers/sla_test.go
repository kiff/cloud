package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The SLA the Terms (§14) promise before any paid plan: every section
// renders, with its commitments, and the page voice holds.
func TestSLA(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodGet, "/legal/sla", nil)
	rec := httptest.NewRecorder()
	SLA().ServeHTTP(rec, req)

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
		`id="scope"`, `id="availability"`, `id="credits"`, `id="exclusions"`,
		`id="support"`, `id="incidents"`, `id="general"`,
		// The commitments themselves.
		"99.5% monthly availability",
		"within 30 seconds",
		"10%", "25%", "50%",
		"within 30 days after the month ends",
		"2 business days", "1 business day",
		// Paid plans only, and our own hosting's outage counts.
		"The trial, and any feature we call a preview",
		"it counts as downtime",
		"/terms", "/legal/dpa", "/security",
		// The status page the SLA's incidents and maintenance are posted on.
		`href="https://status.kiff.dev"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q", want)
		}
	}
	for _, banned := range []string{"Merkle", "Sepolia", "on-chain", "anchor", "kiff-cloud", "apps/web/internal", "Status:"} {
		if strings.Contains(body, banned) {
			t.Errorf("body contains banned token %q (page voice rule)", banned)
		}
	}
}
