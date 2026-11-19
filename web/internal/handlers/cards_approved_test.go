package handlers_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/web/internal/apiclient"
	"github.com/kiff/cloud/web/internal/handlers"
	"github.com/kiff/cloud/web/internal/session"
)

// The Approved once tab reads the statement and, separately, the
// answered holds. When only the second read fails, the rows stay and
// each says its outcome could not be read, with a note above the
// table: a ledger without its What happened column looked complete.
func TestApprovedOnceTabSaysWhenOutcomesCannotBeRead(t *testing.T) {
	t.Parallel()
	apiMux := http.NewServeMux()
	apiMux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/me/mandates":
			writeJSONForTest(w, http.StatusOK, map[string]any{"mandates": []map[string]any{{"id": "card-1", "subject": "agent-1"}}})
		case "/v1/me/mandates/card-1/statement":
			writeJSONForTest(w, http.StatusOK, map[string]any{
				"limits": []any{},
				"approved_outside": map[string]any{
					"entries": []map[string]any{{"exception_id": "exc-1", "proposal_id": "tc-1", "action": "refund_order", "quantity": "count", "amount": 1}},
					"totals":  map[string]any{"count": 1},
				},
			})
		case "/v1/me/card-exceptions":
			writeJSONForTest(w, http.StatusInternalServerError, map[string]any{"error": "boom"})
		default:
			writeJSONForTest(w, http.StatusNotFound, map[string]any{"error": "not found"})
		}
	})
	apiSrv := httptest.NewServer(apiMux)
	t.Cleanup(apiSrv.Close)
	apiClient, err := apiclient.NewSessionAware(apiSrv.URL, 0)
	if err != nil {
		t.Fatalf("NewSessionAware: %v", err)
	}
	mgr := newTestSessionManager(t)
	dashH := handlers.NewDashboardHandler(apiClient, mgr, &countingBearerSource{}, nil)
	mux := http.NewServeMux()
	dashH.Register(mux)
	gated := handlers.RequireSession(mgr)(mux)

	issue := httptest.NewRecorder()
	if err := mgr.Issue(issue, session.Session{
		Subject: "alice", Bearer: "stored-bearer", SessionID: "sess_1", Email: "alice@example.com",
		ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatalf("Issue: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/cards/card-1/approved", nil)
	req.AddCookie(issue.Result().Cookies()[0])
	rec := httptest.NewRecorder()
	gated.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := strings.Join(strings.Fields(rec.Body.String()), " ")
	for _, want := range []string{"/needs-you/exc-1", "What happened", "Could not be read", "What happened to each action could not be read."} {
		if !strings.Contains(body, want) {
			t.Errorf("approved tab missing %q", want)
		}
	}
}
