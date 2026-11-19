package handlers_test

import (
	"io"
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

const heldWithSteps = `{"id":"exc-1","proposal_id":"p-1","subject":"refund-agent","action":"REFUND_ORDER","entity_id":"ord-9","status":"held",
"breaches":[{"mandate_id":"card-refunds","outcome":"per_action_exceeded","quantity":"sum(amount_cents)","limit":50000,"used":0,"draw":150000}],
"next_steps":["return_first","proof_first"],"created_at":"2026-09-30T10:00:00Z"}`

const refusedWithStep = `{"id":"exc-1","proposal_id":"p-1","subject":"refund-agent","action":"REFUND_ORDER","entity_id":"ord-9","status":"rejected",
"breaches":[{"mandate_id":"card-refunds","outcome":"per_action_exceeded","quantity":"sum(amount_cents)","limit":50000,"used":0,"draw":150000}],
"refusal":{"next_step":"return_first","note":"Label sent."},"resolved_at":"2026-09-30T10:05:00Z","resolved_by":"alice",
"resolution":"reject","created_at":"2026-09-30T10:00:00Z"}`

// postNeedsYouForm signs in, posts a form and follows the redirect, as the owner's
// browser does.
func postNeedsYouForm(t *testing.T, api http.Handler, path string, form url.Values) (*http.Response, string) {
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
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	res := rec.Result()
	next := httptest.NewRequest(http.MethodGet, res.Header.Get("Location"), nil)
	for _, c := range append(cookies, res.Cookies()...) {
		next.AddCookie(c)
	}
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, next)
	b, _ := io.ReadAll(rec2.Result().Body)
	return res, strings.Join(strings.Fields(string(b)), " ")
}

// The Card's next steps are offered next to Reject, each one a
// refusal that tells the agent what to do instead.
func TestNeedsYouOffersTheCardsNextSteps(t *testing.T) {
	t.Parallel()
	api := http.NewServeMux()
	api.HandleFunc("GET /v1/me/card-exceptions", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"exceptions":[` + heldWithSteps + `]}`))
	})
	_, body := needsYouServer(t, api)(http.MethodGet, "/needs-you")
	for _, want := range []string{`name="next_step" value="return_first"`, ">Return first<", `name="next_step" value="proof_first"`, ">Proof first<", ">Reject<"} {
		if !strings.Contains(body, want) {
			t.Errorf("page lacks %q", want)
		}
	}
}

func TestNeedsYouRejectsWithANextStep(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var gotPath, gotBody, gotType string
	api := http.NewServeMux()
	api.HandleFunc("POST /v1/me/card-exceptions/{id}/{answer}", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		gotPath, gotBody, gotType = r.URL.Path, string(b), r.Header.Get("Content-Type")
		mu.Unlock()
		_, _ = w.Write([]byte(refusedWithStep))
	})
	api.HandleFunc("GET /v1/me/card-exceptions/{id}", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(refusedWithStep))
	})
	res, body := postNeedsYouForm(t, api, "/needs-you/exc-1/reject", url.Values{"next_step": {"return_first"}})
	if res.Header.Get("Location") != "/needs-you/exc-1" {
		t.Fatalf("location %q", res.Header.Get("Location"))
	}
	mu.Lock()
	if gotPath != "/v1/me/card-exceptions/exc-1/reject" || gotBody != `{"next_step":"return_first"}` || gotType != "application/json" {
		t.Errorf("api got %s %q (%s)", gotPath, gotBody, gotType)
	}
	mu.Unlock()
	for _, want := range []string{"Refused, with the next step: Return first. The agent is told what to do instead.",
		"rejected it and told the agent: Return first.", "The agent is told this, with the note: “Label sent.”"} {
		if !strings.Contains(body, want) {
			t.Errorf("page lacks %q", want)
		}
	}
}

// A step the API does not accept leaves the action held and says so.
func TestNeedsYouNextStepNotAccepted(t *testing.T) {
	t.Parallel()
	api := http.NewServeMux()
	api.HandleFunc("POST /v1/me/card-exceptions/{id}/{answer}", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"next_step_not_on_card"}`, http.StatusBadRequest)
	})
	api.HandleFunc("GET /v1/me/card-exceptions/{id}", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(heldWithSteps))
	})
	_, body := postNeedsYouForm(t, api, "/needs-you/exc-1/reject", url.Values{"next_step": {"refund_half"}})
	if !strings.Contains(body, "That next step is not one this Card offers") {
		t.Errorf("flash missing: %s", body)
	}
}

// A plain Reject sends no body, exactly as before.
func TestNeedsYouPlainRejectSendsNoBody(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	gotBody := "unset"
	api := http.NewServeMux()
	api.HandleFunc("POST /v1/me/card-exceptions/{id}/{answer}", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		gotBody = string(b)
		mu.Unlock()
		_, _ = w.Write([]byte(heldException))
	})
	api.HandleFunc("GET /v1/me/card-exceptions/{id}", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(heldException))
	})
	_, body := postNeedsYouForm(t, api, "/needs-you/exc-1/reject", url.Values{})
	mu.Lock()
	defer mu.Unlock()
	if gotBody != "" || !strings.Contains(body, "Rejected. Nothing happened") {
		t.Errorf("body %q, page %s", gotBody, body)
	}
}
