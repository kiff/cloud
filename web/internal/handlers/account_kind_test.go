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

// kindAPI stubs /v1/me and the account-kind write.
type kindAPI struct {
	mu      sync.Mutex
	kind    string
	meFails bool
	forbid  bool
	set     []string
	chat    []bool
	zones   []string
	names   []string
	zone    string
}

func (a *kindAPI) serve(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	switch {
	case r.URL.Path == "/v1/me" && r.Method == http.MethodGet:
		if a.meFails {
			writeJSONForTest(w, http.StatusInternalServerError, map[string]any{"error": "down"})
			return
		}
		writeJSONForTest(w, http.StatusOK, map[string]any{"subject": "alice", "tenant_id": "t1", "tenant_slug": "alice", "account_kind": a.kind, "time_zone": a.zone})
	case r.URL.Path == "/v1/me/account-kind" && r.Method == http.MethodPut:
		if a.forbid {
			writeJSONForTest(w, http.StatusForbidden, map[string]any{"error": "no"})
			return
		}
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		a.set = append(a.set, body["kind"])
		a.kind = body["kind"]
		writeJSONForTest(w, http.StatusOK, map[string]any{"account_kind": a.kind})
	case r.URL.Path == "/v1/me/time-zone" && r.Method == http.MethodPut:
		if a.forbid {
			writeJSONForTest(w, http.StatusForbidden, map[string]any{"error": "no"})
			return
		}
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		a.zones = append(a.zones, body["time_zone"])
		writeJSONForTest(w, http.StatusOK, map[string]any{"time_zone": body["time_zone"]})
	case r.URL.Path == "/v1/me/account-name" && r.Method == http.MethodPut:
		if a.forbid {
			writeJSONForTest(w, http.StatusForbidden, map[string]any{"error": "no"})
			return
		}
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		a.names = append(a.names, body["name"])
		writeJSONForTest(w, http.StatusOK, map[string]any{"name": body["name"]})
	case r.URL.Path == "/v1/me/chat-approvals" && r.Method == http.MethodPut:
		if a.forbid {
			writeJSONForTest(w, http.StatusForbidden, map[string]any{"error": "no"})
			return
		}
		var body map[string]bool
		_ = json.NewDecoder(r.Body).Decode(&body)
		a.chat = append(a.chat, body["enabled"])
		writeJSONForTest(w, http.StatusOK, map[string]any{"chat_approvals": body["enabled"]})
	case r.URL.Path == "/v1/me/mandates/card-z/statement":
		writeJSONForTest(w, http.StatusOK, apiclient.MandateStatement{Mandate: apiclient.Mandate{ID: "card-z"},
			Limits: []apiclient.MandateLimit{{Quantity: "count", Window: "calendar_day", Measure: "attempts", Status: "ok", Limit: 2, BaseLimit: 2}}})
	case r.URL.Path == "/v1/me/mandates":
		writeJSONForTest(w, http.StatusOK, map[string]any{"mandates": []any{}})
	default:
		writeJSONForTest(w, http.StatusNotFound, map[string]any{"error": "not found"})
	}
}

func kindRig(t *testing.T, api *kindAPI) (http.Handler, *http.Cookie) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(api.serve))
	t.Cleanup(srv.Close)
	client, err := apiclient.NewSessionAware(srv.URL, 0)
	if err != nil {
		t.Fatal(err)
	}
	mgr := newTestSessionManager(t)
	dash := handlers.NewDashboardHandler(client, mgr, handlers.StaticBearerSource(), nil)
	mux := http.NewServeMux()
	dash.Register(mux)
	issue := httptest.NewRecorder()
	if err := mgr.Issue(issue, session.Session{Subject: "alice", Bearer: "b", Email: "a@example.com", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	return handlers.RequireSession(mgr)(dash.WithChrome(mux)), issue.Result().Cookies()[0]
}

// the cards-for-any-agent design: an account whose owner has not
// chosen is asked once, before any page of the app; the answer
// returns them where they were going, on this site only.
func TestAccountKind_AskedOnceThenRemembered(t *testing.T) {
	t.Parallel()
	api := &kindAPI{}
	h, c := kindRig(t, api)
	rec := send(h, c, http.MethodGet, "/cards?x=1")
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/welcome?next=%2Fcards%3Fx%3D1" {
		t.Fatalf("unchosen: %d → %q", rec.Code, rec.Header().Get("Location"))
	}
	welcome := send(h, c, http.MethodGet, "/welcome?next=%2Fcards%3Fx%3D1")
	if welcome.Code != http.StatusOK || !strings.Contains(welcome.Body.String(), "Who is this account for?") ||
		!strings.Contains(welcome.Body.String(), `name="next" value="/cards?x=1"`) {
		t.Fatalf("welcome: %d", welcome.Code)
	}
	if rec := send(h, c, http.MethodGet, "/auth/device"); rec.Code == http.StatusSeeOther && strings.HasPrefix(rec.Header().Get("Location"), "/welcome") {
		t.Error("the device approval page was interrupted")
	}
	rec = postForm(h, c, "/welcome", url.Values{"kind": {"personal"}, "next": {"/cards?x=1"}})
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/cards?x=1" || len(api.set) != 1 || api.set[0] != "personal" {
		t.Fatalf("choose: %d → %q, set %v", rec.Code, rec.Header().Get("Location"), api.set)
	}
	if rec := send(h, c, http.MethodGet, "/cards"); rec.Code != http.StatusOK {
		t.Fatalf("after choosing: %d", rec.Code)
	}
	for _, next := range []string{"//evil.example/x", "https://evil.example", `/\evil.example`, "/\t/evil.example", "/\n/evil.example", "/ /evil.example", "/welcome"} {
		api.kind = ""
		rec := postForm(h, c, "/welcome", url.Values{"kind": {"company"}, "next": {next}})
		if loc := rec.Header().Get("Location"); loc != "/cards" {
			t.Errorf("next %q → %q, want /cards", next, loc)
		}
	}
}

