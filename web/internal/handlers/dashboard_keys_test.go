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
	"github.com/kiff/cloud/web/internal/pages"
	"github.com/kiff/cloud/web/internal/session"
)

func TestDashboard_KeyPanelRendersRevokeForms(t *testing.T) {
	t.Parallel()
	v := freshDashboardView()
	v.Keys = []apiclient.KeyListItem{{
		ID:        "key-123",
		Label:     "onboarding",
		CreatedAt: "2026-05-26T16:09:19Z",
	}}
	v.KeyCount = 1

	body := renderKeys(t, v)
	for _, want := range []string{
		`>Action</th>`,
		`action="/dashboard/keys/key-123/revoke"`,
		`>Revoke</button>`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("dashboard missing %q\n---\n%s", want, body)
		}
	}
}

// Keys with the same name are told apart by the agent they belong to and
// their last use, most recently used first.
func TestDashboard_KeyPanelShowsAgentAndLastUse(t *testing.T) {
	t.Parallel()
	v := freshDashboardView()
	used := "2026-10-02T10:00:00Z"
	v.Keys = []apiclient.KeyListItem{
		{ID: "key-old", Label: "Gateway key for a", AgentID: "a", CreatedAt: "2026-10-01T10:00:00Z"},
		{ID: "key-used", Label: "Gateway key for a", AgentID: "a", CreatedAt: "2026-09-01T10:00:00Z", LastUsedAt: &used},
	}
	body := renderKeys(t, v)
	for _, want := range []string{">Agent</th>", ">Last used</th>", `href="/agents/a/keys"`, "Never used"} {
		if !strings.Contains(body, want) {
			t.Errorf("keys page missing %q", want)
		}
	}
	if strings.Index(body, "key-used") > strings.Index(body, "key-old") {
		t.Error("the used key is not listed first")
	}
}

func TestDashboard_RevokeKeyPostsToAPI(t *testing.T) {
	t.Parallel()

	var sawDelete bool
	var gotAuth string
	apiMux := http.NewServeMux()
	apiMux.HandleFunc("DELETE /v1/keys/{id}", func(w http.ResponseWriter, r *http.Request) {
		sawDelete = true
		gotAuth = r.Header.Get("Authorization")
		if r.PathValue("id") != "key-123" {
			t.Fatalf("key id = %q, want key-123", r.PathValue("id"))
		}
		w.WriteHeader(http.StatusNoContent)
	})
	apiSrv := httptest.NewServer(apiMux)
	t.Cleanup(apiSrv.Close)

	apiClient, err := apiclient.NewSessionAware(apiSrv.URL, 0)
	if err != nil {
		t.Fatalf("NewSessionAware: %v", err)
	}
	mgr := newTestSessionManager(t)
	dashMux := http.NewServeMux()
	handlers.NewDashboardHandler(apiClient, mgr, nil, nil).Register(dashMux)
	protected := handlers.RequireSession(mgr)(dashMux)

	issue := httptest.NewRecorder()
	if err := mgr.Issue(issue, session.Session{
		Subject:   "user-1",
		Bearer:    "session-bearer",
		Email:     "user@example.com",
		ExpiresAt: time.Now().UTC().Add(time.Hour),
	}); err != nil {
		t.Fatalf("Issue: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/dashboard/keys/key-123/revoke", nil)
	req.AddCookie(issue.Result().Cookies()[0])
	rec := httptest.NewRecorder()
	protected.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303 (body=%q)", rec.Code, rec.Body.String())
	}
	if loc := rec.Header().Get("Location"); loc != "/dashboard/keys" {
		t.Fatalf("Location = %q, want /dashboard/keys", loc)
	}
	if !sawDelete {
		t.Fatal("API DELETE was not called")
	}
	if gotAuth != "Bearer session-bearer" {
		t.Fatalf("Authorization = %q, want Bearer session-bearer", gotAuth)
	}
}

