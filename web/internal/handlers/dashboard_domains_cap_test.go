package handlers_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// capAPI serves a tenant whose /v1/me carries a max_domains cap and
// whose /v1/me/domains lists n domains, so the apex's "N of M" count
// and the gated New-domain affordance can be exercised. n > 1 so the
// apex renders the list view.
func capAPI(t *testing.T, maxDomains, n int) *httptest.Server {
	t.Helper()
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
			"tenant_id": "ten-cap", "tenant_slug": "alice-team",
			"tenant_display_name": "Alice's Team", "api_key": true,
			"max_domains": maxDomains,
		})
	})
	mux.HandleFunc("GET /v1/me/domains", func(w http.ResponseWriter, _ *http.Request) {
		domains := make([]map[string]any, 0, n)
		for i := 0; i < n; i++ {
			domains = append(domains, map[string]any{
				"name": fmt.Sprintf("d%d", i), "entity": "Order",
				"version": 1, "status": "active", "action_count": 1,
			})
		}
		writeJSONForTest(w, http.StatusOK, map[string]any{"domains": domains})
	})
	return srv
}

func TestDomainsApex_UnderCapShowsCountAndButton(t *testing.T) {
	t.Parallel()
	mgr := newTestSessionManager(t)
	gated := newDomainsMux(t, capAPI(t, 5, 2), mgr)
	body := getDomains(t, gated, mgr, "/dashboard/domains")

	if !strings.Contains(body, "2 of 5 domains") {
		t.Errorf("apex should show the plan count '2 of 5 domains'; body=%s", body)
	}
	if !strings.Contains(body, `href="/dashboard/domains/new"`) {
		t.Error("under the cap, the New domain button should be present")
	}
}

func TestDomainsApex_AtCapHidesButton(t *testing.T) {
	t.Parallel()
	mgr := newTestSessionManager(t)
	gated := newDomainsMux(t, capAPI(t, 2, 2), mgr)
	body := getDomains(t, gated, mgr, "/dashboard/domains")

	if !strings.Contains(body, "2 of 2 domains") {
		t.Errorf("apex should show '2 of 2 domains' at the cap; body=%s", body)
	}
	if !strings.Contains(body, "Domain limit reached") {
		t.Error("at the cap, the limit-reached note should show")
	}
	if strings.Contains(body, `href="/dashboard/domains/new"`) {
		t.Error("at the cap, the New domain button must be hidden")
	}
}