// An API that cannot answer /v1/me must not trap anyone on the question.
func TestAccountKind_NotAskedWhenUnknown(t *testing.T) {
	t.Parallel()
	h, c := kindRig(t, &kindAPI{meFails: true})
	if rec := send(h, c, http.MethodGet, "/cards"); rec.Code == http.StatusSeeOther {
		t.Fatalf("redirected to %q", rec.Header().Get("Location"))
	}
}

// A personal account sees My Cards, My agents, Connected tools, Needs
// you and Activity, and no domain or contract machinery; its API keys
// are under Account. A company's sidebar is the workspace as it was,
// with API keys once, under Setup.
func TestAccountKind_Sidebar(t *testing.T) {
	t.Parallel()
	personal, c := kindRig(t, &kindAPI{kind: "personal"})
	body := send(personal, c, http.MethodGet, "/cards").Body.String()
	for _, want := range []string{">My Cards<", ">My agents<", ">Connected tools<", `href="/needs-you"`, ">Activity<", `<a class="btn btn-primary" href="/tools">Give an agent a Card</a>`} {
		if !strings.Contains(body, want) {
			t.Errorf("personal sidebar missing %q", want)
		}
	}
	for _, unwanted := range []string{">Domain<", ">Build<", ">Govern<", "dash-domain-select", `href="/cards/new"`} {
		if strings.Contains(body, unwanted) {
			t.Errorf("personal sidebar shows %q", unwanted)
		}
	}
	if n := strings.Count(body, ">API keys<"); n != 1 {
		t.Errorf("personal sidebar shows API keys %d times, want once", n)
	}
	if account := strings.Index(body, ">Account<"); account < 0 || strings.Index(body, ">API keys<") < account {
		t.Error("a personal account's API keys are not under Account")
	}
	company, c2 := kindRig(t, &kindAPI{kind: "company"})
	body = send(company, c2, http.MethodGet, "/cards").Body.String()
	for _, want := range []string{">Cards<", ">Tools<", ">Domain<", ">API keys<"} {
		if !strings.Contains(body, want) {
			t.Errorf("company sidebar missing %q", want)
		}
	}
	if n := strings.Count(body, ">API keys<"); n != 1 {
		t.Errorf("company sidebar shows API keys %d times, want once", n)
	}
}

// Settings changes it; only the owner or an admin may.
func TestAccountKind_ChangedInSettings(t *testing.T) {
	t.Parallel()
	api := &kindAPI{kind: "company"}
	h, c := kindRig(t, api)
	rec := postForm(h, c, "/dashboard/account-kind", url.Values{"kind": {"personal"}})
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/dashboard/settings?tab=view" || api.kind != "personal" {
		t.Fatalf("change: %d → %q (%s)", rec.Code, rec.Header().Get("Location"), api.kind)
	}
	if rec := postForm(h, c, "/dashboard/account-kind", url.Values{"kind": {"family"}}); len(api.set) != 1 || rec.Code != http.StatusSeeOther {
		t.Errorf("an invalid kind was sent: %v", api.set)
	}
	api.forbid = true
	msg := followFlashTo(t, h, c, postForm(h, c, "/dashboard/account-kind", url.Values{"kind": {"company"}}), "/dashboard/settings?tab=view")
	if !strings.Contains(msg, "Only the account's owner or an admin") {
		t.Errorf("forbidden: %s", msg)
	}
}