// keysCreateRig is the dashboard's mint route over a stub API whose
// POST /v1/keys runs answer; it returns the handler and a session cookie.
func keysCreateRig(t *testing.T, answer http.HandlerFunc) (http.Handler, *http.Cookie) {
	t.Helper()
	apiMux := http.NewServeMux()
	apiMux.HandleFunc("POST /v1/keys", answer)
	apiSrv := httptest.NewServer(apiMux)
	t.Cleanup(apiSrv.Close)
	apiClient, err := apiclient.NewSessionAware(apiSrv.URL, 0)
	if err != nil {
		t.Fatalf("NewSessionAware: %v", err)
	}
	mgr := newTestSessionManager(t)
	dashMux := http.NewServeMux()
	handlers.NewDashboardHandler(apiClient, mgr, nil, nil).Register(dashMux)
	issue := httptest.NewRecorder()
	if err := mgr.Issue(issue, session.Session{Subject: "alice", Bearer: "session-bearer", Email: "alice@example.com",
		ExpiresAt: time.Now().UTC().Add(time.Hour)}); err != nil {
		t.Fatalf("Issue: %v", err)
	}
	return handlers.RequireSession(mgr)(dashMux), issue.Result().Cookies()[0]
}

func postKeyForm(h http.Handler, c *http.Cookie, form url.Values) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/dashboard/keys", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(c)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// The Create form's kind decides the roles the API is asked for: a
// read-only account key is the viewer role; an agent or service key, the
// default, asks for none.
func TestDashboard_CreateKeySendsTheKindsRoles(t *testing.T) {
	t.Parallel()
	var got [][]string
	h, c := keysCreateRig(t, func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Roles []string `json:"roles"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode: %v", err)
		}
		got = append(got, payload.Roles)
		writeJSONForTest(w, http.StatusOK, map[string]any{"id": "key-1", "label": "qa", "plaintext": "kiff_live_x"})
	})
	for _, kind := range []string{"read-only", "agent", ""} {
		form := url.Values{"label": {"qa"}}
		if kind != "" {
			form.Set("kind", kind)
		}
		if rec := postKeyForm(h, c, form); rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/dashboard/keys" {
			t.Fatalf("kind %q: %d → %q", kind, rec.Code, rec.Header().Get("Location"))
		}
	}
	if len(got) != 3 || strings.Join(got[0], ",") != "viewer" || len(got[1]) != 0 || len(got[2]) != 0 {
		t.Fatalf("roles sent = %q, want [viewer] then none, none", got)
	}
}

// A read-only account key the API refuses for want of a recent sign-in
// lands back on API keys saying so, with the way to sign in again.
func TestDashboard_CreateReadOnlyKeyNeedsARecentSignIn(t *testing.T) {
	t.Parallel()
	h, c := keysCreateRig(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSONForTest(w, http.StatusForbidden, map[string]string{"error": "reauth_required",
			"message": "this change needs a recent sign-in; sign in again and retry within ten minutes"})
	})
	flash := followFlashTo(t, h, c, postKeyForm(h, c, url.Values{"label": {"qa"}, "kind": {"read-only"}}), "/dashboard/keys")
	if !strings.Contains(flash, string(session.FlashKeyReauth)) || !strings.Contains(flash, "needs a recent sign-in") {
		t.Fatalf("flash = %s, want the sign-in-again notice", flash)
	}
	body := renderKeys(t, pages.DashboardView{
		Me:    &apiclient.Me{Subject: "s", TenantID: "t", TenantSlug: "tenant"},
		Flash: &pages.DashboardFlash{Kind: string(session.FlashKeyReauth), Message: "A read-only account key needs a recent sign-in."},
	})
	if !strings.Contains(body, "Sign in again first.") || !strings.Contains(body, `href="/sign-out?next=%2Fdashboard%2Fkeys"`) {
		t.Fatalf("sign-in-again notice missing\n---\n%s", body)
	}
}

// The Create form offers both kinds, the agent or service key first and
// chosen; the list says what each key is.
func TestDashboard_KeysPageShowsKinds(t *testing.T) {
	t.Parallel()
	v := freshDashboardView()
	v.Keys = []apiclient.KeyListItem{
		{ID: "key-agent", Label: "a", AgentID: "a", Roles: []string{"viewer"}, CreatedAt: "2026-10-01T10:00:00Z"},
		{ID: "key-reader", Label: "qa", Roles: []string{"viewer"}, CreatedAt: "2026-10-01T10:00:00Z"},
		{ID: "key-admin", Label: "ops", Roles: []string{"admin"}, CreatedAt: "2026-10-01T10:00:00Z"},
		{ID: "key-svc", Label: "svc", CreatedAt: "2026-10-01T10:00:00Z"},
		{ID: "key-conn", Label: "c", Roles: []string{"connector"}, CreatedAt: "2026-10-01T10:00:00Z"},
	}
	v.KeyCount = len(v.Keys)
	body := renderKeys(t, v)
	for _, want := range []string{
		`name="kind" value="agent" checked`, `name="kind" value="read-only"`, "Read-only account key",
		">Kind</th>", ">Read-only account<", ">Account · admin<", ">Service<", ">Connector<",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("keys page missing %q", want)
		}
	}
	// viewer on an agent's key is still that agent's key.
	if strings.Count(body, `<td class="dash-keys-kind">Agent</td>`) != 1 {
		t.Error("the agent's key with viewer is not shown as an Agent key")
	}
}

func TestDashboard_KeyRevokedFlashRenders(t *testing.T) {
	t.Parallel()
	body := renderKeys(t, pages.DashboardView{
		Me: &apiclient.Me{Subject: "s", TenantID: "t", TenantSlug: "tenant"},
		Flash: &pages.DashboardFlash{
			Kind:    string(session.FlashKeyRevoked),
			Message: "API key revoked: key-123",
		},
	})
	if !strings.Contains(body, "Key revoked.") || !strings.Contains(body, "API key revoked: key-123") {
		t.Fatalf("revoked flash missing\n---\n%s", body)
	}
}

// The Create form's expiry is sent as expires_at: a number of days from
// now, or nothing for the cloud's default.
func TestDashboard_CreateKeySendsTheChosenExpiry(t *testing.T) {
	t.Parallel()
	var got []string
	h, c := keysCreateRig(t, func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			ExpiresAt string `json:"expires_at"`
		}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		got = append(got, payload.ExpiresAt)
		writeJSONForTest(w, http.StatusOK, map[string]any{"id": "key-1", "label": "qa", "plaintext": "kiff_live_x"})
	})
	before := time.Now()
	for _, choice := range []string{"30", ""} {
		postKeyForm(h, c, url.Values{"label": {"qa"}, "kind": {"read-only"}, "expires": {choice}})
	}
	if len(got) != 2 || got[1] != "" {
		t.Fatalf("expires_at sent = %q, want one for 30 days then none", got)
	}
	at, err := time.Parse(time.RFC3339, got[0])
	if err != nil || at.Before(before.Add(30*24*time.Hour-time.Minute)) || at.After(time.Now().Add(30*24*time.Hour+time.Minute)) {
		t.Fatalf("30 days sent as %q (%v)", got[0], err)
	}
}

// The list says when each key stops working, and the form offers the
// choice with the cloud's default first.
func TestDashboard_KeysPageShowsExpiry(t *testing.T) {
	t.Parallel()
	in := func(d time.Duration) *string { s := time.Now().Add(d).UTC().Format(time.RFC3339); return &s }
	v := freshDashboardView()
	v.Keys = []apiclient.KeyListItem{
		{ID: "key-never", Label: "svc", CreatedAt: "2026-10-01T10:00:00Z"},
		{ID: "key-soon", Label: "qa", Roles: []string{"viewer"}, CreatedAt: "2026-10-01T10:00:00Z", ExpiresAt: in(89*24*time.Hour + time.Hour)},
		{ID: "key-tomorrow", Label: "t", Roles: []string{"viewer"}, CreatedAt: "2026-10-01T10:00:00Z", ExpiresAt: in(25 * time.Hour)},
		{ID: "key-gone", Label: "old", Roles: []string{"admin"}, CreatedAt: "2026-10-01T10:00:00Z", ExpiresAt: in(-time.Hour)},
		{ID: "key-today", Label: "now", Roles: []string{"viewer"}, CreatedAt: "2026-10-01T10:00:00Z", ExpiresAt: in(3 * time.Hour)},
	}
	v.KeyCount = len(v.Keys)
	body := renderKeys(t, v)
	for _, want := range []string{">Expires</th>", `dash-keys-expires">Never<`, `dash-keys-expires">In 89 days<`,
		`dash-keys-expires">In 1 day<`, `dash-keys-expires">Today<`, `dash-keys-expires">Expired<`, `name="expires"`, `<option value="" selected>Default</option>`, "a read-only key expires in 90 days; an agent or service key never does"} {
		if !strings.Contains(body, want) {
			t.Errorf("keys page missing %q", want)
		}
	}
}
