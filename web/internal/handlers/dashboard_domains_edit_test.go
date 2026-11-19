package handlers_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// editDomainAPI serves GET /v1/me, GET /v1/me/domains/{domain}/yaml
// (the editor pre-fill), and PUT /v1/me/domains/{domain} with a
// configurable status, so the per-domain edit flow can be exercised
// across its branches.
type editDomainAPI struct {
	yaml      string
	putStatus int
	putBody   map[string]any
}

func (e *editDomainAPI) start(t *testing.T) *httptest.Server {
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
			"tenant_id": "ten-e", "tenant_slug": "alice-team",
			"tenant_display_name": "Alice's Team", "api_key": true,
		})
	})
	mux.HandleFunc("GET /v1/me/domains/{domain}/yaml", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/x-yaml")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(e.yaml))
	})
	mux.HandleFunc("PUT /v1/me/domains/{domain}", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONForTest(w, e.putStatus, e.putBody)
	})
	return srv
}

func TestEditDomain_RendersPrefilledForm(t *testing.T) {
	t.Parallel()
	mgr := newOnboardingTestSessionManager(t)
	api := &editDomainAPI{yaml: "domain: alpha\nentity: Order\n", putStatus: http.StatusOK, putBody: map[string]any{"domain": "alpha"}}
	gated := newDomainsMux(t, api.start(t), mgr)

	req := httptest.NewRequest(http.MethodGet, "/dashboard/domains/alpha/edit", nil)
	req.AddCookie(issueOnboardingSession(t, mgr))
	rec := httptest.NewRecorder()
	gated.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET edit status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{"Edit domain", "domain: alpha", `action="/dashboard/domains/alpha/edit"`, "Save changes"} {
		if !strings.Contains(body, want) {
			t.Errorf("edit form missing %q", want)
		}
	}
}

func TestUpdateDomain_SuccessRedirectsToDetail(t *testing.T) {
	t.Parallel()
	mgr := newOnboardingTestSessionManager(t)
	api := &editDomainAPI{yaml: "domain: alpha\nentity: Order\n", putStatus: http.StatusOK, putBody: map[string]any{"domain": "alpha"}}
	gated := newDomainsMux(t, api.start(t), mgr)

	form := url.Values{"yaml": {"domain: alpha\nentity: Order\nevents: [X]\n"}}
	req := httptest.NewRequest(http.MethodPost, "/dashboard/domains/alpha/edit", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(issueOnboardingSession(t, mgr))
	rec := httptest.NewRecorder()
	gated.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("update status = %d, want 303; body=%s", rec.Code, rec.Body.String())
	}
	if loc := rec.Header().Get("Location"); loc != "/dashboard/domains/alpha" {
		t.Errorf("redirect = %q, want /dashboard/domains/alpha", loc)
	}
}

func TestUpdateDomain_RenameRerenders(t *testing.T) {
	t.Parallel()
	mgr := newOnboardingTestSessionManager(t)
	api := &editDomainAPI{yaml: "domain: alpha\nentity: Order\n", putStatus: http.StatusConflict, putBody: map[string]any{"error": "rename"}}
	gated := newDomainsMux(t, api.start(t), mgr)

	form := url.Values{"yaml": {"domain: renamed\nentity: Order\n"}}
	req := httptest.NewRequest(http.MethodPost, "/dashboard/domains/alpha/edit", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(issueOnboardingSession(t, mgr))
	rec := httptest.NewRecorder()
	gated.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("rename update status = %d, want 200 (re-render); body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "renaming is a separate operation") {
		t.Errorf("rename re-render missing the precise message; body=%s", body)
	}
	// Posted YAML preserved.
	if !strings.Contains(body, "domain: renamed") {
		t.Error("rename re-render did not preserve the posted YAML")
	}
}

func TestUpdateDomain_ValidationRerendersWithIssues(t *testing.T) {
	t.Parallel()
	mgr := newOnboardingTestSessionManager(t)
	api := &editDomainAPI{
		yaml:      "domain: alpha\nentity: Order\n",
		putStatus: http.StatusUnprocessableEntity,
		putBody: map[string]any{
			"error":  "domain fails validation; cannot update",
			"issues": []map[string]any{{"path": "actions[0].executor", "message": "unknown executor"}},
		},
	}
	gated := newDomainsMux(t, api.start(t), mgr)

	form := url.Values{"yaml": {"domain: alpha\nentity: Order\n"}}
	req := httptest.NewRequest(http.MethodPost, "/dashboard/domains/alpha/edit", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(issueOnboardingSession(t, mgr))
	rec := httptest.NewRecorder()
	gated.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("invalid update status = %d, want 200 (re-render); body=%s", rec.Code, rec.Body.String())
	}
	for _, want := range []string{"actions[0].executor", "unknown executor"} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("validation re-render missing issue text %q", want)
		}
	}
}

// TestDomainDetail_ShowsEditAffordance: a domain's home offers the
// per-domain Edit YAML entry point.
func TestDomainDetail_ShowsEditAffordance(t *testing.T) {
	t.Parallel()
	mgr := newTestSessionManager(t)
	gated := newDomainsMux(t, twoDomainAPI(t), mgr)
	body := getDomains(t, gated, mgr, "/dashboard/domains/alpha?tab=settings")
	if !strings.Contains(body, `href="/dashboard/domains/alpha/edit"`) {
		t.Errorf("domain detail should offer an Edit YAML affordance; body=%s", body)
	}
}
