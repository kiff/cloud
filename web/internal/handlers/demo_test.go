package handlers_test

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kiff/cloud/web/internal/handlers"
)

func TestDemoAppRendersRealDashboardWithFixtureData(t *testing.T) {
	t.Parallel()
	h := handlers.DemoApp(slog.New(slog.NewTextHandler(io.Discard, nil)))
	req := httptest.NewRequest(http.MethodGet, "/demo/dashboard/domains?tour=1", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{
		`class="dash-shell"`,
		"northstar-operations",
		"accounts-payable",
		"card-refund",
		`href="/demo/dashboard/runtimes"`,
		`href="/demo/cards"`,
		`src="/static/js/demo-app.js"`,
		`href="/static/css/demo-app.css"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("demo body missing %q", want)
		}
	}
	for _, reject := range []string{"demo-screen", "demo-sidebar", "payments.refund"} {
		if strings.Contains(body, reject) {
			t.Errorf("demo body still contains rejected mock marker %q", reject)
		}
	}
	if got := rec.Header().Get("Set-Cookie"); got != "" {
		t.Fatalf("public demo must not issue account cookies, got %q", got)
	}
	if got := rec.Header().Get("X-Robots-Tag"); got != "noindex" {
		t.Fatalf("X-Robots-Tag = %q, want noindex", got)
	}
}

func TestDemoAppMountExposesDashboardSections(t *testing.T) {
	t.Parallel()
	h := handlers.DemoApp(slog.New(slog.NewTextHandler(io.Discard, nil)))
	tests := []struct {
		path string
		want string
	}{
		{"/demo/dashboard/runtimes", "ap-payment-agent"},
		{"/demo/dashboard/keys", "production runtime"},
		{"/demo/dashboard/notifications", "waiting for approval"},
		{"/demo/dashboard/settings", "northstar-operations"},
		{"/demo/dashboard/support", "Add a second approval group"},
		{"/demo/dashboard/build?domain=accounts-payable", "PAY_INVOICE"},
		{"/demo/dashboard/govern?control_id=ctrl-ap-pay", "Enforced before execution"},
		{"/demo/dashboard/domains/accounts-payable/evidence?tab=decisions", "inv-2026-0842"},
		{"/demo/dashboard/receipts/trace-pay-3420", "PAY_INVOICE on inv-2026-0842"},
		{"/demo/cards", "card-refunds-eu"},
		{"/demo/agents", "support-agent"},
		{"/demo/agents/support-agent", "kiff-guard agno 1.1.0 in enforce mode"},
		{"/demo/agents/support-agent/cards", "card-refunds-eu"},
		{"/demo/cards/card-store-credits", "card-store-credits"},
		{"/demo/cards/card-plan-changes", "card-plan-changes"},
		{"/demo/cards/card-refunds-eu/statement", "proposal-demo-card-refunds-eu"},
		{"/demo/cards/card-refunds-eu/changes", "Issued and changed"},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), tt.want) {
				t.Fatalf("body missing %q", tt.want)
			}
		})
	}
}

func TestDemoCardsStayInsideDemoNamespace(t *testing.T) {
	t.Parallel()
	h := handlers.DemoApp(slog.New(slog.NewTextHandler(io.Discard, nil)))
	req := httptest.NewRequest(http.MethodGet, "/demo/cards", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{
		`href="/demo/cards/new"`,
		`href="/demo/cards/card-refunds-eu"`,
		`href="/demo/cards/card-store-credits"`,
		`href="/demo/cards/card-plan-changes"`,
		"support-agent",
		"€5,000 a day",
		"€1,000",
		"Active",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("demo Cards body missing %q", want)
		}
	}
	if strings.Contains(body, `href="/cards`) || strings.Contains(body, `action="/cards`) {
		t.Error("demo Cards page contains a link that escapes into the authenticated Cards surface")
	}
	if strings.Contains(body, "Expired") {
		t.Error("demo fixture renders its example cards as expired")
	}
}

func TestDemoAppRewritesActionRedirectsAndStripsCookies(t *testing.T) {
	t.Parallel()
	h := handlers.DemoApp(slog.New(slog.NewTextHandler(io.Discard, nil)))
	req := httptest.NewRequest(http.MethodPost, "/demo/dashboard/approvals/approval-842/grant", strings.NewReader("reason=reviewed"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303; body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Location"); !strings.HasPrefix(got, "/demo/dashboard/") {
		t.Fatalf("Location = %q, want demo namespace", got)
	}
	if got := rec.Header().Get("Set-Cookie"); got != "" {
		t.Fatalf("public demo must strip flash/session cookies, got %q", got)
	}
}
