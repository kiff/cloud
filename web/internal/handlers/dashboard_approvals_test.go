package handlers_test

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kiff/cloud/web/internal/apiclient"
	"github.com/kiff/cloud/web/internal/handlers"
	"github.com/kiff/cloud/web/internal/session"
)

// dashboard_approvals_test.go covers the approval grant/deny POST
// handlers. The standalone /dashboard/approvals queue page was retired
// in the authenticated-surface design: the actionable queue now renders
// inside the domain's Activity view (see
// TestDashboardDomainEvidence_RendersPendingApprovalQueue in
// dashboard_domains_test.go), and GET /dashboard/approvals redirects
// there. The grant/deny POST handlers are unchanged and still post to
// /v1/approvals/{id}/grant|deny; after resolving they return the
// reviewer to the domain Activity view they acted from.

// approvalsFakeAPI serves the approval grant/deny endpoints the POST
// handlers drive. Stores pending approvals in-process; tests seed them
// via seedApproval.
type approvalsFakeAPI struct {
	mu sync.Mutex

	approvals map[string]apiclient.PendingApproval
	observed  []string
	meRoles   []string
}

func (f *approvalsFakeAPI) start(t *testing.T) *httptest.Server {
	t.Helper()
	if f.approvals == nil {
		f.approvals = map[string]apiclient.PendingApproval{}
	}
	mux := http.NewServeMux()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			http.Error(w, "missing bearer", http.StatusUnauthorized)
			return
		}
		f.mu.Lock()
		f.observed = append(f.observed, r.Method+" "+r.URL.Path)
		f.mu.Unlock()
		mux.ServeHTTP(w, r)
	}))

	mux.HandleFunc("GET /v1/me", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONForTest(w, http.StatusOK, map[string]any{
			"subject":             "alice",
			"email":               "alice@example.com",
			"tenant_id":           "ten-test",
			"tenant_slug":         "alice-team",
			"tenant_display_name": "Alice's Team",
			"api_key":             false,
			"roles":               f.meRoles,
		})
	})
	mux.HandleFunc("POST /v1/approvals/{id}/grant", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		f.mu.Lock()
		defer f.mu.Unlock()
		a, ok := f.approvals[id]
		if !ok {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		a.Status = "granted"
		a.ReviewedAt = time.Now().UTC()
		a.ReviewedBy = "subj"
		f.approvals[id] = a
		writeJSONForTest(w, http.StatusOK, a)
	})
	mux.HandleFunc("POST /v1/approvals/{id}/deny", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		f.mu.Lock()
		defer f.mu.Unlock()
		a, ok := f.approvals[id]
		if !ok {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		a.Status = "denied"
		a.ReviewedAt = time.Now().UTC()
		a.ReviewedBy = "subj"
		f.approvals[id] = a
		writeJSONForTest(w, http.StatusOK, a)
	})

	t.Cleanup(srv.Close)
	return srv
}

