package handlers_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/web/internal/apiclient"
	"github.com/kiff/cloud/web/internal/components"
	"github.com/kiff/cloud/web/internal/handlers"
	"github.com/kiff/cloud/web/internal/session"
)

func TestExtraRoomPreviewConfirmationKeepsWindowAndManagementAuthority(t *testing.T) {
	t.Parallel()
	end := time.Now().UTC().Add(45 * time.Minute).Truncate(time.Second)
	var revisions []apiclient.ExtraRoomRequest
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer owner-session" {
			t.Error("wrong management identity")
		}
		switch r.URL.Path {
		case "/v1/me/mandates/card-room/statement":
			writeJSONForTest(w, 200, apiclient.MandateStatement{
				Mandate: apiclient.Mandate{ID: "card-room", Subject: "refund-agent"},
				Limits:  []apiclient.MandateLimit{{Quantity: "sum(amount_cents)", Measure: "effects", Window: "rolling_1h", Status: "ok", BaseLimit: 50000, Limit: 50000, Unit: "EUR", Scale: 2, RoomWindowEnd: end}},
			})
		case "/v1/me/mandates/card-room/extra-room":
			var req apiclient.ExtraRoomRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Error(err)
			}
			revisions = append(revisions, req)
			writeJSONForTest(w, 200, apiclient.Mandate{ID: "card-room"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer api.Close()
	client, err := apiclient.NewSessionAware(api.URL, 0)
	if err != nil {
		t.Fatal(err)
	}
	mgr := newTestSessionManager(t)
	dash := handlers.NewDashboardHandler(client, mgr, handlers.StaticBearerSource(), nil)
	mux := http.NewServeMux()
	dash.Register(mux)
	h := handlers.RequireSession(mgr)(mux)
	issued := httptest.NewRecorder()
	if err := mgr.Issue(issued, session.Session{Subject: "alice", Bearer: "owner-session", Email: "a@example.com", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	cookie := issued.Result().Cookies()[0]
	// The owner types euros, not the tool's cents: 10 is 10.00 EUR.
	preview := postForm(h, cookie, "/cards/card-room/extra-room/preview", url.Values{"amount": {"10"}, "limit": {"sum(amount_cents)|rolling_1h|effects"}})
	if preview.Code != 200 || len(revisions) != 0 || !strings.Contains(preview.Body.String(), "10.00 EUR") || !strings.Contains(preview.Body.String(), components.InZone(end, time.UTC)) {
		t.Fatalf("preview mutated or lost units/window: %d %s", preview.Code, preview.Body.String())
	}
	form := url.Values{}
	for _, match := range regexp.MustCompile(`<input type="hidden" name="([^"]+)" value="([^"]*)"`).FindAllStringSubmatch(preview.Body.String(), -1) {
		form.Set(match[1], match[2])
	}
	if form.Get("revision_id") == "" || form.Get("duration") != "this_window" {
		t.Fatalf("default confirmation: %v", form)
	}
	confirmed := postForm(h, cookie, "/cards/card-room/extra-room", form)
	if confirmed.Code != http.StatusSeeOther || len(revisions) != 1 {
		t.Fatalf("confirmation: %d %s", confirmed.Code, confirmed.Body.String())
	}
	got := revisions[0]
	if got.Amount != 1000 || got.Duration != "this_window" || !got.WindowEnd.Equal(end) || got.ExpectedBase == nil || *got.ExpectedBase != 50000 || got.ID != form.Get("revision_id") {
		t.Fatalf("confirmation changed: %+v", got)
	}
	// An unreadable window or base never reaches the write endpoint.
	for _, field := range []string{"window_end", "base_limit"} {
		bad := url.Values{}
		for key, value := range form {
			bad[key] = append([]string(nil), value...)
		}
		bad.Set(field, "invalid")
		if result := postForm(h, cookie, "/cards/card-room/extra-room", bad); result.Code != 400 {
			t.Fatalf("invalid %s: %d", field, result.Code)
		}
	}
	if len(revisions) != 1 {
		t.Fatal("invalid confirmation wrote a revision")
	}
	denied := handlers.RequireSession(mgr)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mux.ServeHTTP(w, r.WithContext(components.WithCapabilities(r.Context(), []string{"cards.read"})))
	}))
	if response := postForm(denied, cookie, "/cards/card-room/extra-room", form); response.Code != 403 || len(revisions) != 1 {
		t.Fatalf("management exclusion: %d", response.Code)
	}
}

