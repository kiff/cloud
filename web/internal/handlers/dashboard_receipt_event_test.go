package handlers_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kiff/cloud/web/internal/apiclient"
	"github.com/kiff/cloud/web/internal/handlers"
	"github.com/kiff/cloud/web/internal/session"
)

// dashboard_receipt_event_test.go covers two live-testing fixes:
//   - the gated receipt route serves a receipt through the user's
//     session (no operator KIFF_DEMO_API_KEY needed), so clicking
//     "open" in the Activity table no longer shows "Dashboard not
//     configured";
//   - the Playground "Send sample event" applies the event's own
//     template first, so sending a refund event while the cafe
//     domain is active no longer 500s with an invalid-state error.

func receiptEventSession(t *testing.T, mgr *session.Manager) *http.Cookie {
	t.Helper()
	rec := httptest.NewRecorder()
	if err := mgr.Issue(rec, session.Session{
		Subject:   "user-1",
		Bearer:    "session-bearer",
		Email:     "user@example.com",
		ExpiresAt: time.Now().UTC().Add(time.Hour),
	}); err != nil {
		t.Fatalf("Issue: %v", err)
	}
	return rec.Result().Cookies()[0]
}

// TestDashboardReceipt_ServedViaSession confirms the gated receipt
// route fetches through the session bearer and renders the receipt
// page — not the "Dashboard not configured" page.
func TestDashboardReceipt_ServedViaSession(t *testing.T) {
	t.Parallel()

	var gotAuth string
	apiMux := http.NewServeMux()
	apiMux.HandleFunc("GET /v1/receipts/{id}", func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"schema_version":"1",
			"trace_id":"trace-xyz",
			"entity_id":"order-1",
			"entity_type":"Order",
			"action_name":"MARK_PAID"
		}`))
	})
	apiSrv := httptest.NewServer(apiMux)
	t.Cleanup(apiSrv.Close)

	apiClient, err := apiclient.NewSessionAware(apiSrv.URL, 0)
	if err != nil {
		t.Fatalf("NewSessionAware: %v", err)
	}
	mgr := newTestSessionManager(t)
	dashMux := http.NewServeMux()
	handlers.NewDashboardHandler(apiClient, mgr, nil, nil).Register(dashMux)
	protected := handlers.RequireSession(mgr)(dashMux)

	req := httptest.NewRequest(http.MethodGet, "/dashboard/receipts/trace-xyz", nil)
	req.AddCookie(receiptEventSession(t, mgr))
	rec := httptest.NewRecorder()
	protected.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, "Dashboard not configured") {
		t.Fatal("receipt page rendered the not-configured page; should serve via session")
	}
	if !strings.Contains(gotAuth, "Bearer ") {
		t.Errorf("api call missing bearer auth; got %q", gotAuth)
	}
	// The receipt page renders the trace id somewhere.
	if !strings.Contains(body, "trace-xyz") {
		t.Errorf("receipt page missing the trace id")
	}
	// Signed-in receipts wear the dashboard shell (topbar + sidebar),
	// not the marketing layout, so the page reads as part of the app.
	for _, want := range []string{`class="dash-sidebar"`, `href="/dashboard/govern"`} {
		if !strings.Contains(body, want) {
			t.Errorf("receipt page not rendered in the dashboard shell; missing %q", want)
		}
	}
}

// TestDashboardReceipt_NotFoundViaSession confirms a 404 from the
// api renders the not-found page, not the error page.
func TestDashboardReceipt_NotFoundViaSession(t *testing.T) {
	t.Parallel()

	apiMux := http.NewServeMux()
	apiMux.HandleFunc("GET /v1/receipts/{id}", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	apiSrv := httptest.NewServer(apiMux)
	t.Cleanup(apiSrv.Close)

	apiClient, _ := apiclient.NewSessionAware(apiSrv.URL, 0)
	mgr := newTestSessionManager(t)
	dashMux := http.NewServeMux()
	handlers.NewDashboardHandler(apiClient, mgr, nil, nil).Register(dashMux)
	protected := handlers.RequireSession(mgr)(dashMux)

	req := httptest.NewRequest(http.MethodGet, "/dashboard/receipts/missing", nil)
	req.AddCookie(receiptEventSession(t, mgr))
	rec := httptest.NewRecorder()
	protected.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

// eventFakeAPI records the order of api calls so the test can
// assert the template is applied before the event is ingested.
type eventFakeAPI struct {
	mu    sync.Mutex
	calls []string
}

func (f *eventFakeAPI) start(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.calls = append(f.calls, r.Method+" "+r.URL.Path)
		f.mu.Unlock()
		mux.ServeHTTP(w, r)
	}))
	mux.HandleFunc("POST /v1/me/domain/template", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"domain":"refund-agno","version":1}`))
	})
	mux.HandleFunc("POST /v1/events/raw", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	t.Cleanup(srv.Close)
	return srv
}
