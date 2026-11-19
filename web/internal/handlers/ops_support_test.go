package handlers_test

import (
	"encoding/json"
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

// opsSupportFakeAPI serves /v1/me plus the operator support routes the
// /ops/support console consumes. It records reply/status calls so the
// tests can assert the operator's action reached the API.
type opsSupportFakeAPI struct {
	mu          sync.Mutex
	replies     map[string][]string // ticketID -> reply bodies
	statuses    map[string]string   // ticketID -> last status set
	seededEmpty bool
	now         time.Time
}

func (f *opsSupportFakeAPI) start(t *testing.T) *httptest.Server {
	t.Helper()
	if f.now.IsZero() {
		f.now = time.Date(2026, 6, 2, 9, 30, 0, 0, time.UTC)
	}
	f.replies = map[string][]string{}
	f.statuses = map[string]string{}

	mux := http.NewServeMux()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			http.Error(w, "missing bearer", http.StatusUnauthorized)
			return
		}
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)

	ticket := func() map[string]any {
		return map[string]any{
			"id": "tkt-1", "tenant_id": "ten-abc", "author_subject": "user-a",
			"author_email": "user@example.com", "title": "Cannot deploy",
			"body": "the deploy spins forever", "category": "bug", "status": "open",
			"created_at": f.now.Add(-2 * time.Hour).Format(time.RFC3339),
			"updated_at": f.now.Add(-time.Hour).Format(time.RFC3339),
		}
	}

	mux.HandleFunc("GET /v1/ops/support", func(w http.ResponseWriter, r *http.Request) {
		out := []map[string]any{}
		if !f.seededEmpty {
			// Honor the status filter so the filter test is meaningful.
			if s := r.URL.Query().Get("status"); s == "" || s == "open" {
				out = append(out, ticket())
			}
		}
		writeJSONForTest(w, http.StatusOK, map[string]any{"tickets": out})
	})

	mux.HandleFunc("GET /v1/ops/support/{id}", func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("id") != "tkt-1" {
			writeJSONForTest(w, http.StatusNotFound, map[string]any{"error": "ticket not found"})
			return
		}
		writeJSONForTest(w, http.StatusOK, map[string]any{
			"ticket": ticket(),
			"messages": []map[string]any{{
				"id": "m1", "ticket_id": "tkt-1", "author_kind": "user",
				"author_subject": "user-a", "body": "still stuck",
				"created_at": f.now.Add(-90 * time.Minute).Format(time.RFC3339),
			}},
		})
	})

	mux.HandleFunc("POST /v1/ops/support/{id}/reply", func(w http.ResponseWriter, r *http.Request) {
		var req map[string]string
		_ = json.NewDecoder(r.Body).Decode(&req)
		f.mu.Lock()
		f.replies[r.PathValue("id")] = append(f.replies[r.PathValue("id")], req["body"])
		f.mu.Unlock()
		writeJSONForTest(w, http.StatusCreated, map[string]any{"id": "m-new"})
	})

	mux.HandleFunc("POST /v1/ops/support/{id}/status", func(w http.ResponseWriter, r *http.Request) {
		var req map[string]string
		_ = json.NewDecoder(r.Body).Decode(&req)
		f.mu.Lock()
		f.statuses[r.PathValue("id")] = req["status"]
		f.mu.Unlock()
		writeJSONForTest(w, http.StatusOK, map[string]any{"id": r.PathValue("id"), "status": req["status"]})
	})

	return srv
}

// newOpsSupportMux wires the ops handler behind RequireSession +
// RequireOperator (operator = "alice", the subject issueOnboardingSession
// uses). The gated handler serves both GET and POST directly, so the
// tests exercise the handler logic; the outer-router POST mount is
// covered by the smoke run.
func newOpsSupportMux(t *testing.T, srv *httptest.Server, mgr *session.Manager) http.Handler {
	t.Helper()
	cli, err := apiclient.NewSessionAware(srv.URL, 5*time.Second)
	if err != nil {
		t.Fatalf("apiclient.NewSessionAware: %v", err)
	}
	h := handlers.NewOpsHandler(cli, mgr, handlers.StaticBearerSource(),
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	inner := http.NewServeMux()
	h.Register(inner)
	return handlers.RequireSession(mgr)(handlers.RequireOperator([]string{"alice"})(inner))
}

func opsGet(t *testing.T, gated http.Handler, mgr *session.Manager, path string) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.AddCookie(issueOnboardingSession(t, mgr))
	rec := httptest.NewRecorder()
	gated.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s: status %d, want 200; body=%s", path, rec.Code, rec.Body.String())
	}
	return rec.Body.String()
}

func opsPost(t *testing.T, gated http.Handler, mgr *session.Manager, path string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(issueOnboardingSession(t, mgr))
	rec := httptest.NewRecorder()
	gated.ServeHTTP(rec, req)
	return rec
}

func TestOpsSupport_ListRenders(t *testing.T) {
	t.Parallel()
	srv := (&opsSupportFakeAPI{}).start(t)
	mgr := newOnboardingTestSessionManager(t)
	body := opsGet(t, newOpsSupportMux(t, srv, mgr), mgr, "/ops/support")
	for _, want := range []string{"Support", "Cannot deploy", "ten-abc", "/ops/support/tkt-1"} {
		if !strings.Contains(body, want) {
			t.Fatalf("ops support list missing %q\n---\n%s", want, body)
		}
	}
}

