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

// supportFakeAPI serves /v1/me plus the tenant support routes the
// dashboard Support surface consumes. It keeps an in-memory ticket so
// create → list → detail → reply can be exercised end-to-end.
type supportFakeAPI struct {
	mu        sync.Mutex
	tickets   map[string]map[string]any // id -> ticket
	messages  map[string][]map[string]any
	seedEmpty bool
	now       time.Time
}

func (f *supportFakeAPI) start(t *testing.T) *httptest.Server {
	t.Helper()
	if f.now.IsZero() {
		f.now = time.Date(2026, 6, 2, 9, 30, 0, 0, time.UTC)
	}
	if f.tickets == nil {
		f.tickets = map[string]map[string]any{}
	}
	if f.messages == nil {
		f.messages = map[string][]map[string]any{}
	}
	if !f.seedEmpty {
		f.tickets["tkt-1"] = map[string]any{
			"id": "tkt-1", "author_subject": "alice", "title": "Cannot deploy",
			"body": "the deploy button spins forever", "category": "bug", "status": "answered",
			"created_at": f.now.Add(-2 * time.Hour).Format(time.RFC3339),
			"updated_at": f.now.Add(-1 * time.Hour).Format(time.RFC3339),
		}
		f.messages["tkt-1"] = []map[string]any{{
			"id": "msg-1", "ticket_id": "tkt-1", "author_kind": "operator",
			"author_subject": "ops-1", "body": "Try clearing the build cache.",
			"created_at": f.now.Add(-90 * time.Minute).Format(time.RFC3339),
		}}
	}

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
			"tenant_id": "ten-test", "tenant_slug": "alice-team",
			"tenant_display_name": "Alice's Team", "api_key": true,
		})
	})

	mux.HandleFunc("GET /v1/me/support/tickets", func(w http.ResponseWriter, _ *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		out := []map[string]any{}
		for _, tk := range f.tickets {
			out = append(out, tk)
		}
		writeJSONForTest(w, http.StatusOK, map[string]any{"tickets": out})
	})

	mux.HandleFunc("POST /v1/me/support/tickets", func(w http.ResponseWriter, r *http.Request) {
		var req map[string]string
		_ = json.NewDecoder(r.Body).Decode(&req)
		if strings.TrimSpace(req["title"]) == "" || strings.TrimSpace(req["body"]) == "" {
			writeJSONForTest(w, http.StatusBadRequest, map[string]any{"error": "title and body are required"})
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		id := "tkt-new"
		tk := map[string]any{
			"id": id, "author_subject": "alice", "title": req["title"],
			"body": req["body"], "category": req["category"], "status": "open",
			"created_at": f.now.Format(time.RFC3339), "updated_at": f.now.Format(time.RFC3339),
		}
		f.tickets[id] = tk
		writeJSONForTest(w, http.StatusCreated, tk)
	})

	mux.HandleFunc("GET /v1/me/support/tickets/{id}", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		tk, ok := f.tickets[r.PathValue("id")]
		if !ok {
			writeJSONForTest(w, http.StatusNotFound, map[string]any{"error": "ticket not found"})
			return
		}
		writeJSONForTest(w, http.StatusOK, map[string]any{
			"ticket": tk, "messages": f.messages[r.PathValue("id")],
		})
	})

	mux.HandleFunc("POST /v1/me/support/tickets/{id}/reply", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		id := r.PathValue("id")
		if _, ok := f.tickets[id]; !ok {
			writeJSONForTest(w, http.StatusNotFound, map[string]any{"error": "ticket not found"})
			return
		}
		var req map[string]string
		_ = json.NewDecoder(r.Body).Decode(&req)
		if strings.TrimSpace(req["body"]) == "" {
			writeJSONForTest(w, http.StatusBadRequest, map[string]any{"error": "body is required"})
			return
		}
		msg := map[string]any{
			"id": "msg-new", "ticket_id": id, "author_kind": "user",
			"author_subject": "alice", "body": req["body"],
			"created_at": f.now.Format(time.RFC3339),
		}
		f.messages[id] = append(f.messages[id], msg)
		writeJSONForTest(w, http.StatusCreated, msg)
	})

	return srv
}