// A page rendered by a POST (step 2 of /tools/new) draws the account's
// own sidebar, not the company one; a POST is never sent to /welcome.
func TestAccountKind_SidebarOnAPostedPage(t *testing.T) {
	t.Parallel()
	h, c := kindRig(t, &kindAPI{kind: "personal"})
	body := postForm(h, c, "/tools/discover", url.Values{"url": {"https://tools.example.com/mcp"}}).Body.String()
	if !strings.Contains(body, "My Cards") || strings.Contains(body, ">Domain<") {
		t.Error("a personal account's posted page shows the company sidebar")
	}
	h, c = kindRig(t, &kindAPI{})
	if rec := postForm(h, c, "/tools/discover", url.Values{"url": {"https://tools.example.com/mcp"}}); rec.Code == http.StatusSeeOther &&
		strings.HasPrefix(rec.Header().Get("Location"), "/welcome") {
		t.Error("a POST was sent to /welcome")
	}
}

// the cards-for-any-agent design: the owner turns the chat offer on
// or off from the Account tab; an agent-less session that may not is
// told so.
func TestChatApprovals_SetFromSettings(t *testing.T) {
	t.Parallel()
	api := &kindAPI{kind: "personal"}
	h, c := kindRig(t, api)
	rec := postForm(h, c, "/dashboard/chat-approvals", url.Values{"enabled": {"false"}})
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/dashboard/settings?tab=account" || len(api.chat) != 1 || api.chat[0] {
		t.Fatalf("off: %d → %q, sent %v", rec.Code, rec.Header().Get("Location"), api.chat)
	}
	postForm(h, c, "/dashboard/chat-approvals", url.Values{"enabled": {"maybe"}})
	if len(api.chat) != 1 {
		t.Errorf("an invalid value was sent: %v", api.chat)
	}
	api.forbid = true
	msg := followFlashTo(t, h, c, postForm(h, c, "/dashboard/chat-approvals", url.Values{"enabled": {"true"}}), "/dashboard/settings?tab=account")
	if !strings.Contains(msg, "Only the account's owner or an admin") {
		t.Errorf("forbidden: %s", msg)
	}
}

func TestAccountTimeZone_SetFromSettings(t *testing.T) {
	api := &kindAPI{kind: "personal"}
	h, c := kindRig(t, api)
	rec := postForm(h, c, "/dashboard/time-zone", url.Values{"time_zone": {"Europe/Paris"}})
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/dashboard/settings?tab=account" || len(api.zones) != 1 || api.zones[0] != "Europe/Paris" {
		t.Fatalf("save: %d %v", rec.Code, api.zones)
	}
	api.forbid = true
	msg := followFlashTo(t, h, c, postForm(h, c, "/dashboard/time-zone", url.Values{"time_zone": {"UTC"}}), "/dashboard/settings?tab=account")
	if !strings.Contains(msg, "Only the account") || len(api.zones) != 1 {
		t.Fatalf("forbidden: %s", msg)
	}
}

// An owner or admin names the account from Settings.
func TestAccountName_SetFromSettings(t *testing.T) {
	api := &kindAPI{kind: "personal"}
	h, c := kindRig(t, api)
	rec := postForm(h, c, "/dashboard/account-name", url.Values{"name": {"Kiff Agents"}})
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/dashboard/settings?tab=account" || len(api.names) != 1 || api.names[0] != "Kiff Agents" {
		t.Fatalf("save: %d %v", rec.Code, api.names)
	}
	api.forbid = true
	msg := followFlashTo(t, h, c, postForm(h, c, "/dashboard/account-name", url.Values{"name": {"Other"}}), "/dashboard/settings?tab=account")
	if !strings.Contains(msg, "Only the account") || len(api.names) != 1 {
		t.Fatalf("forbidden: %s", msg)
	}
}

// Every dashboard page reads times in the account's time zone, which
// the chrome takes from /v1/me; an account without one reads UTC.
func TestChromeCarriesTheAccountTimeZone(t *testing.T) {
	t.Parallel()
	for zone, want := range map[string]string{"Europe/Paris": "Until (your time, Europe/Paris)", "": "Until (your time, UTC)"} {
		api := &kindAPI{kind: "tools", zone: zone}
		h, c := kindRig(t, api)
		if rec := send(h, c, http.MethodGet, "/cards/card-z/extra-room"); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("zone %q: %d, want %q", zone, rec.Code, want)
		}
	}
}
