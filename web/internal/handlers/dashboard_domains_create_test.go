package handlers_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// createDomainAPI serves GET /v1/me (for page chrome) and POST
// /v1/me/domains with a configurable status + body, so the web create
// flow can be exercised against each branch: 201 success, 409 clash,
// 422 validation.
func createDomainAPI(t *testing.T, status int, body map[string]any) *httptest.Server {
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
			"tenant_id": "ten-c", "tenant_slug": "alice-team",
			"tenant_display_name": "Alice's Team", "api_key": true,
		})
	})
	mux.HandleFunc("POST /v1/me/domains", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONForTest(w, status, body)
	})
	return srv
}

func TestNewDomain_RendersForm(t *testing.T) {
	t.Parallel()
	mgr := newOnboardingTestSessionManager(t)
	srv := createDomainAPI(t, http.StatusCreated, map[string]any{"domain": "gamma"})
	gated := newDomainsMux(t, srv, mgr)

	req := httptest.NewRequest(http.MethodGet, "/dashboard/domains/new", nil)
	req.AddCookie(issueOnboardingSession(t, mgr))
	rec := httptest.NewRecorder()
	gated.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /dashboard/domains/new: status %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{"New domain", `action="/dashboard/domains"`, `name="yaml"`, "Create domain"} {
		if !strings.Contains(body, want) {
			t.Errorf("new-domain form missing %q", want)
		}
	}
}

func TestCreateDomain_SuccessRedirects(t *testing.T) {
	t.Parallel()
	mgr := newOnboardingTestSessionManager(t)
	srv := createDomainAPI(t, http.StatusCreated, map[string]any{"domain": "gamma", "entity": "Order"})
	gated := newDomainsMux(t, srv, mgr)

	form := url.Values{"yaml": {"domain: gamma\nentity: Order\n"}}
	req := httptest.NewRequest(http.MethodPost, "/dashboard/domains", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(issueOnboardingSession(t, mgr))
	rec := httptest.NewRecorder()
	gated.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("create status = %d, want 303; body=%s", rec.Code, rec.Body.String())
	}
	if loc := rec.Header().Get("Location"); loc != "/dashboard/domains/gamma" {
		t.Errorf("redirect = %q, want /dashboard/domains/gamma", loc)
	}
}

func TestCreateDomain_ConflictRerendersWithYAML(t *testing.T) {
	t.Parallel()
	mgr := newOnboardingTestSessionManager(t)
	srv := createDomainAPI(t, http.StatusConflict, map[string]any{"error": "exists"})
	gated := newDomainsMux(t, srv, mgr)

	const yaml = "domain: gamma\nentity: Order\n"
	form := url.Values{"yaml": {yaml}}
	req := httptest.NewRequest(http.MethodPost, "/dashboard/domains", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(issueOnboardingSession(t, mgr))
	rec := httptest.NewRecorder()
	gated.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("conflict create status = %d, want 200 (re-render); body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "already exists") {
		t.Errorf("conflict page missing the name-taken message; body=%s", body)
	}
	// The posted YAML is preserved so the user does not lose their work.
	if !strings.Contains(body, "domain: gamma") {
		t.Errorf("conflict page did not preserve the posted YAML")
	}
}

func TestCreateDomain_ValidationRerendersWithIssues(t *testing.T) {
	t.Parallel()
	mgr := newOnboardingTestSessionManager(t)
	srv := createDomainAPI(t, http.StatusUnprocessableEntity, map[string]any{
		"error": "domain fails validation; cannot create",
		"issues": []map[string]any{
			{"path": "actions[0].executor", "message": "unknown executor"},
		},
	})
	gated := newDomainsMux(t, srv, mgr)

	form := url.Values{"yaml": {"domain: gamma\nentity: Order\n"}}
	req := httptest.NewRequest(http.MethodPost, "/dashboard/domains", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(issueOnboardingSession(t, mgr))
	rec := httptest.NewRecorder()
	gated.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("invalid create status = %d, want 200 (re-render); body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{"actions[0].executor", "unknown executor"} {
		if !strings.Contains(body, want) {
			t.Errorf("validation page missing issue text %q", want)
		}
	}
}

func TestCreateDomain_LimitReachedRerenders(t *testing.T) {
	t.Parallel()
	mgr := newOnboardingTestSessionManager(t)
	srv := createDomainAPI(t, http.StatusForbidden, map[string]any{
		"error":       "domain limit reached for this plan; delete a domain or upgrade",
		"max_domains": 5, "count": 5,
	})
	gated := newDomainsMux(t, srv, mgr)

	form := url.Values{"yaml": {"domain: gamma\nentity: Order\n"}}
	req := httptest.NewRequest(http.MethodPost, "/dashboard/domains", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(issueOnboardingSession(t, mgr))
	rec := httptest.NewRecorder()
	gated.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("limit-reached create status = %d, want 200 (re-render); body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "domain limit") {
		t.Errorf("limit-reached page missing the upgrade/delete message; body=%s", rec.Body.String())
	}
}

func TestCreateDomain_EmptyYAMLRerenders(t *testing.T) {
	t.Parallel()
	mgr := newOnboardingTestSessionManager(t)
	srv := createDomainAPI(t, http.StatusCreated, map[string]any{"domain": "gamma"})
	gated := newDomainsMux(t, srv, mgr)

	form := url.Values{"yaml": {"   "}}
	req := httptest.NewRequest(http.MethodPost, "/dashboard/domains", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(issueOnboardingSession(t, mgr))
	rec := httptest.NewRecorder()
	gated.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("empty create status = %d, want 200 (re-render); body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Paste the domain YAML") {
		t.Errorf("empty create did not prompt for YAML")
	}
}
