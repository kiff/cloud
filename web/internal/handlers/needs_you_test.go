package handlers_test

import (
	"io"
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

const heldException = `{"id":"exc-1","proposal_id":"p-1","subject":"refund-agent","action":"REFUND_ORDER","entity_id":"ord-9","status":"held",
"breaches":[{"mandate_id":"card-refunds","outcome":"per_action_exceeded","quantity":"sum(amount_cents)","limit":50000,"used":0,"draw":150000}],
"created_at":"2026-09-30T10:00:00Z"}`

// needsYouServer runs the dashboard against a fake API and returns a
// function that sends a signed-in request, following one redirect so the
// flash is rendered.
func needsYouServer(t *testing.T, api http.Handler) func(method, path string) (*http.Response, string) {
	t.Helper()
	apiSrv := httptest.NewServer(api)
	t.Cleanup(apiSrv.Close)
	client, err := apiclient.NewSessionAware(apiSrv.URL, 0)
	if err != nil {
		t.Fatal(err)
	}
	mgr := newTestSessionManager(t)
	mux := http.NewServeMux()
	handlers.NewDashboardHandler(client, mgr, nil, nil).Register(mux)
	h := handlers.RequireSession(mgr)(mux)
	issue := httptest.NewRecorder()
	_ = mgr.Issue(issue, session.Session{Subject: "alice", Bearer: "b", ExpiresAt: time.Now().Add(time.Hour)})
	cookies := issue.Result().Cookies()
	return func(method, path string) (*http.Response, string) {
		req := httptest.NewRequest(method, path, nil)
		for _, c := range cookies {
			req.AddCookie(c)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		res := rec.Result()
		if res.StatusCode == http.StatusSeeOther {
			next := httptest.NewRequest(http.MethodGet, res.Header.Get("Location"), nil)
			for _, c := range append(cookies, res.Cookies()...) {
				next.AddCookie(c)
			}
			rec2 := httptest.NewRecorder()
			h.ServeHTTP(rec2, next)
			b, _ := io.ReadAll(rec2.Result().Body)
			return res, strings.Join(strings.Fields(string(b)), " ")
		}
		b, _ := io.ReadAll(res.Body)
		return res, strings.Join(strings.Fields(string(b)), " ")
	}
}

// The page lists what is waiting, with how far outside the Card it is and
// the three answers.
func TestNeedsYouListsHeldActions(t *testing.T) {
	t.Parallel()
	api := http.NewServeMux()
	api.HandleFunc("GET /v1/me/card-exceptions", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"exceptions":[` + heldException + `]}`))
	})
	do := needsYouServer(t, api)
	res, body := do(http.MethodGet, "/needs-you")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status %d", res.StatusCode)
	}
	for _, want := range []string{"REFUND_ORDER", "card-refunds", "this Card’s", `action="/needs-you/exc-1/approve-once"`, `action="/needs-you/exc-1/change-card"`, `action="/needs-you/exc-1/reject"`} {
		if !strings.Contains(body, want) {
			t.Errorf("page lacks %q", want)
		}
	}
}

func TestNeedsYouEmptyAndUnavailableAreDistinct(t *testing.T) {
	t.Parallel()
	empty := http.NewServeMux()
	empty.HandleFunc("GET /v1/me/card-exceptions", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"exceptions":[]}`))
	})
	empty.HandleFunc("GET /v1/me/attention", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"items":[]}`))
	})
	if _, body := needsYouServer(t, empty)(http.MethodGet, "/needs-you"); !strings.Contains(body, "Nothing is waiting for you.") {
		t.Fatal("empty state missing")
	}
	broken := http.NewServeMux()
	broken.HandleFunc("GET /v1/me/card-exceptions", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", http.StatusInternalServerError)
	})
	_, body := needsYouServer(t, broken)(http.MethodGet, "/needs-you")
	if strings.Contains(body, "Nothing is waiting for you.") || !strings.Contains(body, "could not be loaded") {
		t.Fatal("an unreadable list must not look empty")
	}
}

// Each answer posts to the API and comes back to the action with what
// happened; a conflict says it was already answered.
func TestNeedsYouAnswersPostToTheAPI(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		answer, status, want string
	}{
		{"approve-once", "200", "Answer saved.</strong> Approved once."},
		{"change-card", "200", "The card now allows this"},
		{"reject", "200", "Rejected. Nothing happened"},
		{"reject", "409", "This was already answered"},
	} {
		var mu sync.Mutex
		var got string
		api := http.NewServeMux()
		api.HandleFunc("POST /v1/me/card-exceptions/{id}/{answer}", func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			got = r.URL.Path
			mu.Unlock()
			if tc.status == "409" {
				http.Error(w, `{"error":"conflict"}`, http.StatusConflict)
				return
			}
			_, _ = w.Write([]byte(heldException))
		})
		api.HandleFunc("GET /v1/me/card-exceptions/{id}", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(heldException))
		})
		res, body := needsYouServer(t, api)(http.MethodPost, "/needs-you/exc-1/"+tc.answer)
		if res.Header.Get("Location") != "/needs-you/exc-1" {
			t.Fatalf("%s: location %q", tc.answer, res.Header.Get("Location"))
		}
		mu.Lock()
		if got != "/v1/me/card-exceptions/exc-1/"+tc.answer {
			t.Errorf("%s: api got %q", tc.answer, got)
		}
		mu.Unlock()
		if !strings.Contains(body, tc.want) {
			t.Errorf("%s/%s: flash %q missing", tc.answer, tc.status, tc.want)
		}
	}
}

func TestNeedsYouUnknownExceptionIs404(t *testing.T) {
	t.Parallel()
	api := http.NewServeMux()
	api.HandleFunc("GET /v1/me/card-exceptions/{id}", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
	})
	if res, _ := needsYouServer(t, api)(http.MethodGet, "/needs-you/exc-x"); res.StatusCode != http.StatusNotFound {
		t.Fatalf("status %d", res.StatusCode)
	}
}

// Needs you re-checks its count and list while open, and the bell is
// served alone for the same re-check, so an answer or a new hold shows
// without a reload.
func TestNeedsYouAndTheBellReCheckThemselves(t *testing.T) {
	t.Parallel()
	api := http.NewServeMux()
	api.HandleFunc("GET /v1/me/card-exceptions", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"exceptions":[` + heldException + `]}`))
	})
	do := needsYouServer(t, api)
	_, body := do(http.MethodGet, "/needs-you")
	for _, want := range []string{
		`id="ny-tabs" aria-label="Needs you" data-live-src="/needs-you"`,
		`id="ny-live" class="ny-tab" data-live-src="/needs-you"`,
		`id="dash-bell" data-live-src="/dashboard/bell"`,
		`document.querySelectorAll("[data-live-src][id]")`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	// The list region holds the held action, so a re-check replaces it.
	live := body[strings.Index(body, `id="ny-live"`):]
	if !strings.Contains(live, `action="/needs-you/exc-1/approve-once"`) {
		t.Errorf("held action is outside the live region")
	}

	res, bell := do(http.MethodGet, "/dashboard/bell")
	if res.StatusCode != http.StatusOK || res.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("bell: %d %q", res.StatusCode, res.Header.Get("Cache-Control"))
	}
	if !strings.HasPrefix(bell, `<details class="dash-bell dash-autoclose" id="dash-bell"`) || strings.Contains(bell, "<html") {
		t.Errorf("bell is not the bell alone: %.200s", bell)
	}
}
