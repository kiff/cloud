package handlers_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/web/internal/apiclient"
	"github.com/kiff/cloud/web/internal/handlers"
	"github.com/kiff/cloud/web/internal/session"
)

func TestCardBudgetWarningsAdminJourney(t *testing.T) {
	role := "admin"
	writes := 0
	settingsReads := 0
	resolved := false
	config := apiclient.WarningConfig{Settings: apiclient.WarningSettings{Runway: true, Spent: true, Unusual: true, Burst: true, RunwayPercent: 25, UnusualMultiplier: 3, BurstPercent: 50}}
	now := time.Now().UTC()
	card := apiclient.Mandate{ID: "card", Subject: "worker", Domain: "gateway", Active: true, OnExceed: "owner", Grants: []apiclient.MandateGrant{{Action: "refund"}}}
	warning := apiclient.AttentionItem{ID: "card-warning:synthetic", Kind: "card_warning", SourceID: "card", Reason: "This Card total is spent. Review this window’s room, change the Card, or pause its agent.", CreatedAt: now, Status: "open", Revision: 1, Unread: true}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/me":
			caps := []string{"governance.manage", "cards.manage"}
			if role != "admin" {
				caps = []string{"cards.read"}
			}
			writeJSONForTest(w, 200, map[string]any{"member_role": role, "account_kind": "tools", "capabilities": caps})
		case "/v1/me/mandates":
			writeJSONForTest(w, 200, map[string]any{"mandates": []apiclient.Mandate{card}})
		case "/v1/me/mandates/card/warnings":
			if r.Method == "GET" {
				settingsReads++
			}
			if r.Method == "PUT" {
				var request struct {
					Revision int64                     `json:"revision"`
					Settings apiclient.WarningSettings `json:"settings"`
				}
				dec := json.NewDecoder(r.Body)
				dec.DisallowUnknownFields()
				if err := dec.Decode(&request); err != nil {
					t.Error(err)
					http.Error(w, "invalid request", 400)
					return
				}
				if request.Revision != config.Revision {
					http.Error(w, "stale settings", 409)
					return
				}
				writes++
				config.Revision++
				config.Settings = request.Settings
				config.At = now
				config.By = "owner"
			}
			writeJSONForTest(w, 200, config)
		case "/v1/me/mandates/card/statement":
			writeJSONForTest(w, 200, map[string]any{"mandate": card, "limits": []map[string]any{{"quantity": "count", "limit": 10, "used": 10, "gross_authorized": 10, "net_counted": 10, "remaining": 0, "status": "ok", "window": "rolling_24h", "measure": "attempts", "forecast": map[string]any{"state": "spent", "observed_at": now, "observation_seconds": 3600}}}, "warnings": []map[string]any{{"id": "synthetic", "card_id": "card", "kind": "spent", "reason": warning.Reason, "at": now, "resolved_at": func() any {
				if resolved {
					return now
				}
				return nil
			}()}}})
		case "/v1/me/attention":
			writeJSONForTest(w, 200, apiclient.AttentionFeed{Items: []apiclient.AttentionItem{warning}, Open: 1, Unread: 1})
		case "/v1/me/card-exceptions":
			writeJSONForTest(w, 200, map[string]any{"exceptions": []any{}})
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
	h := handlers.RequireSession(mgr)(dash.WithChrome(mux))
	issued := httptest.NewRecorder()
	_ = mgr.Issue(issued, session.Session{Subject: "owner", Bearer: "synthetic-person", ExpiresAt: now.Add(time.Hour)})
	cookie := issued.Result().Cookies()[0]
	get := func(path string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", path, nil)
		r.AddCookie(cookie)
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, r)
		return rr
	}
	capture := func(name string, page *httptest.ResponseRecorder) {
		if path := os.Getenv("KIFF_WARNING_UI_CAPTURE"); path != "" {
			if name != "sheet" {
				path += "-" + name + ".html"
			}
			if err := os.WriteFile(path, page.Body.Bytes(), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	page := get("/cards/card")
	if page.Code != 200 || !strings.Contains(page.Body.String(), "This total is spent") {
		t.Fatal("overview lacks active one-line warning")
	}
	for _, unwanted := range []string{"Save warnings", "Adjust thresholds", "Recent budget warnings"} {
		if strings.Contains(page.Body.String(), unwanted) {
			t.Fatalf("overview contains warning configuration/history: %s", unwanted)
		}
	}
	if settingsReads != 0 {
		t.Fatal("overview reads configuration")
	}
	capture("overview", page)
	page = get("/cards/card/settings")
	for _, want := range []string{"Budget warnings", "/cards/card/warnings", "On"} {
		if !strings.Contains(page.Body.String(), want) {
			t.Fatalf("settings lacks %q", want)
		}
	}
	if strings.Contains(page.Body.String(), "Save warnings") {
		t.Fatal("form is inline instead of a Settings row")
	}
	capture("settings", page)
	page = get("/cards/card/warnings")
	for _, want := range []string{"Save warnings", "Adjust thresholds", "data-wk-step=\"1\"", "/cards/card/settings"} {
		if !strings.Contains(page.Body.String(), want) {
			t.Fatalf("warning sheet lacks %q", want)
		}
	}
	if page.Code != 200 || strings.Count(page.Body.String(), "data-wk-step=") != 1 {
		t.Fatal("warnings are not a one-step sheet")
	}
	capture("sheet", page)
	page = get("/cards/card/statement")
	if !strings.Contains(page.Body.String(), "Recent budget warnings") {
		t.Fatal("statement lacks warning history")
	}
	resolved = true
	page = get("/cards/card")
	if strings.Contains(page.Body.String(), "This total is spent") {
		t.Fatal("resolved warning stays active on overview")
	}
	resolved = false
	form := url.Values{"revision": {"0"}, "spent": {"on"}, "unusual": {"on"}, "burst": {"on"}, "runway_percent": {"20"}, "unusual_multiplier": {"4"}, "burst_percent": {"60"}}
	post := func() *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/cards/card/warnings", strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.AddCookie(cookie)
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, r)
		return rr
	}
	response := post()
	if response.Code != 303 || response.Header().Get("Location") != "/cards/card/warnings" || writes != 1 || config.Settings.Runway || config.Settings.RunwayPercent != 20 {
		t.Fatalf("save %d writes %d config %+v", response.Code, writes, config)
	}
	// A stale second save must not overwrite the latest revision.
	response = post()
	if response.Code != 303 || writes != 1 {
		t.Fatal("stale save overwrote settings")
	}
	form.Set("runway_percent", "101")
	response = post()
	if response.Code != 303 || response.Header().Get("Location") != "/cards/card/warnings" || writes != 1 {
		t.Fatal("invalid form did not return to sheet safely")
	}
	request := httptest.NewRequest("GET", "/cards/card/warnings", nil)
	request.AddCookie(cookie)
	for _, c := range response.Result().Cookies() {
		request.AddCookie(c)
	}
	flashed := httptest.NewRecorder()
	h.ServeHTTP(flashed, request)
	if !strings.Contains(flashed.Body.String(), "Enter percentages from 1 to 100") {
		t.Fatal("validation error not shown in sheet")
	}
	// Waiting says a Card is running low in one line; the warning itself
	// is on the Warnings tab.
	waiting := get("/needs-you").Body.String()
	if i := strings.Index(waiting, `id="ny-live"`); i < 0 || !strings.Contains(waiting[i:], "1 Card is running low or used up") ||
		!strings.Contains(waiting[i:], `href="/needs-you?tab=warnings"`) || strings.Contains(waiting[i:], warning.Reason) {
		t.Fatal("Waiting tab does not point to the warning in one line")
	}
	needs := get("/needs-you?tab=warnings")
	if !strings.Contains(needs.Body.String(), `href="/cards/card"`) || !strings.Contains(needs.Body.String(), warning.Reason) || !strings.Contains(needs.Body.String(), "/cards/card/extra-room") {
		t.Fatal("warning missing from Needs you")
	}
	role = "viewer"
	if page = get("/cards/card/warnings"); page.Code != 403 {
		t.Fatalf("viewer can open admin sheet: %d", page.Code)
	}
	response = post()
	if response.Code != 403 || writes != 1 {
		t.Fatalf("viewer changed warnings: %d", response.Code)
	}
}
