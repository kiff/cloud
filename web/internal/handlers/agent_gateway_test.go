package handlers_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/web/internal/apiclient"
	"github.com/kiff/cloud/web/internal/gatewayclient"
	"github.com/kiff/cloud/web/internal/handlers"
	"github.com/kiff/cloud/web/internal/session"
)

// gatewayAgentRig is agentsRig with a gateway configured.
func gatewayAgentRig(t *testing.T, api *agentsAPI, withGateway bool) (http.Handler, *http.Cookie) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(api.serve))
	t.Cleanup(srv.Close)
	client, err := apiclient.NewSessionAware(srv.URL, 0)
	if err != nil {
		t.Fatal(err)
	}
	mgr := newTestSessionManager(t)
	dash := handlers.NewDashboardHandler(client, mgr, handlers.StaticBearerSource(), nil)
	if withGateway {
		gw, err := gatewayclient.New("http://127.0.0.1:1")
		if err != nil {
			t.Fatal(err)
		}
		dash.SetGateway(gw)
		dash.PublicGatewayURL = "https://mcp.example.test/mcp"
	}
	mux := http.NewServeMux()
	dash.Register(mux)
	issue := httptest.NewRecorder()
	if err := mgr.Issue(issue, session.Session{Subject: "alice", Bearer: "b", Email: "a@example.com", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	return handlers.RequireSession(mgr)(mux), issue.Result().Cookies()[0]
}

// follow renders the redirect target with the flash the redirect set.
func follow(t *testing.T, h http.Handler, c *http.Cookie, rec *httptest.ResponseRecorder) *httptest.ResponseRecorder {
	t.Helper()
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", rec.Code)
	}
	// A browser does not send the fragment.
	loc, _, _ := strings.Cut(rec.Header().Get("Location"), "#")
	req := httptest.NewRequest(http.MethodGet, loc, nil)
	req.AddCookie(c)
	for _, ck := range rec.Result().Cookies() {
		if ck.Name == session.FlashCookieName {
			req.AddCookie(ck)
		}
	}
	out := httptest.NewRecorder()
	h.ServeHTTP(out, req)
	return out
}

// the cards-for-any-agent design: connecting an agent mints one key
// bound to it holding only gateway_agent, and shows it once inside the
// Claude Code and Codex configuration for the public gateway URL.
func TestAgentGateway_KeyShownOnceInItsConfig(t *testing.T) {
	t.Parallel()
	api := &agentsAPI{}
	h, c := gatewayAgentRig(t, api, true)

	// The Tools tab's row opens the sheet that makes the key.
	if tab := send(h, c, http.MethodGet, "/agents/refund-agent/tools").Body.String(); !strings.Contains(tab, `href="/agents/refund-agent/tools/gateway"`) {
		t.Error("tools tab has no row to connect Claude Code or Codex")
	}
	before := send(h, c, http.MethodGet, "/agents/refund-agent/tools/gateway").Body.String()
	for _, want := range []string{`action="/agents/refund-agent/gateway-key"`, "Connect Claude Code or Codex", "https://mcp.example.test/mcp", "Only an admin"} {
		if !strings.Contains(before, want) {
			t.Errorf("gateway sheet missing %q", want)
		}
	}

	rec := postForm(h, c, "/agents/refund-agent/gateway-key", nil)
	if loc := rec.Header().Get("Location"); loc != "/agents/refund-agent/tools#gateway" {
		t.Errorf("redirect = %q, want the gateway section", loc)
	}
	page := follow(t, h, c, rec)
	if len(api.keyReqs) != 1 {
		t.Fatalf("key requests = %v", api.keyReqs)
	}
	req := api.keyReqs[0]
	roles, _ := json.Marshal(req["roles"])
	if req["agent_id"] != "refund-agent" || string(roles) != `["gateway_agent"]` {
		t.Fatalf("key request = %v, want bound to refund-agent with only gateway_agent", req)
	}
	body := page.Body.String()
	// The key alone first, copyable beside it; then each format's block
	// with a button that names what it copies.
	if strings.Count(body, `id="gateway-key">kiff_live_agent_plaintext</code>`) != 1 {
		t.Error("the key isn't shown on its own row")
	}
	for _, want := range []string{
		`data-copy-target="gateway-key">Copy key</button>`,
		`data-copy-target="gateway-claude">Copy .mcp.json</button>`,
		`data-copy-target="gateway-codex">Copy config.toml</button>`,
		`data-copy-target="gateway-codex-env">Copy env line</button>`,
		`data-copy-target="gateway-env">Copy export line</button>`,
		`data-copy-target="gateway-curl">Copy curl</button>`,
		`name="gw-format" value="claude" id="gw-f-claude"`,
		"Shown once. Copy it now.",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("reveal missing %q", want)
		}
	}
	for _, want := range []string{
		"Gateway key created",
		`&#34;url&#34;: &#34;https://mcp.example.test/mcp&#34;`,
		`&#34;Authorization&#34;: &#34;Bearer kiff_live_agent_plaintext&#34;`,
		`bearer_token_env_var = &#34;KIFF_GATEWAY_KEY&#34;`,
		"export KIFF_GATEWAY_KEY=kiff_live_agent_plaintext",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("reveal missing %q", want)
		}
	}
	if page.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("the page with the key is cacheable: %q", page.Header().Get("Cache-Control"))
	}
	if strings.Contains(body, "API key created") {
		t.Error("the generic key banner is shown as well")
	}
	// Once: the next load does not show it.
	if again := send(h, c, http.MethodGet, "/agents/refund-agent/tools").Body.String(); strings.Contains(again, "kiff_live_agent_plaintext") {
		t.Error("the key is shown a second time")
	}
}

