package handlers_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// twoDomainControlsAPI serves a tenant with one active control in each
// of two domains (alpha/REFUND, beta/PAYOUT), so the Govern surface's
// domain scoping can be exercised.
func twoDomainControlsAPI(t *testing.T) *httptest.Server {
	t.Helper()
	now := time.Date(2026, 6, 2, 9, 30, 0, 0, time.UTC)
	mux := http.NewServeMux()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			http.Error(w, "missing bearer", http.StatusUnauthorized)
			return
		}
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)

	mux.HandleFunc("GET /v1/me", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONForTest(w, http.StatusOK, map[string]any{
			"subject": "alice", "email": "alice@example.com",
			"tenant_id": "ten-2dc", "tenant_slug": "alice-team",
			"tenant_display_name": "Alice's Team", "api_key": true,
		})
	})
	mux.HandleFunc("GET /v1/me/controls", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONForTest(w, http.StatusOK, map[string]any{
			"controls": []map[string]any{
				{
					"id": "ctrl-alpha", "domain": "alpha", "entity": "Order",
					"actions": []string{"REFUND"}, "environment": "aws", "workflow": "refunds",
					"activated_at": now.Add(-time.Hour).Format(time.RFC3339),
					"activated_by": "alice", "status": "active",
				},
				{
					"id": "ctrl-beta", "domain": "beta", "entity": "Invoice",
					"actions": []string{"PAYOUT"}, "environment": "aws", "workflow": "payouts",
					"activated_at": now.Add(-time.Hour).Format(time.RFC3339),
					"activated_by": "alice", "status": "active",
				},
			},
		})
	})
	emptyDecisions := func(w http.ResponseWriter, _ *http.Request) {
		writeJSONForTest(w, http.StatusOK, map[string]any{
			"control":   map[string]any{"id": "c", "status": "active"},
			"decisions": map[string]any{"total": 0, "counts": map[string]int{}, "rows": []any{}},
			"receipts":  map[string]any{"status": "unavailable", "samples": []any{}},
			"alerts":    map[string]any{"status": "unavailable", "deliveries": []any{}},
			"approvals": map[string]any{"status": "unavailable"},
		})
	}
	mux.HandleFunc("GET /v1/me/controls/ctrl-alpha/decisions", emptyDecisions)
	mux.HandleFunc("GET /v1/me/controls/ctrl-beta/decisions", emptyDecisions)
	return srv
}

func TestGovern_ScopedToDomainFiltersControls(t *testing.T) {
	t.Parallel()
	mgr := newOnboardingTestSessionManager(t)
	gated := newDashboardControlsMux(t, twoDomainControlsAPI(t), mgr)

	req := httptest.NewRequest(http.MethodGet, "/dashboard/govern?domain=alpha", nil)
	req.AddCookie(issueOnboardingSession(t, mgr))
	rec := httptest.NewRecorder()
	gated.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /dashboard/govern?domain=alpha: status %d; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()

	// The header reflects the scope.
	if !strings.Contains(body, "Govern · alpha") {
		t.Errorf("scoped Govern header missing the domain; body=%s", body)
	}
	// The governed-actions nav lists alpha's control card, not beta's.
	// (The card is identified by domain + control link; the verbatim
	// action-name catalog was removed to stop duplicating the Domain page.)
	if !strings.Contains(body, "control_id=ctrl-alpha") {
		t.Error("scoped Govern should list the alpha domain's control")
	}
	if strings.Contains(body, "control_id=ctrl-beta") {
		t.Error("scoped Govern must not list the beta domain's control")
	}
	// The spine nav stays scoped to alpha while on Govern.
	if !strings.Contains(body, `href="/dashboard/domains/alpha/evidence"`) {
		t.Error("scoped Govern nav should keep Activity pointed at alpha's evidence")
	}
}

func TestGovern_UnscopedListsAllControls(t *testing.T) {
	t.Parallel()
	mgr := newOnboardingTestSessionManager(t)
	gated := newDashboardControlsMux(t, twoDomainControlsAPI(t), mgr)

	req := httptest.NewRequest(http.MethodGet, "/dashboard/govern", nil)
	req.AddCookie(issueOnboardingSession(t, mgr))
	rec := httptest.NewRecorder()
	gated.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /dashboard/govern: status %d; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	// Both domains' control cards are present when unscoped (back-compatible).
	for _, want := range []string{"control_id=ctrl-alpha", "control_id=ctrl-beta"} {
		if !strings.Contains(body, want) {
			t.Errorf("unscoped Govern missing control %q", want)
		}
	}
}
