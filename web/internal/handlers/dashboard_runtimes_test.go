package handlers_test

import (
	"bytes"
	"context"
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

func renderRuntimes(t *testing.T, v pages.DashboardView) string {
	t.Helper()
	var buf bytes.Buffer
	if err := pages.DashboardRuntimes(v).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render runtimes: %v", err)
	}
	return buf.String()
}

// TestRuntimes_ScopesNavToCurrentDomain asserts that a non-domain tab
// (Runtimes) scopes its sidebar Govern link to the remembered domain
// when one is set — so a Runtimes → Govern hop stays on the same domain
// instead of flipping Govern between "all domains" and "one domain".
func TestRuntimes_ScopesNavToCurrentDomain(t *testing.T) {
	t.Parallel()
	v := freshDashboardView()
	v.CurrentDomain = "prod-guardian"
	body := renderRuntimes(t, v)

	if !strings.Contains(body, `href="/dashboard/govern?domain=prod-guardian"`) {
		t.Errorf("Runtimes should scope the Govern nav link to the current domain\n---\n%s", body)
	}
	// And without a current domain, the link stays unscoped (picker).
	if body := renderRuntimes(t, freshDashboardView()); !strings.Contains(body, `href="/dashboard/govern"`) {
		t.Errorf("Runtimes with no current domain should leave Govern unscoped\n---\n%s", body)
	}
}

// TestRuntimes_EmptyState asserts the runtimes page renders the
// install-the-guard empty state (and no table) when the tenant
// has no connected runtimes.
func TestRuntimes_EmptyState(t *testing.T) {
	t.Parallel()
	body := renderRuntimes(t, freshDashboardView())

	if !strings.Contains(body, "Connect an agent") {
		t.Error("runtimes page missing the Connect panel")
	}
	if !strings.Contains(body, "No runtimes yet") {
		t.Error("runtimes page missing the empty-state copy")
	}
	if strings.Contains(body, `<table class="dash-table">`) {
		t.Error("runtimes page rendered a table despite having no connections")
	}
	// Breadcrumb-style eyebrow (Workspace · Runtimes).
	if !strings.Contains(body, `<p class="eyebrow">Workspace · Runtimes</p>`) {
		t.Error("runtimes page missing the Workspace · Runtimes eyebrow header")
	}
}

func TestRuntimes_ConnectPanelRendersEndpointKeyFormAndSnippet(t *testing.T) {
	t.Parallel()
	v := freshDashboardView()
	v.APIBaseURL = "http://127.0.0.1:8082"
	v.KeyCount = 1

	body := renderRuntimes(t, v)

	for _, want := range []string{
		"Connect an agent",
		`<code>http://127.0.0.1:8082</code>`,
		`action="/dashboard/keys"`,
		`name="label" value="connect"`,
		`name="next" value="/dashboard/runtimes"`,
		`Create connect key`,
		`/v1/proposals/decide`,
		`action_name`,
		`refund_order`,
		`kiff_live_...`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("Connect panel missing %q", want)
		}
	}
}

func TestRuntimes_ConnectPanelLanguageSnippet(t *testing.T) {
	t.Parallel()
	v := freshDashboardView()
	v.APIBaseURL = "http://127.0.0.1:8082"
	v.ConnectSnippet = "python"

	body := renderRuntimes(t, v)

	for _, want := range []string{
		`dash-snippet-tab dash-snippet-tab-active`,
		`requests.post`,
		`http://127.0.0.1:8082`,
		`approval_required`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("Python snippet missing %q", want)
		}
	}
}

// TestRuntimes_RendersRows asserts the table renders one row per
// connection with the status/runtime/mode/adapter columns.
func TestRuntimes_RendersRows(t *testing.T) {
	t.Parallel()
	v := freshDashboardView()
	now := time.Now().UTC()
	v.GuardConnections = []apiclient.GuardConnection{
		{
			Project:     "cookbook",
			Environment: "aws",
			AgentID:     "agent-1",
			Workflow:    "duplicate-payment",
			Adapter:     "openclaw",
			SDKVersion:  "0.1.0",
			Mode:        "enforce",
			LastSeenAt:  now, // live
			SeenCount:   42,
		},
		{
			Project:     "cookbook",
			Environment: "staging",
			AgentID:     "agent-2",
			Workflow:    "refund",
			Adapter:     "langchain",
			Mode:        "observe",
			LastSeenAt:  now.Add(-72 * time.Hour), // idle
			SeenCount:   3,
		},
	}

	body := renderRuntimes(t, v)

	mustContain := []string{
		`<th>Status</th>`,
		`<th>Runtime</th>`,
		`<th>Mode</th>`,
		`<th>Adapter</th>`,
		"duplicate-payment",
		"openclaw",
		"enforce",
		"observe",
		"live",  // recent LastSeenAt
		"idle",  // 3-days-stale LastSeenAt
		"0.1.0", // sdk version
	}
	for _, w := range mustContain {
		if !strings.Contains(body, w) {
			t.Errorf("runtimes table missing %q", w)
		}
	}
}