func TestOpsSupport_StatusFilter(t *testing.T) {
	t.Parallel()
	srv := (&opsSupportFakeAPI{}).start(t)
	mgr := newOnboardingTestSessionManager(t)
	// answered filter yields nothing from the fake (only an open ticket).
	body := opsGet(t, newOpsSupportMux(t, srv, mgr), mgr, "/ops/support?status=answered")
	if !strings.Contains(body, "No tickets match") {
		t.Fatalf("answered filter should be empty\n---\n%s", body)
	}
}

func TestOpsSupport_DetailRenders(t *testing.T) {
	t.Parallel()
	srv := (&opsSupportFakeAPI{}).start(t)
	mgr := newOnboardingTestSessionManager(t)
	body := opsGet(t, newOpsSupportMux(t, srv, mgr), mgr, "/ops/support/tkt-1")
	for _, want := range []string{
		"Cannot deploy", "the deploy spins forever", "still stuck",
		"/ops/support/tkt-1/reply", "/ops/support/tkt-1/status", "Send reply", "Update status",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("ops support detail missing %q\n---\n%s", want, body)
		}
	}
}

func TestOpsSupport_UnknownTicket404(t *testing.T) {
	t.Parallel()
	srv := (&opsSupportFakeAPI{}).start(t)
	mgr := newOnboardingTestSessionManager(t)
	gated := newOpsSupportMux(t, srv, mgr)
	req := httptest.NewRequest(http.MethodGet, "/ops/support/nope", nil)
	req.AddCookie(issueOnboardingSession(t, mgr))
	rec := httptest.NewRecorder()
	gated.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown ticket: status %d, want 404", rec.Code)
	}
}

func TestOpsSupport_ReplyReachesAPIAndRedirects(t *testing.T) {
	t.Parallel()
	fake := &opsSupportFakeAPI{}
	srv := fake.start(t)
	mgr := newOnboardingTestSessionManager(t)
	gated := newOpsSupportMux(t, srv, mgr)
	rec := opsPost(t, gated, mgr, "/ops/support/tkt-1/reply", url.Values{"body": {"clear the build cache"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("reply status = %d, want 303; body=%s", rec.Code, rec.Body.String())
	}
	if loc := rec.Header().Get("Location"); loc != "/ops/support/tkt-1" {
		t.Errorf("redirect = %q, want /ops/support/tkt-1", loc)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if got := fake.replies["tkt-1"]; len(got) != 1 || got[0] != "clear the build cache" {
		t.Fatalf("reply not forwarded to API: %v", got)
	}
}

func TestOpsSupport_EmptyReplyDoesNotCallAPI(t *testing.T) {
	t.Parallel()
	fake := &opsSupportFakeAPI{}
	srv := fake.start(t)
	mgr := newOnboardingTestSessionManager(t)
	gated := newOpsSupportMux(t, srv, mgr)
	rec := opsPost(t, gated, mgr, "/ops/support/tkt-1/reply", url.Values{"body": {"   "}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("empty reply status = %d, want 303", rec.Code)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.replies["tkt-1"]) != 0 {
		t.Fatalf("empty reply must not reach the API, got %v", fake.replies["tkt-1"])
	}
}

func TestOpsSupport_StatusReachesAPIAndRedirects(t *testing.T) {
	t.Parallel()
	fake := &opsSupportFakeAPI{}
	srv := fake.start(t)
	mgr := newOnboardingTestSessionManager(t)
	gated := newOpsSupportMux(t, srv, mgr)
	rec := opsPost(t, gated, mgr, "/ops/support/tkt-1/status", url.Values{"status": {"closed"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status POST = %d, want 303; body=%s", rec.Code, rec.Body.String())
	}
	if loc := rec.Header().Get("Location"); loc != "/ops/support/tkt-1" {
		t.Errorf("redirect = %q, want /ops/support/tkt-1", loc)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.statuses["tkt-1"] != "closed" {
		t.Fatalf("status not forwarded: %q", fake.statuses["tkt-1"])
	}
}

func TestOpsSupport_NonOperatorGets404(t *testing.T) {
	t.Parallel()
	srv := (&opsSupportFakeAPI{}).start(t)
	mgr := newOnboardingTestSessionManager(t)
	// Operator allow-list that does NOT include "alice" (the session
	// subject) → RequireOperator returns 404.
	cli, _ := apiclient.NewSessionAware(srv.URL, 5*time.Second)
	h := handlers.NewOpsHandler(cli, mgr, handlers.StaticBearerSource(),
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	inner := http.NewServeMux()
	h.Register(inner)
	gated := handlers.RequireSession(mgr)(handlers.RequireOperator([]string{"someone-else"})(inner))

	req := httptest.NewRequest(http.MethodGet, "/ops/support", nil)
	req.AddCookie(issueOnboardingSession(t, mgr))
	rec := httptest.NewRecorder()
	gated.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("non-operator: status %d, want 404", rec.Code)
	}
}
