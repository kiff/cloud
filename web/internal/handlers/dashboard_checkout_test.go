package handlers_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/web/internal/apiclient"
	"github.com/kiff/cloud/web/internal/handlers"
	"github.com/kiff/cloud/web/internal/session"
)

// Settings' "Choose Company": signed out, it signs in and comes back;
// signed in, it goes to Stripe's checkout for Company; Solo, sold only in
// the app, goes back to Settings; an account with a plan goes to Settings.
func TestCheckoutStartsThePlanOnTheWeb(t *testing.T) {
	var asked string
	subscribed := false
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/me/billing/checkout" {
			writeJSONForTest(w, 200, map[string]any{})
			return
		}
		var body struct{ Plan string }
		_ = json.NewDecoder(r.Body).Decode(&body)
		asked = body.Plan
		if subscribed {
			writeJSONForTest(w, 409, map[string]string{"error": "already_subscribed"})
			return
		}
		writeJSONForTest(w, 200, map[string]string{"url": "https://checkout.stripe.com/c/pay/cs_test_fixture"})
	}))
	defer api.Close()
	client, _ := apiclient.NewSessionAware(api.URL, 0)
	mgr := newTestSessionManager(t)
	dash := handlers.NewDashboardHandler(client, mgr, handlers.StaticBearerSource(), nil)
	mux := http.NewServeMux()
	dash.Register(mux)
	h := handlers.RequireSession(mgr)(mux)

	// Signed out: to sign-in, coming back to checkout after.
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("GET", "/dashboard/checkout?plan=team", nil))
	if rr.Code != http.StatusFound || !strings.Contains(rr.Header().Get("Location"), "checkout") {
		t.Fatalf("signed out: %d %q", rr.Code, rr.Header().Get("Location"))
	}

	signed := httptest.NewRecorder()
	mgr.Issue(signed, session.Session{Subject: "owner", Bearer: "person-bearer", ExpiresAt: time.Now().Add(time.Hour)})
	cookie := signed.Result().Cookies()[0]
	get := func(target string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", target, nil)
		req.AddCookie(cookie)
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		return rr
	}
	if rr := get("/dashboard/checkout?plan=team"); rr.Code != http.StatusSeeOther || rr.Header().Get("Location") != "https://checkout.stripe.com/c/pay/cs_test_fixture" || asked != "team" {
		t.Fatalf("signed in: %d %q, asked %q", rr.Code, rr.Header().Get("Location"), asked)
	}
	// Solo isn't sold here: back to Settings, nothing asked of the API.
	asked = ""
	if rr := get("/dashboard/checkout?plan=solo"); rr.Code != http.StatusSeeOther || !strings.HasPrefix(rr.Header().Get("Location"), "/dashboard/settings") || asked != "" {
		t.Fatalf("solo: %d %q, asked %q", rr.Code, rr.Header().Get("Location"), asked)
	}
	// Anything else is Company.
	if get("/dashboard/checkout?plan=gold"); asked != "team" {
		t.Fatalf("plan=gold asked %q", asked)
	}
	subscribed = true
	if rr := get("/dashboard/checkout?plan=team"); rr.Code != http.StatusSeeOther || !strings.HasPrefix(rr.Header().Get("Location"), "/dashboard/settings") {
		t.Fatalf("already subscribed: %d %q", rr.Code, rr.Header().Get("Location"))
	}
}

// A pack on the web: ?pack= opens its one-time checkout, with the request
// it answers; declining a request posts the note to the API. A request
// already answered goes back to Settings saying so.
func TestCheckoutPacksAndDeclines(t *testing.T) {
	var asked, declined map[string]string
	answered := false
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/me/billing/checkout":
			_ = json.NewDecoder(r.Body).Decode(&asked)
			if answered {
				writeJSONForTest(w, 409, map[string]string{"error": "request_answered"})
				return
			}
			writeJSONForTest(w, 200, map[string]string{"url": "https://checkout.stripe.com/c/pay/cs_pack"})
		case "/v1/me/pack-requests/pr_1/decline":
			_ = json.NewDecoder(r.Body).Decode(&declined)
			writeJSONForTest(w, 200, map[string]string{"status": "declined"})
		default:
			writeJSONForTest(w, 200, map[string]any{})
		}
	}))
	defer api.Close()
	client, _ := apiclient.NewSessionAware(api.URL, 0)
	mgr := newTestSessionManager(t)
	mux := http.NewServeMux()
	handlers.NewDashboardHandler(client, mgr, handlers.StaticBearerSource(), nil).Register(mux)
	h := handlers.RequireSession(mgr)(mux)
	signed := httptest.NewRecorder()
	mgr.Issue(signed, session.Session{Subject: "owner", Bearer: "person-bearer", ExpiresAt: time.Now().Add(time.Hour)})
	cookie := signed.Result().Cookies()[0]
	do := func(method, target, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, target, strings.NewReader(body))
		if body != "" {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		req.AddCookie(cookie)
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		return rr
	}
	if rr := do("GET", "/dashboard/checkout?pack=rayo&request=pr_1", ""); rr.Code != http.StatusSeeOther ||
		rr.Header().Get("Location") != "https://checkout.stripe.com/c/pay/cs_pack" || asked["pack"] != "rayo" || asked["request_id"] != "pr_1" {
		t.Fatalf("pack: %d %q %v", rr.Code, rr.Header().Get("Location"), asked)
	}
	answered = true
	if rr := do("GET", "/dashboard/checkout?pack=rayo&request=pr_1", ""); rr.Code != http.StatusSeeOther || !strings.HasPrefix(rr.Header().Get("Location"), "/dashboard/settings") {
		t.Fatalf("answered: %d %q", rr.Code, rr.Header().Get("Location"))
	}
	if rr := do("POST", "/dashboard/pack-requests/pr_1/decline", "note=Next+month"); rr.Code != http.StatusSeeOther || declined["note"] != "Next month" {
		t.Fatalf("decline: %d %v", rr.Code, declined)
	}
}
