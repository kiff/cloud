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

// tabsFixture is one Card off the gateway whose limit apps/api resolved
// to EUR, with one approver, for the Card page's tabs and sheets.
func tabsFixture(t *testing.T, role string) (http.Handler, *http.Cookie, *apiclient.CreateApproverRequest, *map[string]any) {
	t.Helper()
	created := &apiclient.CreateApproverRequest{}
	moved := &map[string]any{}
	card := apiclient.Mandate{ID: "card", Subject: "worker", Domain: "billing", Active: true, OnExceed: "owner",
		Grants: []apiclient.MandateGrant{{Action: "refund", PerActionMax: []apiclient.MandatePerAction{{Quantity: "sum(amount_eur)", Max: 5000}}}}}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/me":
			writeJSONForTest(w, 200, map[string]any{"member_role": role})
		case "/v1/me/mandates":
			writeJSONForTest(w, 200, map[string]any{"mandates": []apiclient.Mandate{card}})
		case "/v1/me/mandates/card/statement":
			writeJSONForTest(w, 200, apiclient.MandateStatement{Mandate: card, Limits: []apiclient.MandateLimit{{Quantity: "sum(amount_eur)", Unit: "EUR", Scale: 2, Limit: 100000, Remaining: 64000, Window: "calendar_day", Status: "ok"}}})
		case "/v1/me/mandates/card/move":
			_ = json.NewDecoder(r.Body).Decode(moved)
			writeJSONForTest(w, 200, card)
		case "/v1/me/agents":
			writeJSONForTest(w, 200, map[string]any{"agents": []map[string]any{{"id": "worker"}, {"id": "other"}}})
		case "/v1/me/approver-grants":
			if r.Method == "POST" {
				_ = json.NewDecoder(r.Body).Decode(created)
				writeJSONForTest(w, 201, map[string]any{"grant": map[string]any{"id": "apg_new", "name": created.Name}, "credential": "kiff_approver_SYNTHETIC_ONLY"})
				return
			}
			writeJSONForTest(w, 200, map[string]any{"grants": []apiclient.ApproverGrant{{ID: "apg_1", Name: "Reviewer", Worker: "worker", Action: "refund", Parameter: "amount_eur", Max: 10000, ExpiresAt: time.Now().Add(time.Hour), ConnectionStatus: "not_responding"}}})
		default:
			writeJSONForTest(w, 200, map[string]any{})
		}
	}))
	t.Cleanup(api.Close)
	client, _ := apiclient.NewSessionAware(api.URL, 0)
	mgr := newTestSessionManager(t)
	dash := handlers.NewDashboardHandler(client, mgr, handlers.StaticBearerSource(), nil)
	mux := http.NewServeMux()
	dash.Register(mux)
	rr := httptest.NewRecorder()
	mgr.Issue(rr, session.Session{Subject: "owner", Bearer: "person-bearer", ExpiresAt: time.Now().Add(time.Hour)})
	return handlers.RequireSession(mgr)(mux), rr.Result().Cookies()[0], created, moved
}

func postTabForm(h http.Handler, c *http.Cookie, target string, form url.Values) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", target, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(c)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestCardTabsAndSheets(t *testing.T) {
	h, cookie, created, moved := tabsFixture(t, "admin")
	overview := send(h, cookie, "GET", "/cards/card").Body.String()
	for _, want := range []string{"640.00 EUR", `href="/cards/card/settings"`, `href="/cards/card/approvers"`, `href="/cards/card/decisions"`} {
		if !strings.Contains(overview, want) {
			t.Errorf("overview lacks %q", want)
		}
	}
	// Approvers are their own tab: none on the Overview.
	if strings.Contains(overview, "Reviewer") || strings.Contains(overview, "ac-approver") {
		t.Error("the Overview still shows approvers")
	}
	settings := send(h, cookie, "GET", "/cards/card/settings").Body.String()
	for _, want := range []string{"50.00 EUR", "Ask · 10 min", "card=card&amp;step=4", `href="/cards/card/holder"`, `href="/cards/card/revoke"`} {
		if !strings.Contains(settings, want) {
			t.Errorf("settings lacks %q", want)
		}
	}
	approvers := send(h, cookie, "GET", "/cards/card/approvers").Body.String()
	for _, want := range []string{"Reviewer", "ac-badge-bad", "Not responding", "100.00 EUR a call", `href="/cards/card/approvers/apg_1/revoke"`, `href="/cards/card/approvers/new"`} {
		if !strings.Contains(approvers, want) {
			t.Errorf("approvers tab lacks %q", want)
		}
	}
	if strings.Contains(approvers, "stored units") {
		t.Error("an amount reads in stored units, not the Card's EUR")
	}
	// The connect steps prefill the Card's own threshold and post one scope.
	sheet := send(h, cookie, "GET", "/cards/card/approvers/new").Body.String()
	for _, want := range []string{`value="refund|amount_eur|EUR|2"`, `value="50.00"`, "In 7 days", `name="expires_on"`} {
		if !strings.Contains(sheet, want) {
			t.Errorf("connect sheet lacks %q", want)
		}
	}
	day := time.Now().UTC().Add(48 * time.Hour).Format("2006-01-02")
	rec := postTabForm(h, cookie, "/cards/card/approvers", url.Values{"scope": {"refund|amount_eur|EUR|2"}, "worker": {"worker"}, "max": {"75.50"}, "name": {"Nightly"}, "expires_at": {""}, "expires_on": {day}})
	if rec.Code != 303 || rec.Header().Get("Location") != "/cards/card/approvers" || created.Max != 7550 || created.Action != "refund" || created.ExpiresAt.Format("2006-01-02T15:04") != day+"T23:59" {
		t.Fatalf("connect: %d %s %+v", rec.Code, rec.Header().Get("Location"), *created)
	}
	if revoke := send(h, cookie, "GET", "/cards/card/approvers/apg_1/revoke").Body.String(); !strings.Contains(revoke, "Revoke Reviewer?") || !strings.Contains(revoke, `action="/cards/card/approvers/apg_1/revoke"`) {
		t.Error("the approver revoke sheet does not confirm")
	}
	if c := send(h, cookie, "GET", "/cards/card/revoke").Body.String(); !strings.Contains(c, `action="/cards/card/revoke"`) || !strings.Contains(c, "Keep it") {
		t.Error("the Card revoke sheet does not confirm")
	}
	holder := send(h, cookie, "GET", "/cards/card/holder").Body.String()
	if !strings.Contains(holder, `type="radio" name="agent" value="other"`) || strings.Contains(holder, `type="radio" name="agent" value="worker"`) {
		t.Error("the holder sheet offers the wrong agents")
	}
	// Move, from the holder sheet, is a move.
	if rec := postTabForm(h, cookie, "/cards/card/holders", url.Values{"action": {"move"}, "agent": {"other"}}); rec.Code != 303 || (*moved)["subject"] != "other" {
		t.Fatalf("move: %d %v", rec.Code, *moved)
	}
}

// Approvers are an admin's to see and change.
func TestCardApproversAdminOnly(t *testing.T) {
	h, cookie, _, _ := tabsFixture(t, "editor")
	body := send(h, cookie, "GET", "/cards/card/approvers").Body.String()
	if !strings.Contains(body, "Only an admin") || strings.Contains(body, "Reviewer") {
		t.Error("a non-admin sees approvers")
	}
	if rec := send(h, cookie, "GET", "/cards/card/approvers/new"); rec.Code != 303 {
		t.Errorf("a non-admin opened the connect sheet: %d", rec.Code)
	}
}