// TestRuntimes_RevokeKeyAction asserts each runtime with a stamped key id
// renders an inline "revoke this key" form (the control lever, since the
// guard API has no per-runtime mutation), and a runtime without a key id
// renders no form.
func TestRuntimes_RevokeKeyAction(t *testing.T) {
	t.Parallel()
	v := freshDashboardView()
	now := time.Now().UTC()
	v.GuardConnections = []apiclient.GuardConnection{
		{
			Project: "cookbook", AgentID: "agent-keyed", Adapter: "openclaw",
			Mode: "enforce", KeyID: "key-xyz", LastSeenAt: now, SeenCount: 9,
		},
		{
			Project: "cookbook", AgentID: "agent-jwt", Adapter: "openclaw",
			Mode: "observe", KeyID: "", LastSeenAt: now, SeenCount: 1,
		},
	}

	body := renderRuntimes(t, v)

	for _, want := range []string{
		`<th>Actions</th>`,
		`action="/dashboard/keys/key-xyz/revoke"`,
		`name="next" value="/dashboard/runtimes"`,
		`>Revoke key</button>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("runtime revoke action missing %q\n---\n%s", want, body)
		}
	}
	// The JWT-authed runtime (no key id) must not render a revoke form.
	if strings.Count(body, "/revoke") != 1 {
		t.Errorf("exactly one revoke form expected (only the keyed runtime); got %d", strings.Count(body, "/revoke"))
	}
}

// TestRuntimes_RevokeKeyReturnsToRuntimes asserts the shared revoke
// handler, when posted from the runtimes page (next=/dashboard/runtimes),
// calls DELETE /v1/keys/{id} and redirects back to the runtimes page
// rather than the keys page.
func TestRuntimes_RevokeKeyReturnsToRuntimes(t *testing.T) {
	t.Parallel()

	var deletedID string
	apiMux := http.NewServeMux()
	apiMux.HandleFunc("DELETE /v1/keys/{id}", func(w http.ResponseWriter, r *http.Request) {
		deletedID = r.PathValue("id")
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
		Subject: "alice", Bearer: "session-bearer", Email: "alice@example.com",
		ExpiresAt: time.Now().UTC().Add(time.Hour),
	}); err != nil {
		t.Fatalf("Issue: %v", err)
	}

	form := url.Values{}
	form.Set("next", "/dashboard/runtimes")
	req := httptest.NewRequest(http.MethodPost, "/dashboard/keys/key-xyz/revoke", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(issue.Result().Cookies()[0])
	rec := httptest.NewRecorder()
	protected.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303 (body=%q)", rec.Code, rec.Body.String())
	}
	if loc := rec.Header().Get("Location"); loc != "/dashboard/runtimes" {
		t.Fatalf("Location = %q, want /dashboard/runtimes", loc)
	}
	if deletedID != "key-xyz" {
		t.Fatalf("deleted key id = %q, want key-xyz", deletedID)
	}
}

// TestSettings_NoAPIKeysOrRuntimesBoxes asserts Settings no longer
// duplicates the API keys / Runtimes surfaces — those live in the
// Workspace nav group now, so Settings stays focused on account,
// workspace, consumption, and the danger zone.
func TestSettings_NoAPIKeysOrRuntimesBoxes(t *testing.T) {
	t.Parallel()
	v := freshDashboardView()
	v.KeyCount = 2

	body := renderSettings(t, v)

	for _, gone := range []string{
		`<h2>API keys</h2>`,
		`Manage API keys`,
		`<h2>Runtimes</h2>`,
	} {
		if strings.Contains(body, gone) {
			t.Errorf("Settings should no longer carry %q (moved to Workspace nav)", gone)
		}
	}
	// The account-level surfaces it should keep.
	for _, keep := range []string{`<h2>Account</h2>`, `<h2>Workspace</h2>`, `<h2>Danger zone</h2>`} {
		if !strings.Contains(body, keep) {
			t.Errorf("Settings missing %q", keep)
		}
	}
}

// TestRuntimes_WorkspaceWideInventory asserts the runtimes list is
// presented as a workspace-wide inventory, not falsely scoped under the
// currently-selected domain. Guard connections are tenant-scoped, so a
// runtime that proposes against another domain must not be labeled as
// "within" whatever domain the nav happens to point at.
func TestRuntimes_WorkspaceWideInventory(t *testing.T) {
	t.Parallel()
	v := freshDashboardView()
	// Even with a domain selected in the nav, the inventory stays
	// workspace-wide (the previous behavior mislabeled it under this).
	v.Domain = &apiclient.Domain{Domain: "duplicate-payment", Entity: "Invoice"}
	v.GuardConnections = []apiclient.GuardConnection{{
		Project: "cookbook", Environment: "aws", AgentID: "agent-1",
		Workflow: "pay", Adapter: "openclaw", Mode: "enforce",
		LastSeenAt: time.Now().UTC(), SeenCount: 5,
	}}

	body := renderRuntimes(t, v)

	for _, w := range []string{
		"Connected runtimes",          // workspace-wide header
		"connected to this workspace", // workspace-wide copy
		`<table class="dash-table">`,
		"agent-1",
	} {
		if !strings.Contains(body, w) {
			t.Errorf("workspace-wide runtimes missing %q", w)
		}
	}
	// The old, misleading single-domain framing is gone.
	if strings.Contains(body, "operating within this domain") {
		t.Error("runtimes still use the misleading 'within this domain' framing")
	}
	if strings.Contains(body, "<h2>duplicate-payment</h2>") {
		t.Error("runtimes still head the list with the selected domain name")
	}
}

// TestRuntimes_WorkspaceWideWithoutDomain asserts the same workspace-wide
// framing holds when no domain is selected — no "No active domain" group.
func TestRuntimes_WorkspaceWideWithoutDomain(t *testing.T) {
	t.Parallel()
	v := freshDashboardView() // no Domain set
	v.GuardConnections = []apiclient.GuardConnection{{
		Project: "cookbook", AgentID: "agent-1", Adapter: "openclaw",
		Mode: "observe", LastSeenAt: time.Now().UTC(), SeenCount: 1,
	}}

	body := renderRuntimes(t, v)

	if !strings.Contains(body, "Connected runtimes") || !strings.Contains(body, "connected to this workspace") {
		t.Error("runtimes should render as a workspace-wide inventory")
	}
	if strings.Contains(body, "No active domain") {
		t.Error("the misleading 'No active domain' group should be gone")
	}
	if !strings.Contains(body, `<table class="dash-table">`) {
		t.Error("the runtimes table should still render")
	}
}

// TestRuntimes_AIAssistantConnectPrompt asserts the connect panel offers
// an AI-assistant tab whose snippet is a paste-into-your-assistant prompt
// grounded in the decide route and the outcome contract.
func TestRuntimes_AIAssistantConnectPrompt(t *testing.T) {
	t.Parallel()
	v := freshDashboardView()
	v.APIBaseURL = "https://api.kiff.dev"
	v.ConnectSnippet = "assistant"

	body := renderRuntimes(t, v)

	for _, w := range []string{
		"AI assistant",                          // the tab
		"Paste this into your coding assistant", // the hint
		"/v1/proposals/decide",                  // grounded route
		"outcome",                               // the contract
	} {
		if !strings.Contains(body, w) {
			t.Errorf("assistant connect prompt missing %q", w)
		}
	}
}

func TestRuntimes_MintConnectKeyReturnsToRuntimes(t *testing.T) {
	t.Parallel()

	var gotLabel string
	apiMux := http.NewServeMux()
	apiMux.HandleFunc("POST /v1/keys", func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Label string `json:"label"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode key payload: %v", err)
		}
		gotLabel = payload.Label
		writeJSONForTest(w, http.StatusOK, map[string]any{
			"id":        "key-connect",
			"tenant":    "ten-test",
			"subject":   "alice",
			"label":     payload.Label,
			"plaintext": "kiff_live_test_secret",
		})
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
		Subject:   "alice",
		Bearer:    "session-bearer",
		Email:     "alice@example.com",
		ExpiresAt: time.Now().UTC().Add(time.Hour),
	}); err != nil {
		t.Fatalf("Issue: %v", err)
	}

	form := url.Values{}
	form.Set("label", "connect")
	form.Set("next", "/dashboard/runtimes")
	req := httptest.NewRequest(http.MethodPost, "/dashboard/keys", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(issue.Result().Cookies()[0])
	rec := httptest.NewRecorder()
	protected.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303 (body=%q)", rec.Code, rec.Body.String())
	}
	if loc := rec.Header().Get("Location"); loc != "/dashboard/runtimes" {
		t.Fatalf("Location = %q, want /dashboard/runtimes", loc)
	}
	if gotLabel != "connect" {
		t.Fatalf("label = %q, want connect", gotLabel)
	}
	if len(rec.Result().Cookies()) == 0 {
		t.Fatal("expected a flash cookie with the minted key")
	}
}