func (f *approvalsFakeAPI) seedApproval(t *testing.T, a apiclient.PendingApproval) {
	t.Helper()
	if a.Status == "" {
		a.Status = "pending"
	}
	if a.CreatedAt.IsZero() {
		a.CreatedAt = time.Now().UTC()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.approvals == nil {
		f.approvals = map[string]apiclient.PendingApproval{}
	}
	f.approvals[a.ID] = a
}

func newApprovalsMux(t *testing.T, srv *httptest.Server, mgr *session.Manager) http.Handler {
	t.Helper()
	cli, err := apiclient.NewSessionAware(srv.URL, 5*time.Second)
	if err != nil {
		t.Fatalf("apiclient.NewSessionAware: %v", err)
	}
	h := handlers.NewDashboardHandler(cli, mgr, handlers.StaticBearerSource(),
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	inner := http.NewServeMux()
	h.Register(inner)
	return handlers.RequireSession(mgr)(inner)
}

// domainActivityReferer is the Activity-view URL a reviewer grants/denies
// from; the POST handler returns them to this page (approvalsRedirectTarget).
const domainActivityReferer = "http://localhost/dashboard/domains/duplicate-payment/evidence"
const domainActivityPath = "/dashboard/domains/duplicate-payment/evidence"

// TestApprovals_AnonymousRedirectsToSignIn confirms the retired GET
// route still sits behind the dashboard's auth boundary: an anonymous
// request bounces to sign-in before the redirect-to-Activity runs.
func TestApprovals_AnonymousRedirectsToSignIn(t *testing.T) {
	t.Parallel()
	api := &approvalsFakeAPI{}
	srv := api.start(t)
	mgr := newOnboardingTestSessionManager(t)
	gated := newApprovalsMux(t, srv, mgr)

	req := httptest.NewRequest(http.MethodGet, "/dashboard/approvals", nil)
	rec := httptest.NewRecorder()
	gated.ServeHTTP(rec, req)
	if rec.Code != http.StatusFound {
		t.Fatalf("status: got %d, want 302", rec.Code)
	}
}

// TestApprovals_RetiredRouteRedirectsToDomainActivity confirms a
// signed-in GET to the retired /dashboard/approvals route lands on the
// domain Activity view (single-domain tenant), where the queue now lives.
func TestApprovals_RetiredRouteRedirectsToDomainActivity(t *testing.T) {
	t.Parallel()
	srv := (&domainsFakeAPI{hasDomain: true}).start(t)
	mgr := newOnboardingTestSessionManager(t)
	gated := newDomainsMux(t, srv, mgr)

	req := httptest.NewRequest(http.MethodGet, "/dashboard/approvals", nil)
	req.AddCookie(issueOnboardingSession(t, mgr))
	rec := httptest.NewRecorder()
	gated.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status: got %d, want 303; body=%s", rec.Code, rec.Body.String())
	}
	if loc := rec.Result().Header.Get("Location"); loc != domainActivityPath {
		t.Errorf("redirect: got %q, want %q", loc, domainActivityPath)
	}
}

// TestApprovals_GrantTransitionsApprovalAndReturnsToActivity covers the
// grant flow: POST grant with a Referer on the domain Activity view
// transitions the approval to granted (api side) and returns the
// reviewer to that Activity page.
func TestApprovals_GrantTransitionsApprovalAndReturnsToActivity(t *testing.T) {
	t.Parallel()
	api := &approvalsFakeAPI{}
	api.seedApproval(t, apiclient.PendingApproval{
		ID:         "appr-grant",
		EntityID:   "order-1",
		EntityType: "Order",
		ActionName: "ISSUE_REFUND",
	})
	srv := api.start(t)
	mgr := newOnboardingTestSessionManager(t)
	cookie := issueOnboardingSession(t, mgr)
	gated := newApprovalsMux(t, srv, mgr)

	form := url.Values{"reason": {"approved by reviewer"}}
	req := httptest.NewRequest(http.MethodPost, "/dashboard/approvals/appr-grant/grant",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Referer", domainActivityReferer)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	gated.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status: got %d, want 303", rec.Code)
	}
	if loc := rec.Result().Header.Get("Location"); loc != domainActivityPath {
		t.Fatalf("redirect: got %q, want %q", loc, domainActivityPath)
	}
	api.mu.Lock()
	got := api.approvals["appr-grant"].Status
	api.mu.Unlock()
	if got != "granted" {
		t.Errorf("approval status: got %q, want granted", got)
	}
}

// TestApprovals_DenyTransitionsApprovalAndReturnsToActivity mirrors the
// grant test for the deny path.
func TestApprovals_DenyTransitionsApprovalAndReturnsToActivity(t *testing.T) {
	t.Parallel()
	api := &approvalsFakeAPI{}
	api.seedApproval(t, apiclient.PendingApproval{
		ID:         "appr-deny",
		EntityID:   "order-1",
		EntityType: "Order",
		ActionName: "ISSUE_REFUND",
	})
	srv := api.start(t)
	mgr := newOnboardingTestSessionManager(t)
	cookie := issueOnboardingSession(t, mgr)
	gated := newApprovalsMux(t, srv, mgr)

	form := url.Values{"reason": {"too risky"}}
	req := httptest.NewRequest(http.MethodPost, "/dashboard/approvals/appr-deny/deny",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Referer", domainActivityReferer)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	gated.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status: got %d, want 303", rec.Code)
	}
	if loc := rec.Result().Header.Get("Location"); loc != domainActivityPath {
		t.Fatalf("redirect: got %q, want %q", loc, domainActivityPath)
	}
	api.mu.Lock()
	got := api.approvals["appr-deny"].Status
	api.mu.Unlock()
	if got != "denied" {
		t.Errorf("approval status: got %q, want denied", got)
	}
}

// TestApprovals_GrantFailureRedirectsBack confirms the friendly error
// path: the api fake returns 500; the handler flashes the error and
// returns the reviewer to the Activity view rather than 5xx-ing.
func TestApprovals_GrantFailureRedirectsBack(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			http.Error(w, "missing bearer", http.StatusUnauthorized)
			return
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/me":
			writeJSONForTest(w, http.StatusOK, map[string]any{
				"subject": "alice", "tenant_id": "ten-test",
				"tenant_slug": "alice-team",
			})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/grant"):
			http.Error(w, "boom", http.StatusInternalServerError)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	mgr := newOnboardingTestSessionManager(t)
	cookie := issueOnboardingSession(t, mgr)
	cli, _ := apiclient.NewSessionAware(srv.URL, 5*time.Second)
	h := handlers.NewDashboardHandler(cli, mgr, handlers.StaticBearerSource(),
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	inner := http.NewServeMux()
	h.Register(inner)
	gated := handlers.RequireSession(mgr)(inner)

	req := httptest.NewRequest(http.MethodPost, "/dashboard/approvals/appr-x/grant", nil)
	req.Header.Set("Referer", domainActivityReferer)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	gated.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status: got %d, want 303", rec.Code)
	}
	if loc := rec.Result().Header.Get("Location"); loc != domainActivityPath {
		t.Fatalf("redirect: got %q, want %q", loc, domainActivityPath)
	}
}
