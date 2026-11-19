package handlers_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
)

// deleteDomainAPI serves GET /v1/me and DELETE /v1/me/domains/{domain}
// with a configurable status, recording how many DELETEs it saw so a
// test can assert a mismatched confirmation never calls the API.
type deleteDomainAPI struct {
	status int

	mu      sync.Mutex
	deletes int
}

func (d *deleteDomainAPI) start(t *testing.T) *httptest.Server {
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
			"tenant_id": "ten-d", "tenant_slug": "alice-team",
			"tenant_display_name": "Alice's Team", "api_key": true,
		})
	})
	mux.HandleFunc("DELETE /v1/me/domains/{domain}", func(w http.ResponseWriter, _ *http.Request) {
		d.mu.Lock()
		d.deletes++
		d.mu.Unlock()
		if d.status == http.StatusNoContent {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		writeJSONForTest(w, d.status, map[string]any{"error": "x"})
	})
	return srv
}

func (d *deleteDomainAPI) deleteCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.deletes
}

func TestDeleteDomain_SuccessRedirectsToApex(t *testing.T) {
	t.Parallel()
	mgr := newOnboardingTestSessionManager(t)
	api := &deleteDomainAPI{status: http.StatusNoContent}
	gated := newDomainsMux(t, api.start(t), mgr)

	form := url.Values{"confirm_name": {"beta"}}
	req := httptest.NewRequest(http.MethodPost, "/dashboard/domains/beta/delete", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(issueOnboardingSession(t, mgr))
	rec := httptest.NewRecorder()
	gated.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("delete status = %d, want 303; body=%s", rec.Code, rec.Body.String())
	}
	if loc := rec.Header().Get("Location"); loc != "/dashboard/domains" {
		t.Errorf("redirect = %q, want /dashboard/domains", loc)
	}
	if api.deleteCount() != 1 {
		t.Errorf("expected exactly one API DELETE, got %d", api.deleteCount())
	}
}

func TestDeleteDomain_ConfirmMismatchSkipsAPI(t *testing.T) {
	t.Parallel()
	mgr := newOnboardingTestSessionManager(t)
	api := &deleteDomainAPI{status: http.StatusNoContent}
	gated := newDomainsMux(t, api.start(t), mgr)

	// Confirmation does not match the slug → must not call the API.
	form := url.Values{"confirm_name": {"wrong"}}
	req := httptest.NewRequest(http.MethodPost, "/dashboard/domains/beta/delete", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(issueOnboardingSession(t, mgr))
	rec := httptest.NewRecorder()
	gated.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("mismatch status = %d, want 303; body=%s", rec.Code, rec.Body.String())
	}
	if loc := rec.Header().Get("Location"); loc != "/dashboard/domains/beta" {
		t.Errorf("mismatch redirect = %q, want /dashboard/domains/beta", loc)
	}
	if api.deleteCount() != 0 {
		t.Errorf("a mismatched confirmation must not call the API; got %d DELETEs", api.deleteCount())
	}
}

func TestDeleteDomain_DefaultIs409RedirectsToDetail(t *testing.T) {
	t.Parallel()
	mgr := newOnboardingTestSessionManager(t)
	api := &deleteDomainAPI{status: http.StatusConflict}
	gated := newDomainsMux(t, api.start(t), mgr)

	form := url.Values{"confirm_name": {"alpha"}}
	req := httptest.NewRequest(http.MethodPost, "/dashboard/domains/alpha/delete", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(issueOnboardingSession(t, mgr))
	rec := httptest.NewRecorder()
	gated.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("default delete status = %d, want 303; body=%s", rec.Code, rec.Body.String())
	}
	if loc := rec.Header().Get("Location"); loc != "/dashboard/domains/alpha" {
		t.Errorf("default delete redirect = %q, want /dashboard/domains/alpha (stays on detail)", loc)
	}
}

// TestDomainDetail_ShowsDeleteWhenMultiDomain: the guarded delete
// danger zone renders on a domain's home only when the tenant has more
// than one domain.
func TestDomainDetail_ShowsDeleteWhenMultiDomain(t *testing.T) {
	t.Parallel()
	mgr := newTestSessionManager(t)
	gated := newDomainsMux(t, twoDomainAPI(t), mgr)
	body := getDomains(t, gated, mgr, "/dashboard/domains/alpha?tab=settings")

	if !strings.Contains(body, "Delete this domain") {
		t.Error("multi-domain detail should show the delete danger zone")
	}
	if !strings.Contains(body, `action="/dashboard/domains/alpha/delete"`) {
		t.Errorf("delete form should post to the domain's delete route; body=%s", body)
	}
}
