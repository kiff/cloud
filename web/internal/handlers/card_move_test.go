package handlers_test

import (
	"encoding/json"
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

// The dashboard's Move posts the new holder to the API's move endpoint.
func TestMoveCardPostsToTheAPI(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var gotPath, gotSubject string
	apiMux := http.NewServeMux()
	apiMux.HandleFunc("POST /v1/me/mandates/{id}/move", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Subject string `json:"subject"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		gotPath, gotSubject = r.URL.Path, body.Subject
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	})
	apiSrv := httptest.NewServer(apiMux)
	t.Cleanup(apiSrv.Close)
	api, err := apiclient.NewSessionAware(apiSrv.URL, 0)
	if err != nil {
		t.Fatal(err)
	}
	mgr := newTestSessionManager(t)
	mux := http.NewServeMux()
	handlers.NewDashboardHandler(api, mgr, nil, nil).Register(mux)
	issue := httptest.NewRecorder()
	_ = mgr.Issue(issue, session.Session{Subject: "alice", Bearer: "b", ExpiresAt: time.Now().Add(time.Hour)})

	req := httptest.NewRequest(http.MethodPost, "/cards/card-1/move", strings.NewReader(url.Values{"subject": {"agent://b"}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(issue.Result().Cookies()[0])
	rec := httptest.NewRecorder()
	handlers.RequireSession(mgr)(mux).ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/cards/card-1" {
		t.Fatalf("status %d location %q", rec.Code, rec.Header().Get("Location"))
	}
	mu.Lock()
	defer mu.Unlock()
	if gotPath != "/v1/me/mandates/card-1/move" || gotSubject != "agent://b" {
		t.Fatalf("api got %q %q", gotPath, gotSubject)
	}
}