// "Until a date" is typed in the account's time zone, the review shows it
// there with the zone, and the confirmation carries the instant. The
// field is not read for "until the window ends".
func TestExtraRoomUntilADateInTheAccountZone(t *testing.T) {
	t.Parallel()
	var revisions []apiclient.ExtraRoomRequest
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/me/mandates/card-room/statement":
			writeJSONForTest(w, 200, apiclient.MandateStatement{
				Mandate: apiclient.Mandate{ID: "card-room", Subject: "refund-agent"},
				Limits: []apiclient.MandateLimit{{Quantity: "count", Measure: "attempts", Window: "calendar_day", Status: "ok", BaseLimit: 2, Limit: 2,
					RoomWindowEnd: time.Now().Add(time.Hour)}},
			})
		case "/v1/me/mandates/card-room/extra-room":
			var req apiclient.ExtraRoomRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			revisions = append(revisions, req)
			writeJSONForTest(w, 200, apiclient.Mandate{ID: "card-room"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer api.Close()
	client, err := apiclient.NewSessionAware(api.URL, 0)
	if err != nil {
		t.Fatal(err)
	}
	mgr := newTestSessionManager(t)
	dash := handlers.NewDashboardHandler(client, mgr, handlers.StaticBearerSource(), nil)
	mux := http.NewServeMux()
	dash.Register(mux)
	h := handlers.RequireSession(mgr)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mux.ServeHTTP(w, r.WithContext(components.WithTimeZone(r.Context(), "Europe/Paris")))
	}))
	issued := httptest.NewRecorder()
	if err := mgr.Issue(issued, session.Session{Subject: "alice", Bearer: "owner-session", Email: "a@example.com", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	cookie := issued.Result().Cookies()[0]

	req := httptest.NewRequest(http.MethodGet, "/cards/card-room/extra-room", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	page := rec.Body.String()
	if !strings.Contains(page, "2 calls per calendar day") || !strings.Contains(page, "Until (your time, Europe/Paris)") ||
		strings.Contains(page, "whole stored units") || strings.Contains(page, "calendar_day ·") {
		t.Fatalf("form: %s", page)
	}

	// "Until the window ends" with a stray date: the date is not read.
	if res := postForm(h, cookie, "/cards/card-room/extra-room/preview", url.Values{"amount": {"1"}, "limit": {"count|calendar_day|attempts"},
		"duration": {"this_window"}, "until": {"not a date"}}); res.Code != 200 {
		t.Fatalf("this window with a stray date: %d", res.Code)
	}

	year := time.Now().Year() + 1
	local := time.Date(year, time.January, 15, 9, 30, 0, 0, mustZone(t, "Europe/Paris"))
	preview := postForm(h, cookie, "/cards/card-room/extra-room/preview", url.Values{"amount": {"1"}, "limit": {"count|calendar_day|attempts"},
		"duration": {"until_date"}, "until": {local.Format("2006-01-02T15:04")}})
	body := preview.Body.String()
	if preview.Code != 200 || !strings.Contains(body, "1 call") || !strings.Contains(body, components.InZone(local, local.Location())) ||
		!strings.Contains(body, "CET") || strings.Contains(body, "management identity") {
		t.Fatalf("preview: %d %s", preview.Code, body)
	}
	confirm := url.Values{}
	for _, m := range regexp.MustCompile(`<input type="hidden" name="([^"]+)" value="([^"]*)"`).FindAllStringSubmatch(body, -1) {
		confirm.Set(m[1], m[2])
	}
	if res := postForm(h, cookie, "/cards/card-room/extra-room", confirm); res.Code != http.StatusSeeOther || len(revisions) != 1 {
		t.Fatalf("confirm: %d %s", res.Code, res.Body.String())
	}
	if got := revisions[0]; got.Amount != 1 || got.Duration != "until_date" || !got.Until.Equal(local) {
		t.Fatalf("revision: %+v, want until %s", got, local.UTC())
	}
}

func mustZone(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatal(err)
	}
	return loc
}
