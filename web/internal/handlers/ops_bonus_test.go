package handlers_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
)

// The console's bonus form reaches the API with the credits as a number
// and the reason, never as tasks, and goes back to the tenant.
func TestOpsBonus_ReachesAPIAndRedirects(t *testing.T) {
	t.Parallel()
	var (
		mu  sync.Mutex
		got map[string]any
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/ops/tenants/ten-abc/bonus" {
			http.NotFound(w, r)
			return
		}
		mu.Lock()
		_ = json.NewDecoder(r.Body).Decode(&got)
		mu.Unlock()
		writeJSONForTest(w, http.StatusOK, map[string]any{"id": "ten-abc", "credits": 500})
	}))
	t.Cleanup(srv.Close)
	mgr := newOnboardingTestSessionManager(t)
	gated := newOpsSupportMux(t, srv, mgr)
	rec := opsPost(t, gated, mgr, "/ops/tenants/ten-abc/bonus", url.Values{"credits": {"500"}, "reason": {"a tester's top-up"}})
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/ops/tenants/ten-abc" {
		t.Fatalf("bonus: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	mu.Lock()
	defer mu.Unlock()
	if _, tasks := got["tasks"]; tasks || got["credits"] != float64(500) || got["reason"] != "a tester's top-up" {
		t.Fatalf("sent to the API: %v", got)
	}
}
