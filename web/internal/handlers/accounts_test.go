package handlers

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
	webauth "github.com/kiff/cloud/web/internal/auth"
	"github.com/kiff/cloud/web/internal/components"
	"github.com/kiff/cloud/web/internal/session"
)

// fakeMembersAPI is apps/api's the governance-roles design surface,
// enough for the web.
type fakeMembersAPI struct {
	mu          sync.Mutex
	memberships []apiclient.Membership
	role        string
	caps        []string
	headers     map[string]string // path -> Kiff-Tenant it was called with
	accepted    string
	meStatus    int
	meBody      string
}

func (f *fakeMembersAPI) server(t *testing.T) *apiclient.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		if f.headers == nil {
			f.headers = map[string]string{}
		}
		f.headers[r.Method+" "+r.URL.Path] = r.Header.Get(apiclient.TenantHeader)
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.Method + " " + r.URL.Path {
		case "GET /v1/me/memberships":
			_ = json.NewEncoder(w).Encode(map[string]any{"memberships": f.memberships})
		case "GET /v1/me":
			if f.meStatus != 0 {
				w.WriteHeader(f.meStatus)
				_, _ = w.Write([]byte(f.meBody))
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"tenant_id": "t1", "member_role": f.role, "capabilities": f.caps})
		case "GET /v1/tenant/members":
			_ = json.NewEncoder(w).Encode(map[string]any{"members": []map[string]any{
				{"subject": "u1", "email": "alice@x.io", "role": f.role, "status": "active", "you": true},
				{"subject": "u2", "email": "bob@x.io", "role": "approver", "status": "active"},
			}})
		case "GET /v1/me/usage/people":
			_, _ = w.Write([]byte(`{"period":{"start":"2026-10-01T00:00:00Z","end":"2026-11-01T00:00:00Z"},
				"people":[{"subject":"u2","tasks":9,"actions":31}],"no_person":{"tasks":6,"actions":1},"people_since":"2026-10-27T14:37:00Z",
				"groups":[{"id":"g1","name":"Support","people":2,"tasks":9,"actions":31}]}`))
		case "GET /v1/tenant/governance":
			_ = json.NewEncoder(w).Encode(map[string]any{"separation_of_duties": false})
		case "POST /v1/invites/preview":
			_ = json.NewEncoder(w).Encode(map[string]any{"tenant_id": "t2", "account": "Acme", "role": "approver", "invited_by": "boss@acme.test"})
		case "POST /v1/invites/accept":
			var body struct{ Token string }
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.mu.Lock()
			f.accepted = body.Token
			f.mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"tenant_id": "t2", "account": "Acme", "role": "approver"})
		case "PUT /v1/tenant/members/u2/role":
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":"reauth_required","message":"needs a recent sign-in"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	c, err := apiclient.NewSessionAware(srv.URL, 0)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func testSessions(t *testing.T) *session.Manager {
	t.Helper()
	m, err := session.NewManager([]byte(strings.Repeat("k", 32)), session.WithSecure(false))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func signedIn(r *http.Request, tenant string) *http.Request {
	s := session.Session{Subject: "u1", Bearer: "u1", ExpiresAt: time.Now().Add(time.Hour), Tenant: tenant}
	return r.WithContext(withSession(r.Context(), s))
}

// captured records what SelectTenant passed on.
type captured struct {
	session session.Session
	tenant  string
	sw      components.AccountSwitch
	called  bool
}

func (c *captured) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.called = true
		c.session, _ = SessionFrom(r.Context())
		c.tenant = apiclient.TenantFrom(r.Context())
		c.sw = components.AccountSwitchFrom(r.Context())
	})
}

func TestSelectTenantPicksAndKeepsAnActiveAccount(t *testing.T) {
	api := &fakeMembersAPI{memberships: []apiclient.Membership{
		{TenantID: "t1", Account: "Own", Role: "admin", Status: "active"},
		{TenantID: "t2", Account: "Acme", Role: "approver", Status: "active"},
	}}
	h := NewAccountsHandler(api.server(t), testSessions(t), staticBearerSource{}, nil)

	for _, c := range []struct{ had, want string }{{"", "t1"}, {"t2", "t2"}, {"t-gone", "t1"}} {
		got := &captured{}
		rec := httptest.NewRecorder()
		h.SelectTenant(got.handler()).ServeHTTP(rec, signedIn(httptest.NewRequest(http.MethodGet, "/cards", nil), c.had))
		if !got.called || got.tenant != c.want || got.session.Tenant != c.want {
			t.Fatalf("had %q: tenant %q, session %q; want %q", c.had, got.tenant, got.session.Tenant, c.want)
		}
		if reissued := rec.Header().Get("Set-Cookie") != ""; reissued != (c.had != c.want) {
			t.Errorf("had %q: cookie reissued = %v", c.had, reissued)
		}
		if len(got.sw.Accounts) != 2 || got.sw.Current().TenantID != c.want {
			t.Errorf("had %q: switcher = %+v", c.had, got.sw)
		}
	}
	if hdr := api.headers["GET /v1/me/memberships"]; hdr != "" {
		t.Errorf("memberships were read with Kiff-Tenant %q; that route resolves no account", hdr)
	}
}

