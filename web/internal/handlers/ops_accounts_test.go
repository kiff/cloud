package handlers_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
)

// The console's label form reaches the API with the label and the reason,
// and goes back to the tenant.
func TestOpsLabel_ReachesAPIAndRedirects(t *testing.T) {
	t.Parallel()
	var (
		mu  sync.Mutex
		got map[string]any
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/ops/tenants/ten-abc/label" {
			http.NotFound(w, r)
			return
		}
		mu.Lock()
		_ = json.NewDecoder(r.Body).Decode(&got)
		mu.Unlock()
		writeJSONForTest(w, http.StatusOK, map[string]any{"id": "ten-abc", "label": "eval"})
	}))
	t.Cleanup(srv.Close)
	mgr := newOnboardingTestSessionManager(t)
	gated := newOpsSupportMux(t, srv, mgr)
	rec := opsPost(t, gated, mgr, "/ops/tenants/ten-abc/label", url.Values{"label": {"eval"}, "reason": {"the nightly Eval"}})
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/ops/tenants/ten-abc" {
		t.Fatalf("label: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	mu.Lock()
	defer mu.Unlock()
	if got["label"] != "eval" || got["reason"] != "the nightly Eval" {
		t.Fatalf("sent to the API: %v", got)
	}
}

// The consumption page shows the ledger's rows and total, and the account
// list shows each account's label, key prefix and runs.
func TestOpsConsumptionAndAccountList_Render(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/ops/consumption":
			writeJSONForTest(w, http.StatusOK, map[string]any{
				"from": "2026-10-08T12:00:00Z", "to": "2026-11-07T12:00:00Z",
				"rows": []map[string]any{{"day": "2026-11-06T00:00:00Z", "tenant_id": "ten-abc", "slug": "alpha", "label": "eval",
					"agent": "agt-1", "runtime_agent": "taf-refunds", "runs": 3, "counted": 2, "credits": 40, "cost_micro_usd": 1_250_000}},
				"total": map[string]any{"runs": 3, "counted": 2, "credits": 40, "cost_micro_usd": 1_250_000},
			})
		case "/v1/ops/tenants":
			writeJSONForTest(w, http.StatusOK, map[string]any{"tenants": []map[string]any{{
				"id": "ten-abc", "slug": "alpha", "status": "active", "plan": "solo", "created_at": "2026-10-01T00:00:00Z",
				"label": "eval", "billed_by": "stripe", "key_prefix": "kiff_live_ten-abc_", "runs": 3, "credits": 40,
			}}})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	mgr := newOnboardingTestSessionManager(t)
	gated := newOpsSupportMux(t, srv, mgr)
	page := opsGet(t, gated, mgr, "/ops/consumption")
	for _, want := range []string{"taf-refunds", "alpha", "eval", "$1.25", "2026-11-06"} {
		if !strings.Contains(page, want) {
			t.Errorf("consumption page lacks %q", want)
		}
	}
	list := opsGet(t, gated, mgr, "/ops/tenants?q=eval")
	for _, want := range []string{"kiff_live_ten-abc_", "stripe", "alpha"} {
		if !strings.Contains(list, want) {
			t.Errorf("account list (searched by label) lacks %q", want)
		}
	}
}
