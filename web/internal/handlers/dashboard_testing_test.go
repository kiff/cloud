package handlers_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kiff/cloud/web/internal/apiclient"
	"github.com/kiff/cloud/web/internal/session"
)

// dashboard_testing_test.go holds shared test helpers used
// across the dashboard / settings / studio handler test files.
// Originally lived in onboarding_test.go; relocated here when
// the Onboarding tab retired in so studio_test.go and the new
// settings tests can keep using them.
//
// The function names retain the "Onboarding" prefix from their
// original location to avoid renaming churn across the test
// suite — they don't actually have anything to do with the
// retired Onboarding handler. Treat them as generic
// dashboard-test fixtures.

// writeJSONForTest is the standard JSON response helper for
// fake apps/api stand-ins.
func writeJSONForTest(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if body == nil {
		return
	}
	_ = json.NewEncoder(w).Encode(body)
}

// newOnboardingTestSessionManager returns a session.Manager
// seeded with a stable test secret so cookies sign and verify
// identically across calls in the same test binary.
func newOnboardingTestSessionManager(t *testing.T) *session.Manager {
	t.Helper()
	m, err := session.NewManager([]byte("a-test-secret-of-at-least-thirty-two-bytes"))
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	return m
}

// newOnboardingTestClient builds a session-aware apiclient
// pointing at the given test server URL.
func newOnboardingTestClient(t *testing.T, baseURL string) *apiclient.Client {
	t.Helper()
	c, err := apiclient.NewSessionAware(baseURL, 5*time.Second)
	if err != nil {
		t.Fatalf("NewSessionAware: %v", err)
	}
	return c
}

// issueOnboardingSession issues a signed session for "alice"
// and returns the resulting cookie. Tests forward the cookie
// on subsequent requests through the gated mux.
func issueOnboardingSession(t *testing.T, mgr *session.Manager) *http.Cookie {
	t.Helper()
	rec := httptest.NewRecorder()
	if err := mgr.Issue(rec, session.Session{
		Subject:     "alice",
		Bearer:      "alice",
		Email:       "alice@example.com",
		DisplayName: "Alice",
		ExpiresAt:   time.Now().UTC().Add(time.Hour),
	}); err != nil {
		t.Fatalf("Issue session: %v", err)
	}
	cs := rec.Result().Cookies()
	if len(cs) == 0 {
		t.Fatalf("Issue session: no cookie")
	}
	return cs[0]
}