// D8: only an admin can mint gateway_agent; anyone else is told why and
// no key is created.
func TestAgentGateway_NonAdminIsToldWhy(t *testing.T) {
	t.Parallel()
	api := &agentsAPI{notAdmin: true}
	h, c := gatewayAgentRig(t, api, true)
	body := follow(t, h, c, postForm(h, c, "/agents/refund-agent/gateway-key", nil)).Body.String()
	if !strings.Contains(body, "Only an admin of this account can connect an agent to the KIFF gateway") || len(api.keyReqs) != 0 {
		t.Fatalf("non-admin: keys %v\n%s", api.keyReqs, body)
	}
}

func TestAgentGateway_HiddenWithoutAGateway(t *testing.T) {
	t.Parallel()
	api := &agentsAPI{}
	h, c := gatewayAgentRig(t, api, false)
	if body := send(h, c, http.MethodGet, "/agents/refund-agent/tools").Body.String(); strings.Contains(body, "gateway-key") {
		t.Error("the gateway section is shown without a gateway")
	}
	body := follow(t, h, c, postForm(h, c, "/agents/refund-agent/gateway-key", nil)).Body.String()
	if !strings.Contains(body, "cannot be connected to the KIFF gateway from this deployment") || len(api.keyReqs) != 0 {
		t.Fatalf("without gateway: keys %v", api.keyReqs)
	}
}

// the gateway-OAuth design: the Tools tab gives each live connect
// link's command; creating and revoking go to the API.
func TestAgentConnectLinks(t *testing.T) {
	t.Parallel()
	api := &agentsAPI{}
	h, c := gatewayAgentRig(t, api, true)
	if tab := send(h, c, http.MethodGet, "/agents/refund-agent/tools").Body.String(); !strings.Contains(tab, "1 link") {
		t.Error("tools tab does not count the live connect link")
	}
	req := httptest.NewRequest(http.MethodGet, "/agents/refund-agent/tools/link", nil)
	req.AddCookie(c)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	body := rec.Body.String()
	for _, want := range []string{"Connect an app with sign-in", `action="/agents/refund-agent/connect-links"`,
		"claude mcp add --transport http kiff https://mcp.example.test/a/cl_live1234567/mcp",
		`formaction="/agents/refund-agent/connect-links/cl_live1234567/revoke"`, "not a secret"} {
		if !strings.Contains(body, want) {
			t.Errorf("link sheet missing %q", want)
		}
	}
	if strings.Contains(body, "cl_gone1234567") {
		t.Error("a revoked link is offered")
	}
	if rec := postForm(h, c, "/agents/refund-agent/connect-links", nil); rec.Code != http.StatusSeeOther || len(api.linksCreated) != 1 || api.linksCreated[0] != "refund-agent" ||
		rec.Header().Get("Location") != "/agents/refund-agent/tools/link" {
		t.Fatalf("create: %d %v", rec.Code, api.linksCreated)
	}
	if rec := postForm(h, c, "/agents/refund-agent/connect-links/cl_live1234567/revoke", nil); rec.Code != http.StatusSeeOther || len(api.linksRevoked) != 1 || api.linksRevoked[0] != "cl_live1234567" {
		t.Fatalf("revoke: %d %v", rec.Code, api.linksRevoked)
	}
}

// the gateway-OAuth design: one list of every way an agent gets in; a
// grant's own key is not listed twice; revoke goes to the API.
func TestConnectionsPage(t *testing.T) {
	t.Parallel()
	api := &agentsAPI{gatewayKeys: true}
	h, c := gatewayAgentRig(t, api, true)
	req := httptest.NewRequest(http.MethodGet, "/connections", nil)
	req.AddCookie(c)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	body := rec.Body.String()
	for _, want := range []string{"Connections", "Claude Code", "Sign-in", "kiff-cli", "Code", "Ended (link gone)",
		"Gateway key for refund-agent", ">Key<", `action="/connections/grants/kg_live/revoke"`,
		`action="/dashboard/keys/k-gw/revoke"`, `value="/connections"`} {
		if !strings.Contains(body, want) {
			t.Errorf("connections page missing %q", want)
		}
	}
	if strings.Contains(body, "/dashboard/keys/k-grant/revoke") {
		t.Error("a connection's own key is listed as a separate key")
	}
	if strings.Contains(body, "/connections/grants/kg_cli/revoke") {
		t.Error("an ended connection offers Revoke")
	}
	if rec := postForm(h, c, "/connections/grants/kg_live/revoke", nil); rec.Code != http.StatusSeeOther || len(api.linksRevoked) != 1 || api.linksRevoked[0] != "kg_live" {
		t.Fatalf("revoke: %d %v", rec.Code, api.linksRevoked)
	}
}
