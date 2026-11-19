package handlers_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/web/internal/apiclient"
	"github.com/kiff/cloud/web/internal/handlers"
	"github.com/kiff/cloud/web/internal/session"
)

func TestCardApproverSetupRevealsOnceAndRevokes(t *testing.T) {
	var created apiclient.CreateApproverRequest
	revoked := false
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/me":
			writeJSONForTest(w, 200, map[string]any{"member_role": "admin"})
		case "/v1/me/mandates":
			writeJSONForTest(w, 200, map[string]any{"mandates": []apiclient.Mandate{{ID: "card", Subject: "worker", Domain: "billing", Active: true, OnExceed: "owner", Grants: []apiclient.MandateGrant{{Action: "refund", PerActionMax: []apiclient.MandatePerAction{{Quantity: "sum(amount)", Max: 50}}}}}}})
		case "/v1/me/approver-grants":
			if r.Method == "POST" {
				if err := json.NewDecoder(r.Body).Decode(&created); err != nil {
					t.Error(err)
				}
				writeJSONForTest(w, 201, map[string]any{"grant": map[string]any{"id": "apg_fixture", "name": created.Name}, "credential": "kiff_approver_SYNTHETIC_ONLY"})
			} else {
				writeJSONForTest(w, 200, map[string]any{"grants": []apiclient.ApproverGrant{{ID: "apg_fixture", Name: "Reviewer", Worker: "worker", Action: "refund", Parameter: "amount", Max: 100, ExpiresAt: time.Now().Add(time.Hour), ConnectionStatus: "awaiting_connection"}}})
			}
		case "/v1/me/approver-grants/apg_fixture/revoke":
			revoked = true
			writeJSONForTest(w, 200, map[string]bool{"revoked": true})
		default:
			writeJSONForTest(w, 200, map[string]any{})
		}
	}))
	defer api.Close()
	client, _ := apiclient.NewSessionAware(api.URL, 0)
	mgr := newTestSessionManager(t)
	dash := handlers.NewDashboardHandler(client, mgr, handlers.StaticBearerSource(), nil)
	mux := http.NewServeMux()
	dash.Register(mux)
	h := handlers.RequireSession(mgr)(mux)
	rr := httptest.NewRecorder()
	mgr.Issue(rr, session.Session{Subject: "owner", Bearer: "person-bearer", ExpiresAt: time.Now().Add(time.Hour)})
	cookie := rr.Result().Cookies()[0]
	form := url.Values{"name": {"Reviewer"}, "worker": {"worker"}, "action": {"refund"}, "parameter": {"amount"}, "max": {"100"}, "scale": {"0"}, "unit": {""}, "expires_at": {time.Now().UTC().Add(time.Hour).Format("2006-01-02T15:04")}}
	post := httptest.NewRequest("POST", "/cards/card/approvers", strings.NewReader(form.Encode()))
	post.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	post.AddCookie(cookie)
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, post)
	if rr.Code != 303 || created.Max != 100 || created.CardID != "card" || created.Worker != "worker" {
		t.Fatalf("create %d %+v", rr.Code, created)
	}
	for _, c := range rr.Result().Cookies() {
		if strings.Contains(c.Value, "SYNTHETIC_ONLY") {
			t.Fatal("secret in cookie")
		}
	}
	get := httptest.NewRequest("GET", "/cards/card/approvers", nil)
	get.AddCookie(cookie)
	for _, c := range rr.Result().Cookies() {
		get.AddCookie(c)
	}
	page := httptest.NewRecorder()
	h.ServeHTTP(page, get)
	for _, want := range []string{"External approver credential created", "kiff_approver_SYNTHETIC_ONLY", "Awaiting connection", "approve once, reject, or leave a call for you", "/cards/card/approvers/"} {
		if !strings.Contains(page.Body.String(), want) {
			t.Errorf("page lacks %s", want)
		}
	}
	if page.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("secret reveal cacheable")
	}
	again := httptest.NewRecorder()
	h.ServeHTTP(again, get)
	if strings.Contains(again.Body.String(), "kiff_approver_SYNTHETIC_ONLY") {
		t.Fatal("secret revealed twice")
	}
	post = httptest.NewRequest("POST", "/cards/card/approvers/apg_fixture/revoke", nil)
	post.AddCookie(cookie)
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, post)
	if rr.Code != 303 || !revoked {
		t.Fatal("revoke failed")
	}
}

func TestNeedsYouExternalAnswerAndPerformedOutcome(t *testing.T) {
	api := http.NewServeMux()
	api.HandleFunc("GET /v1/me/card-exceptions/exc-1", func(w http.ResponseWriter, r *http.Request) {
		writeJSONForTest(w, 200, map[string]any{"id": "exc-1", "subject": "worker", "action": "refund", "status": "consumed", "resolved_by": "approver:fixture", "resolved_at": time.Now(), "external_answer": map[string]any{"name": "Refund reviewer", "principal": "approver:fixture", "decision": "approve", "reason": "Independent order record permits it.", "at": time.Now()}, "tool_outcome": map[string]any{"status": "done"}})
	})
	_, body := needsYouServer(t, api)("GET", "/needs-you/exc-1")
	for _, want := range []string{"Refund reviewer", "Independent order record permits it.", "The tool reported the call done"} {
		if !strings.Contains(body, want) {
			t.Errorf("page lacks %q", want)
		}
	}
}

func TestNeedsYouDeferredAnswerAndHistory(t *testing.T) {
	for _, historical := range []bool{false, true} {
		api := http.NewServeMux()
		api.HandleFunc("GET /v1/me/card-exceptions/exc-1", func(w http.ResponseWriter, r *http.Request) {
			answer := map[string]any{"name": "Refund reviewer", "principal": "approver:fixture", "decision": "defer", "reason": "Independent facts require human review.", "at": time.Now()}
			view := map[string]any{"id": "exc-1", "subject": "worker", "action": "refund", "status": "held"}
			if historical {
				view["prior_approvals"] = []any{map[string]any{"By": "approver:fixture", "At": time.Now(), "external_answer": answer}}
			} else {
				view["external_answer"] = answer
			}
			writeJSONForTest(w, 200, view)
		})
		_, body := needsYouServer(t, api)("GET", "/needs-you/exc-1")
		for _, want := range []string{"Refund reviewer", "Independent facts require human review."} {
			if !strings.Contains(body, want) {
				t.Errorf("historical=%t: page lacks %q", historical, want)
			}
		}
		if historical && (!strings.Contains(body, "Deferred by") || strings.Contains(body, "Approved by")) {
			t.Fatal("historical defer was displayed as approval")
		}
	}
}
