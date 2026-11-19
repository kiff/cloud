package handlers_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kiff/cloud/web/internal/handlers"
	"github.com/kiff/cloud/web/internal/session"
)

// A signed-in person opening app.kiff.dev/ lands on their Cards, not the
// marketing page (onboarding test, 2026-10-08). Signed out, on the apex,
// or without an app host configured, / stays the landing page.
func TestAppHomeSendsASignedInPersonToCards(t *testing.T) {
	t.Parallel()
	mgr := newTestSessionManager(t)
	landing := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusTeapot) })
	issue := httptest.NewRecorder()
	if err := mgr.Issue(issue, session.Session{
		Subject: "alice", Bearer: "b", SessionID: "sess_1", Email: "alice@example.com",
		ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatalf("Issue: %v", err)
	}
	cookie := issue.Result().Cookies()[0]

	for _, c := range []struct {
		name, appHost, host, path string
		signedIn                  bool
		wantCode                  int
	}{
		{"signed in on the app host", "app.kiff.dev", "app.kiff.dev", "/", true, http.StatusFound},
		{"signed out on the app host", "app.kiff.dev", "app.kiff.dev", "/", false, http.StatusTeapot},
		{"signed in on the marketing apex", "app.kiff.dev", "kiff.dev", "/", true, http.StatusTeapot},
		{"another path", "app.kiff.dev", "app.kiff.dev", "/nope", true, http.StatusTeapot},
		{"no app host configured", "", "localhost", "/", true, http.StatusTeapot},
	} {
		req := httptest.NewRequest(http.MethodGet, "http://"+c.host+c.path, nil)
		if c.signedIn {
			req.AddCookie(cookie)
		}
		rec := httptest.NewRecorder()
		handlers.AppHome(c.appHost, mgr, landing).ServeHTTP(rec, req)
		if rec.Code != c.wantCode {
			t.Errorf("%s: status %d, want %d", c.name, rec.Code, c.wantCode)
		}
		if c.wantCode == http.StatusFound && rec.Header().Get("Location") != "/cards" {
			t.Errorf("%s: Location %q, want /cards", c.name, rec.Header().Get("Location"))
		}
	}
}