func TestSelectTenantSendsTheAccountToTheAPI(t *testing.T) {
	api := &fakeMembersAPI{memberships: []apiclient.Membership{{TenantID: "t2", Role: "editor", Status: "active"}}}
	client := api.server(t)
	h := NewAccountsHandler(client, testSessions(t), staticBearerSource{}, nil)
	page := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = client.GetMeBearer(r.Context(), "u1")
	})
	h.SelectTenant(page).ServeHTTP(httptest.NewRecorder(), signedIn(httptest.NewRequest(http.MethodGet, "/cards", nil), ""))
	if got := api.headers["GET /v1/me"]; got != "t2" {
		t.Fatalf("GET /v1/me went with Kiff-Tenant %q, want t2", got)
	}
}

func TestSelectTenantTellsASuspendedPerson(t *testing.T) {
	api := &fakeMembersAPI{memberships: []apiclient.Membership{{TenantID: "t1", Role: "editor", Status: "suspended"}}}
	h := NewAccountsHandler(api.server(t), testSessions(t), staticBearerSource{}, nil)
	got := &captured{}
	rec := httptest.NewRecorder()
	h.SelectTenant(got.handler()).ServeHTTP(rec, signedIn(httptest.NewRequest(http.MethodGet, "/cards", nil), ""))
	if got.called || rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "Your access is suspended") {
		t.Fatalf("suspended: %d called=%v", rec.Code, got.called)
	}
}

func TestSelectTenantLetsAFirstSignInThrough(t *testing.T) {
	api := &fakeMembersAPI{}
	h := NewAccountsHandler(api.server(t), testSessions(t), staticBearerSource{}, nil)
	got := &captured{}
	h.SelectTenant(got.handler()).ServeHTTP(httptest.NewRecorder(), signedIn(httptest.NewRequest(http.MethodGet, "/cards", nil), ""))
	if !got.called {
		t.Fatal("a person with no account yet must reach the page; the API creates their account on first use")
	}
}

func TestInvitePageShowsThenAccepts(t *testing.T) {
	api := &fakeMembersAPI{}
	h := NewAccountsHandler(api.server(t), testSessions(t), staticBearerSource{}, nil)
	mux := http.NewServeMux()
	h.RegisterInvite(mux)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, signedIn(httptest.NewRequest(http.MethodGet, "/invite/kiff_inv_abc", nil), ""))
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, "Join Acme") || !strings.Contains(body, "boss@acme.test") ||
		!strings.Contains(body, "Approver") {
		t.Fatalf("preview: %d\n%s", rec.Code, body)
	}
	if api.accepted != "" {
		t.Fatal("showing the invitation accepted it")
	}

	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, signedIn(httptest.NewRequest(http.MethodPost, "/invite/kiff_inv_abc", nil), "t1"))
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/cards" || api.accepted != "kiff_inv_abc" {
		t.Fatalf("accept: %d %q accepted=%q", rec.Code, rec.Header().Get("Location"), api.accepted)
	}
	if !strings.Contains(rec.Header().Get("Set-Cookie"), session.CookieName) {
		t.Fatal("the session was not switched to the joined account")
	}
}

func TestInvitePageNeedsASignIn(t *testing.T) {
	h := NewAccountsHandler((&fakeMembersAPI{}).server(t), testSessions(t), staticBearerSource{}, nil)
	mux := http.NewServeMux()
	h.RegisterInvite(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/invite/kiff_inv_abc", nil))
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/sign-in?next="+url.QueryEscape("/invite/kiff_inv_abc") {
		t.Fatalf("anonymous: %d %q", rec.Code, rec.Header().Get("Location"))
	}
}

func TestPeoplePageShowsControlsByRole(t *testing.T) {
	render := func(role string, caps []string) string {
		api := &fakeMembersAPI{role: role, caps: caps}
		h := NewPeopleHandler(api.server(t), testSessions(t), staticBearerSource{}, nil)
		mux := http.NewServeMux()
		h.Register(mux)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, signedIn(httptest.NewRequest(http.MethodGet, "/dashboard/people", nil), "t1"))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d", role, rec.Code)
		}
		return rec.Body.String()
	}
	admin := render("admin", []string{"read", "members.manage", "governance.manage"})
	if !strings.Contains(admin, "bob@x.io") || !strings.Contains(admin, `action="/dashboard/people/members/u2/role"`) ||
		!strings.Contains(admin, "?tab=invite") {
		t.Fatal("an admin sees people with role controls and the Invite tab")
	}
	viewer := render("viewer", []string{"read", "personal"})
	if !strings.Contains(viewer, "bob@x.io") || strings.Contains(viewer, "/dashboard/people/members/u2/") || strings.Contains(viewer, "?tab=invite") {
		t.Fatal("a viewer sees the people and no controls")
	}
}

