package handlers_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// twoDomainAPI serves a tenant with two domains (alpha, beta) so the
// multi-domain index + switcher can be exercised.
func twoDomainAPI(t *testing.T) *httptest.Server {
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
			"tenant_id": "ten-2d", "tenant_slug": "alice-team",
			"tenant_display_name": "Alice's Team", "api_key": true,
		})
	})
	mux.HandleFunc("GET /v1/me/domains", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONForTest(w, http.StatusOK, map[string]any{
			"domains": []map[string]any{
				{"name": "alpha", "entity": "Order", "version": 1, "status": "active", "action_count": 1},
				{"name": "beta", "entity": "Invoice", "version": 1, "status": "active", "action_count": 2},
			},
		})
	})
	mux.HandleFunc("GET /v1/me/domains/{domain}", func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("domain")
		if name != "alpha" && name != "beta" {
			writeJSONForTest(w, http.StatusNotFound, map[string]any{"error": "domain not found"})
			return
		}
		writeJSONForTest(w, http.StatusOK, map[string]any{
			"name": name, "entity": "Order", "version": 1, "status": "active",
			"actions": []map[string]any{{
				"name": "PAY", "risk": "high", "approval": "required",
				"allowed_states": []string{"PENDING"}, "executor": "cloud.proxy",
			}},
			"lifecycle": map[string]any{"states": []string{"PENDING"}, "events": []any{}, "transitions": []any{}},
			"agents":    map[string]any{"status": "unavailable", "agents": []any{}},
			"evidence":  map[string]any{},
		})
	})
	// decorateDomainHome fan-out (non-fatal, minimal payloads).
	mux.HandleFunc("GET /v1/me/domains/{domain}/usage", func(w http.ResponseWriter, r *http.Request) {
		writeJSONForTest(w, http.StatusOK, map[string]any{"domain": r.PathValue("domain"), "counters": map[string]uint64{}})
	})
	mux.HandleFunc("GET /v1/me/domains/{domain}/evidence", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONForTest(w, http.StatusOK, map[string]any{
			"control":   map[string]any{"id": "c", "status": "active"},
			"decisions": map[string]any{"total": 0, "counts": map[string]int{}, "rows": []any{}},
			"receipts":  map[string]any{"status": "unavailable", "samples": []any{}},
			"alerts":    map[string]any{"status": "unavailable", "deliveries": []any{}},
			"approvals": map[string]any{"status": "unavailable"},
		})
	})
	mux.HandleFunc("GET /v1/me/usage", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONForTest(w, http.StatusOK, map[string]any{"tenant_id": "ten-2d", "plan": "free_dev", "counters": map[string]uint64{}})
	})
	mux.HandleFunc("GET /v1/approvals", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONForTest(w, http.StatusOK, map[string]any{"approvals": []any{}})
	})
	return srv
}

// TestDashboardDomains_MultiDomainApexListsAll: a tenant with two
// domains sees both on the apex index (neither is collapsed away).
func TestDashboardDomains_MultiDomainApexListsAll(t *testing.T) {
	t.Parallel()
	mgr := newTestSessionManager(t)
	gated := newDomainsMux(t, twoDomainAPI(t), mgr)
	body := getDomains(t, gated, mgr, "/dashboard/domains")
	for _, want := range []string{"alpha", "beta"} {
		if !strings.Contains(body, want) {
			t.Errorf("apex index missing domain %q", want)
		}
	}
}

// TestDashboardDomains_DetailShowsSwitcher: on a domain's detail page,
// a multi-domain tenant gets a switcher listing the sibling domains so
// it can move between them without bouncing through the apex.
func TestDashboardDomains_DetailShowsSwitcher(t *testing.T) {
	t.Parallel()
	mgr := newTestSessionManager(t)
	gated := newDomainsMux(t, twoDomainAPI(t), mgr)
	body := getDomains(t, gated, mgr, "/dashboard/domains/alpha")

	if !strings.Contains(body, "dash-domain-switcher") {
		t.Fatal("detail page missing the domain switcher")
	}
	// The sibling domain is linked for switching.
	if !strings.Contains(body, `href="/dashboard/domains/beta"`) {
		t.Errorf("switcher missing a link to the sibling domain beta; body=%s", body)
	}
	// The current domain is marked, not re-linked.
	if !strings.Contains(body, "dash-domain-chip-current") {
		t.Error("switcher does not mark the current domain")
	}
}

// TestDashboardDomains_SpineNavScopedToSelectedDomain: on a domain's
// home, the spine's domain-scoped facet (Govern) carries the selected
// domain so navigating stays in-domain. Activity is every call across
// every domain; Build/Runtimes stay workspace-level.
func TestDashboardDomains_SpineNavScopedToSelectedDomain(t *testing.T) {
	t.Parallel()
	mgr := newTestSessionManager(t)
	gated := newDomainsMux(t, twoDomainAPI(t), mgr)
	body := getDomains(t, gated, mgr, "/dashboard/domains/alpha")

	for _, want := range []string{
		`href="/dashboard/govern?domain=alpha"`,
		`href="/activity"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("spine nav missing domain-scoped link %q", want)
		}
	}
	// Build stays workspace-level (per-domain authoring is Slice 3).
	if !strings.Contains(body, `href="/dashboard/build"`) {
		t.Error("Build nav link should stay workspace-level")
	}
}