func newSupportMux(t *testing.T, srv *httptest.Server, mgr *session.Manager) http.Handler {
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

func getSupport(t *testing.T, gated http.Handler, mgr *session.Manager, path string) string {
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

func postSupport(t *testing.T, gated http.Handler, mgr *session.Manager, path string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(issueOnboardingSession(t, mgr))
	rec := httptest.NewRecorder()
	gated.ServeHTTP(rec, req)
	return rec
}

func TestDashboardSupport_EmptyState(t *testing.T) {
	t.Parallel()
	srv := (&supportFakeAPI{seedEmpty: true}).start(t)
	mgr := newOnboardingTestSessionManager(t)
	body := getSupport(t, newSupportMux(t, srv, mgr), mgr, "/dashboard/support")
	for _, want := range []string{"Support", "No tickets yet", "New ticket"} {
		if !strings.Contains(body, want) {
			t.Fatalf("empty support page missing %q\n---\n%s", want, body)
		}
	}
}

func TestDashboardSupport_ListsTickets(t *testing.T) {
	t.Parallel()
	srv := (&supportFakeAPI{}).start(t)
	mgr := newOnboardingTestSessionManager(t)
	body := getSupport(t, newSupportMux(t, srv, mgr), mgr, "/dashboard/support")
	for _, want := range []string{"Cannot deploy", "Answered", "/dashboard/support/tkt-1"} {
		if !strings.Contains(body, want) {
			t.Fatalf("support list missing %q\n---\n%s", want, body)
		}
	}
}

func TestDashboardSupport_DetailShowsThreadAndReplyForm(t *testing.T) {
	t.Parallel()
	srv := (&supportFakeAPI{}).start(t)
	mgr := newOnboardingTestSessionManager(t)
	body := getSupport(t, newSupportMux(t, srv, mgr), mgr, "/dashboard/support/tkt-1")
	for _, want := range []string{
		"Cannot deploy",                   // title
		"the deploy button spins forever", // opening body
		"KIFF Support",                    // operator author label
		"Try clearing the build cache.",   // operator reply
		"/dashboard/support/tkt-1/reply",  // reply form action
		"Send reply",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("ticket detail missing %q\n---\n%s", want, body)
		}
	}
}

func TestDashboardSupport_UnknownTicketRedirects(t *testing.T) {
	t.Parallel()
	srv := (&supportFakeAPI{}).start(t)
	mgr := newOnboardingTestSessionManager(t)
	gated := newSupportMux(t, srv, mgr)
	req := httptest.NewRequest(http.MethodGet, "/dashboard/support/nope", nil)
	req.AddCookie(issueOnboardingSession(t, mgr))
	rec := httptest.NewRecorder()
	gated.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("unknown ticket: status %d, want 303; body=%s", rec.Code, rec.Body.String())
	}
	if loc := rec.Header().Get("Location"); loc != "/dashboard/support" {
		t.Errorf("redirect = %q, want /dashboard/support", loc)
	}
}

func TestDashboardSupport_NewTicketForm(t *testing.T) {
	t.Parallel()
	srv := (&supportFakeAPI{}).start(t)
	mgr := newOnboardingTestSessionManager(t)
	body := getSupport(t, newSupportMux(t, srv, mgr), mgr, "/dashboard/support/new")
	for _, want := range []string{"New ticket", "Subject", "Details", "File ticket"} {
		if !strings.Contains(body, want) {
			t.Fatalf("new-ticket form missing %q\n---\n%s", want, body)
		}
	}
}

func TestDashboardSupport_CreateRedirectsToTicket(t *testing.T) {
	t.Parallel()
	srv := (&supportFakeAPI{}).start(t)
	mgr := newOnboardingTestSessionManager(t)
	gated := newSupportMux(t, srv, mgr)
	form := url.Values{"title": {"Billing question"}, "category": {"billing"}, "body": {"how is metering computed?"}}
	rec := postSupport(t, gated, mgr, "/dashboard/support", form)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("create status = %d, want 303; body=%s", rec.Code, rec.Body.String())
	}
	if loc := rec.Header().Get("Location"); loc != "/dashboard/support/tkt-new" {
		t.Errorf("redirect = %q, want /dashboard/support/tkt-new", loc)
	}
}

func TestDashboardSupport_CreateMissingFieldsRerenders(t *testing.T) {
	t.Parallel()
	srv := (&supportFakeAPI{}).start(t)
	mgr := newOnboardingTestSessionManager(t)
	gated := newSupportMux(t, srv, mgr)
	form := url.Values{"title": {"  "}, "body": {"  "}}
	rec := postSupport(t, gated, mgr, "/dashboard/support", form)
	if rec.Code != http.StatusOK {
		t.Fatalf("missing-fields status = %d, want 200 (re-render); body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Add a subject and some details") {
		t.Fatalf("re-render missing validation message\n---\n%s", rec.Body.String())
	}
}

func TestDashboardSupport_ReplyRedirectsBack(t *testing.T) {
	t.Parallel()
	srv := (&supportFakeAPI{}).start(t)
	mgr := newOnboardingTestSessionManager(t)
	gated := newSupportMux(t, srv, mgr)
	form := url.Values{"body": {"still not working after that"}}
	rec := postSupport(t, gated, mgr, "/dashboard/support/tkt-1/reply", form)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("reply status = %d, want 303; body=%s", rec.Code, rec.Body.String())
	}
	if loc := rec.Header().Get("Location"); loc != "/dashboard/support/tkt-1" {
		t.Errorf("redirect = %q, want /dashboard/support/tkt-1", loc)
	}
}

func TestDashboardSupport_ReplyEmptyRedirectsWithError(t *testing.T) {
	t.Parallel()
	srv := (&supportFakeAPI{}).start(t)
	mgr := newOnboardingTestSessionManager(t)
	gated := newSupportMux(t, srv, mgr)
	form := url.Values{"body": {"   "}}
	rec := postSupport(t, gated, mgr, "/dashboard/support/tkt-1/reply", form)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("empty reply status = %d, want 303; body=%s", rec.Code, rec.Body.String())
	}
	if loc := rec.Header().Get("Location"); loc != "/dashboard/support/tkt-1" {
		t.Errorf("redirect = %q, want /dashboard/support/tkt-1", loc)
	}
}