// Usage by person and group is the billing admin's tab: each person by
// their email, what no person's run made, and each group.
func TestPeopleUsageTabIsTheBillingAdmins(t *testing.T) {
	render := func(caps []string) string {
		api := &fakeMembersAPI{role: "admin", caps: caps}
		h := NewPeopleHandler(api.server(t), testSessions(t), staticBearerSource{}, nil)
		mux := http.NewServeMux()
		h.Register(mux)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, signedIn(httptest.NewRequest(http.MethodGet, "/dashboard/people?tab=usage", nil), "t1"))
		if rec.Code != http.StatusOK {
			t.Fatalf("%v: %d", caps, rec.Code)
		}
		return rec.Body.String()
	}
	body := render([]string{"read", "members.manage", "billing.manage"})
	for _, want := range []string{"?tab=usage", "bob@x.io", "<td>9</td><td>31</td>", "Not tied to a person", "Schedules, doors and your own agents aren't tied to a person.", "before 27 October, when KIFF began recording who asked", "Support", "not how people work"} {
		if !strings.Contains(body, want) {
			t.Errorf("billing admin: missing %q", want)
		}
	}
	if body := render([]string{"read", "members.manage"}); strings.Contains(body, "?tab=usage") || strings.Contains(body, "Not tied to a person") {
		t.Error("someone without billing sees usage by person")
	}
}

func TestPeopleChangeNeedingStepUpSaysSignInAgain(t *testing.T) {
	api := &fakeMembersAPI{role: "admin", caps: []string{"members.manage"}}
	h := NewPeopleHandler(api.server(t), testSessions(t), staticBearerSource{}, nil)
	mux := http.NewServeMux()
	h.Register(mux)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/dashboard/people/members/u2/role", strings.NewReader("role=admin"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	mux.ServeHTTP(rec, signedIn(req, "t1"))
	if rec.Code != http.StatusSeeOther || !strings.Contains(rec.Header().Get("Location"), "err=reauth_required") {
		t.Fatalf("redirect: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	loc := rec.Header().Get("Location")
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, signedIn(httptest.NewRequest(http.MethodGet, loc, nil), "t1"))
	if !strings.Contains(rec.Body.String(), "/sign-out?next=") || !strings.Contains(rec.Body.String(), "Sign in again") {
		t.Fatalf("the page does not offer a fresh sign-in:\n%s", rec.Body.String())
	}
}

// Sign-out's ?next= (the governance-roles design step-up) only
// ever leads back through sign-in to a page on this site.
func TestSignOutNextStaysOnSite(t *testing.T) {
	h := NewAuthHandler(webauth.NewStubProvider(nil), testSessions(t), nil)
	mux := http.NewServeMux()
	h.Register(mux)
	for next, want := range map[string]string{
		"/dashboard/people?tab=policy": "/sign-in?next=" + url.QueryEscape("/dashboard/people?tab=policy"),
		"https://evil.example/":        "/",
		"//evil.example":               "/",
	} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/sign-out?next="+url.QueryEscape(next), nil))
		if got := rec.Header().Get("Location"); got != want {
			t.Errorf("next=%q: Location %q, want %q", next, got, want)
		}
	}
}

// An admin invites on a full-screen sheet, one decision a step, that
// posts to the route that sends invitations; anyone else is sent back
// to the tab.
func TestInviteSheetIsForAdmins(t *testing.T) {
	get := func(role string, caps []string) *httptest.ResponseRecorder {
		api := &fakeMembersAPI{role: role, caps: caps}
		h := NewPeopleHandler(api.server(t), testSessions(t), staticBearerSource{}, nil)
		mux := http.NewServeMux()
		h.Register(mux)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, signedIn(httptest.NewRequest(http.MethodGet, "/dashboard/people/invite", nil), "t1"))
		return rec
	}
	admin := get("admin", []string{"read", "members.manage"})
	body := admin.Body.String()
	if admin.Code != http.StatusOK {
		t.Fatalf("admin: %d", admin.Code)
	}
	for _, want := range []string{`action="/dashboard/people/invite"`, `data-wk-step="1"`, `data-wk-step="2"`,
		`type="email" name="email"`, `name="role" value="approver" checked`, `name="lifetime_hours" value="0" checked`,
		"What can they do?", "Answers held actions.", "css/walkthrough.css"} {
		if !strings.Contains(body, want) {
			t.Errorf("the sheet lacks %q", want)
		}
	}
	viewer := get("viewer", []string{"read", "personal"})
	if viewer.Code != http.StatusSeeOther || viewer.Header().Get("Location") != "/dashboard/people?tab=invite" {
		t.Fatalf("viewer: %d → %q", viewer.Code, viewer.Header().Get("Location"))
	}
}
