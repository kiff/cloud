package handlers_test

import (
	"encoding/json"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kiff/cloud/web/internal/apiclient"
	"github.com/kiff/cloud/web/internal/handlers"
	"github.com/kiff/cloud/web/internal/session"
)

// agentsAPI stubs the API the Agents pages join.
type agentsAPI struct {
	mu      sync.Mutex
	created []string
	keyReqs []map[string]any
	// crowded serves a full page of 100 decisions by other agents.
	crowded bool
	// proposalsFail makes the recent-decisions read fail.
	proposalsFail bool
	// staleRuntime keeps a recorded heartbeat but no live guard.
	staleRuntime bool
	// notAdmin refuses minting a key that holds a role (403), as the API
	// does for a caller who is not admin.
	notAdmin bool
	// Connect links: created for agents, revoked ids.
	linksCreated []string
	linksRevoked []string
	// gatewayKeys serves gateway keys on /v1/keys (the Connections test).
	gatewayKeys bool
	// keyRevoked lists no keys: the key behind refund-agent's last
	// request has been revoked.
	keyRevoked bool
	// twoKeys: refund-agent called with kA, then with kB a minute
	// later; kB has since been revoked and kA is still active.
	twoKeys bool
	// crowdedRevoked: a full page of other agents' decisions holding one
	// request from refund-agent's since-revoked key kB; its active key's
	// requests, if any, are beyond the page. refund-agent has no guard.
	crowdedRevoked bool
}

func (a *agentsAPI) serve(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := time.Now().UTC()
	switch {
	case r.URL.Path == "/v1/me":
		writeJSONForTest(w, http.StatusOK, map[string]any{"subject": "alice", "tenant_id": "t1"})
	case r.URL.Path == "/v1/me/agents" && r.Method == http.MethodGet:
		archived := now.Add(-time.Hour)
		writeJSONForTest(w, http.StatusOK, map[string]any{"agents": []any{
			map[string]any{"id": "refund-agent", "recorded": true, "sources": []string{"record", "card", "key", "runtime"}},
			map[string]any{"id": "watch-agent", "sources": []string{"runtime"}},
			map[string]any{"id": "new-agent", "recorded": true, "sources": []string{"record"}},
			map[string]any{"id": "gone-agent", "recorded": true, "archived_at": archived, "sources": []string{"record"}},
		}})
	case r.URL.Path == "/v1/me/agents" && r.Method == http.MethodPost:
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["id"] == "refund-agent" {
			writeJSONForTest(w, http.StatusConflict, map[string]any{"error": "exists"})
			return
		}
		a.created = append(a.created, body["id"])
		writeJSONForTest(w, http.StatusCreated, map[string]any{"id": body["id"], "recorded": true, "sources": []string{"record"}})
	case r.URL.Path == "/v1/me/mandates":
		writeJSONForTest(w, http.StatusOK, map[string]any{"mandates": []any{
			map[string]any{"id": "refund-agent-refunds", "subject": "refund-agent", "domain": "card-refund",
				"grants": []any{map[string]any{"action": "REFUND_ORDER"}}},
		}})
	case r.URL.Path == "/v1/keys" && r.Method == http.MethodGet && a.keyRevoked:
		writeJSONForTest(w, http.StatusOK, map[string]any{"keys": []any{}})
	case r.URL.Path == "/v1/keys" && r.Method == http.MethodGet && a.twoKeys:
		writeJSONForTest(w, http.StatusOK, map[string]any{"keys": []any{
			map[string]any{"id": "kA", "label": "first key", "agent_id": "refund-agent", "created_at": now.Format(time.RFC3339)},
		}})
	case r.URL.Path == "/v1/keys" && r.Method == http.MethodGet && a.gatewayKeys:
		writeJSONForTest(w, http.StatusOK, map[string]any{"keys": []any{
			map[string]any{"id": "k-gw", "label": "Gateway key for refund-agent", "agent_id": "refund-agent", "roles": []string{"gateway_agent"},
				"subject": "alice", "created_at": now.Format(time.RFC3339)},
			map[string]any{"id": "k-grant", "label": "Claude Code (connected with sign-in)", "agent_id": "refund-agent", "roles": []string{"gateway_agent"},
				"created_at": now.Format(time.RFC3339)},
		}})
	case r.URL.Path == "/v1/keys" && r.Method == http.MethodGet:
		writeJSONForTest(w, http.StatusOK, map[string]any{"keys": []any{
			map[string]any{"id": "k1", "label": "refund runtime", "agent_id": "refund-agent", "roles": []string{"support"},
				"created_at": now.Format(time.RFC3339)},
		}})
	case r.URL.Path == "/v1/keys" && r.Method == http.MethodPost:
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if a.notAdmin && body["roles"] != nil {
			writeJSONForTest(w, http.StatusForbidden, map[string]any{"error": "a key can only grant roles its creator holds"})
			return
		}
		a.keyReqs = append(a.keyReqs, body)
		writeJSONForTest(w, http.StatusCreated, map[string]any{"id": "k-new", "label": body["label"], "plaintext": "kiff_live_agent_plaintext"})
	case r.URL.Path == "/v1/guard/connections":
		refundLastSeen := now.Add(-time.Minute)
		if a.staleRuntime {
			refundLastSeen = now.Add(-2 * time.Hour)
		}
		if a.crowdedRevoked {
			writeJSONForTest(w, http.StatusOK, map[string]any{"connections": []any{}})
			return
		}
		writeJSONForTest(w, http.StatusOK, map[string]any{"connections": []any{
			map[string]any{"agent_id": "refund-agent", "adapter": "agno", "sdk_version": "1.1.0", "mode": "enforce", "last_seen_at": refundLastSeen},
			map[string]any{"agent_id": "watch-agent", "adapter": "langgraph", "mode": "observe", "last_seen_at": now.Add(-2 * time.Minute)},
		}})
	case r.URL.Path == "/v1/proposals":
		if a.proposalsFail {
			writeJSONForTest(w, http.StatusInternalServerError, map[string]any{"error": "list proposals failed"})
			return
		}
		if a.crowdedRevoked {
			rows := []any{}
			for i := 0; i < 100; i++ {
				actor, key := "busy-agent", "k-busy"
				if i == 10 {
					actor, key = "refund-agent", "kB"
				}
				rows = append(rows, map[string]any{"proposal_id": "busy-" + strconv.Itoa(i), "actor_id": actor,
					"action_name": "REFUND_ORDER", "outcome": "allowed", "principal_api_key_id": key, "actor_verified": true,
					"created_at": now.Add(-time.Duration(i) * time.Second)})
			}
			writeJSONForTest(w, http.StatusOK, map[string]any{"proposals": rows})
			return
		}
		if a.crowded {
			rows := []any{}
			for i := 0; i < 100; i++ {
				rows = append(rows, map[string]any{"proposal_id": "busy-" + strconv.Itoa(i), "actor_id": "busy-agent",
					"action_name": "REFUND_ORDER", "outcome": "allowed", "principal_api_key_id": "k-busy", "actor_verified": true,
					"created_at": now.Add(-time.Duration(i) * time.Second)})
			}
			writeJSONForTest(w, http.StatusOK, map[string]any{"proposals": rows})
			return
		}
		if a.twoKeys {
			writeJSONForTest(w, http.StatusOK, map[string]any{"proposals": []any{
				map[string]any{"proposal_id": "pB", "actor_id": "refund-agent", "action_name": "REFUND_ORDER", "outcome": "allowed",
					"principal_api_key_id": "kB", "actor_verified": true, "created_at": now.Add(-time.Minute)},
				map[string]any{"proposal_id": "pA", "actor_id": "refund-agent", "action_name": "REFUND_ORDER", "outcome": "allowed",
					"principal_api_key_id": "kA", "actor_verified": true, "created_at": now.Add(-3 * time.Minute)},
			}})
			return
		}
		writeJSONForTest(w, http.StatusOK, map[string]any{"proposals": []any{
			// An unbound key claiming new-agent's name on an action with
			// no card: a claim, never new-agent's activity.
			map[string]any{"proposal_id": "claim", "actor_id": "new-agent", "action_name": "ISSUE_CREDIT", "outcome": "allowed",
				"principal_api_key_id": "k-unbound", "created_at": now.Add(-time.Minute)},
			map[string]any{"proposal_id": "p1", "actor_id": "refund-agent", "action_name": "REFUND_ORDER", "outcome": "allowed",
				"principal_api_key_id": "k1", "actor_verified": true, "created_at": now.Add(-5 * time.Minute)},
		}})
	case r.URL.Path == "/v1/me/agents/refund-agent/connect-links" && r.Method == http.MethodGet:
		writeJSONForTest(w, http.StatusOK, map[string]any{"links": []any{
			map[string]any{"id": "cl_live1234567", "agent_id": "refund-agent", "url": "https://mcp.example.test/a/cl_live1234567/mcp", "created_at": now},
			map[string]any{"id": "cl_gone1234567", "agent_id": "refund-agent", "url": "https://mcp.example.test/a/cl_gone1234567/mcp", "created_at": now, "revoked_at": now},
		}})
	case strings.HasSuffix(r.URL.Path, "/connect-links") && r.Method == http.MethodPost:
		a.linksCreated = append(a.linksCreated, strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v1/me/agents/"), "/connect-links"))
		writeJSONForTest(w, http.StatusCreated, map[string]any{"id": "cl_new1234567", "url": "https://mcp.example.test/a/cl_new1234567/mcp"})
	case r.URL.Path == "/v1/oauth/connections":
		writeJSONForTest(w, http.StatusOK, map[string]any{"connections": []any{
			map[string]any{"id": "kg_live", "agent_id": "refund-agent", "client_name": "Claude Code", "method": "sign-in",
				"key_id": "k-grant", "granted_by": "alice", "created_at": now, "last_used_at": now, "status": "live"},
			map[string]any{"id": "kg_cli", "agent_id": "refund-agent", "client_name": "kiff-cli", "method": "code",
				"key_id": "k-cli", "granted_by": "alice", "created_at": now, "last_used_at": now, "status": "ended", "ended_reason": "link_gone"},
		}})
	case strings.HasPrefix(r.URL.Path, "/v1/oauth/grants/") && r.Method == http.MethodPost:
		a.linksRevoked = append(a.linksRevoked, strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v1/oauth/grants/"), "/revoke"))
		writeJSONForTest(w, http.StatusOK, map[string]any{"revoked": true})
	case strings.HasPrefix(r.URL.Path, "/v1/oauth/connect-links/") && r.Method == http.MethodPost:
		a.linksRevoked = append(a.linksRevoked, strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v1/oauth/connect-links/"), "/revoke"))
		writeJSONForTest(w, http.StatusOK, map[string]any{"revoked": true})
	default:
		writeJSONForTest(w, http.StatusNotFound, map[string]any{"error": "not found"})
	}
}

func agentsRig(t *testing.T, api *agentsAPI) (http.Handler, *http.Cookie) {
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
	return handlers.RequireSession(mgr)(mux), issue.Result().Cookies()[0]
}

func TestAgents_ListShowsEachAgentOnceWithItsState(t *testing.T) {
	t.Parallel()
	h, c := agentsRig(t, &agentsAPI{})
	body := send(h, c, http.MethodGet, "/agents").Body.String()
	for _, want := range []string{
		`href="/agents/refund-agent"`, "Enforcing", "1 card in force", "1 key", "Allowed REFUND_ORDER",
		`href="/agents/watch-agent"`, "Observing",
		`href="/agents/new-agent"`, "Not connected",
		`href="/agents/new"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("agents list missing %q", want)
		}
	}
	if strings.Contains(body, "gone-agent") {
		t.Error("an archived agent is listed")
	}
	if !strings.Contains(body, `class="dash-nav-item dash-nav-active" href="/agents"`) {
		t.Error("Agents is not the active nav item")
	}
}

func TestAgents_DetailTabs(t *testing.T) {
	t.Parallel()
	h, c := agentsRig(t, &agentsAPI{})

	overview := send(h, c, http.MethodGet, "/agents/refund-agent").Body.String()
	for _, want := range []string{"Connected.", "Last verified request", "Via key</dt><dd><code>refund runtime</code>", "kiff-guard agno 1.1.0 in enforce mode", "Runtime details →", "support", `href="/agents/refund-agent/cards"`} {
		if !strings.Contains(overview, want) {
			t.Errorf("overview missing %q", want)
		}
	}
	if strings.Contains(overview, "Copy instruction") {
		t.Error("a connected agent should not be shown the connect instruction")
	}

	fresh := send(h, c, http.MethodGet, "/agents/new-agent").Body.String()
	for _, want := range []string{"Not connected yet.", "Getting it working", "Give it a key", "Copy instruction", "it was issued for new-agent"} {
		if !strings.Contains(fresh, want) {
			t.Errorf("unconnected agent missing %q", want)
		}
	}
	if !strings.Contains(fresh, "Omit the agent name in the guard configuration and connect request") {
		t.Error("connect instruction should use the bound key's agent identity")
	}
	if strings.Contains(fresh, "Agent name in KIFF:") {
		t.Error("connect instruction should not ask for a duplicate agent name")
	}

	if strings.Contains(overview, "What you can do now") == false {
		t.Error("a connected agent is not told what it can do next")
	}
	if !strings.Contains(fresh, `href="/agents/new-agent/keys"`) || !strings.Contains(fresh, `href="/agents/new-agent/tools"`) {
		t.Error("the Keys and Tools tabs are not linked")
	}

	cards := send(h, c, http.MethodGet, "/agents/refund-agent/cards").Body.String()
	for _, want := range []string{`href="/cards/refund-agent-refunds"`, "ac-srow", `href="/cards/new?subject=refund-agent"`, "STOP"} {
		if !strings.Contains(cards, want) {
			t.Errorf("cards tab missing %q", want)
		}
	}
	if rec := send(h, c, http.MethodGet, "/agents/nobody"); rec.Code != http.StatusNotFound {
		t.Errorf("unknown agent = %d, want 404", rec.Code)
	}
}

// A request from a key that has since been revoked does not keep the
// agent connected: the list, the page's badge and the Overview agree.
func TestAgents_RevokedKeyIsNotConnected(t *testing.T) {
	t.Parallel()
	h, c := agentsRig(t, &agentsAPI{keyRevoked: true, staleRuntime: true})

	list := send(h, c, http.MethodGet, "/agents").Body.String()
	row := list[strings.Index(list, `href="/agents/refund-agent"`):]
	row = row[:strings.Index(row, "</a>")]
	if strings.Contains(row, ">Connected<") || !strings.Contains(row, "0 keys") {
		t.Errorf("refund-agent row should show no keys and not Connected: %s", row)
	}

	overview := send(h, c, http.MethodGet, "/agents/refund-agent").Body.String()
	if strings.Contains(overview, "<strong>Connected.</strong>") {
		t.Error("the Overview says Connected for a request from a revoked key")
	}
	for _, want := range []string{"<strong>Not connected.</strong>", "since been revoked", "(revoked)"} {
		if !strings.Contains(overview, want) {
			t.Errorf("overview missing %q", want)
		}
	}
}

// The newest caller's key was revoked, but another key of the agent is
// still active and was used minutes earlier: the agent is connected.
func TestAgents_AnotherActiveKeyKeepsTheAgentConnected(t *testing.T) {
	t.Parallel()
	h, c := agentsRig(t, &agentsAPI{twoKeys: true, staleRuntime: true})

	list := send(h, c, http.MethodGet, "/agents").Body.String()
	row := list[strings.Index(list, `href="/agents/refund-agent"`):]
	row = row[:strings.Index(row, "</a>")]
	if !strings.Contains(row, ">Connected<") {
		t.Errorf("refund-agent row should be Connected: %s", row)
	}
	overview := send(h, c, http.MethodGet, "/agents/refund-agent").Body.String()
	if !strings.Contains(overview, "<strong>Connected.</strong>") || strings.Contains(overview, "<strong>Not connected.</strong>") {
		t.Error("the Overview should say Connected while another active key has recent requests")
	}
}

// A guard runtime seen within the hour leads, in the badge and the
// Overview alike; an old request from a revoked key does not override
// it, and stays visible as history.
func TestAgents_LiveRuntimeLeadsOverAnOldRevokedKey(t *testing.T) {
	t.Parallel()
	h, c := agentsRig(t, &agentsAPI{keyRevoked: true})

	overview := send(h, c, http.MethodGet, "/agents/refund-agent").Body.String()
	if !strings.Contains(overview, "Enforcing") {
		t.Error("the badge should say Enforcing for a live guard")
	}
	if strings.Contains(overview, "<strong>Not connected.</strong>") {
		t.Error("the Overview says Not connected under a live guard")
	}
	for _, want := range []string{"<strong>Running.</strong>", "(revoked)"} {
		if !strings.Contains(overview, want) {
			t.Errorf("overview missing %q", want)
		}
	}
}

// the agent-as-unit design: adding an agent records it and issues its
// key, bound to it and without roles, in one step; the key is shown
// once on the agent's page.
func TestAgents_AddIssuesTheBoundKey(t *testing.T) {
	t.Parallel()
	api := &agentsAPI{}
	h, c := agentsRig(t, api)

	post := func(path, form string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.AddCookie(c)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	rec := post("/agents", "name="+url.QueryEscape(" billing-agent "))
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/agents/billing-agent" {
		t.Fatalf("add = %d → %q", rec.Code, rec.Header().Get("Location"))
	}
	if len(api.created) != 1 || api.created[0] != "billing-agent" {
		t.Errorf("created = %v", api.created)
	}
	if len(api.keyReqs) != 1 || api.keyReqs[0]["agent_id"] != "billing-agent" || api.keyReqs[0]["roles"] != nil {
		t.Errorf("key request = %v, want agent_id billing-agent and no roles", api.keyReqs)
	}
	var flash *http.Cookie
	for _, ck := range rec.Result().Cookies() {
		if ck.Name == session.FlashCookieName {
			flash = ck
		}
	}
	if flash == nil {
		t.Fatal("no key reveal")
	}

	if rec := post("/agents", "name=refund-agent"); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "already exists") {
		t.Errorf("duplicate add = %d", rec.Code)
	}
	if rec := post("/agents", "name=+"); rec.Code != http.StatusBadRequest {
		t.Errorf("empty name = %d, want 400", rec.Code)
	}
	if len(api.keyReqs) != 1 {
		t.Errorf("a refused add minted a key: %v", api.keyReqs)
	}

	rec = post("/agents/refund-agent/key", "")
	if rec.Code != http.StatusSeeOther || len(api.keyReqs) != 2 || api.keyReqs[1]["agent_id"] != "refund-agent" {
		t.Errorf("create key for agent = %d, requests %v", rec.Code, api.keyReqs)
	}
}

// An unbound key naming an agent is a claim: it is not that agent's last
// action and does not make it look connected.
func TestAgents_UnboundKeyClaimIsNotTheAgentsActivity(t *testing.T) {
	t.Parallel()
	h, c := agentsRig(t, &agentsAPI{})
	list := send(h, c, http.MethodGet, "/agents").Body.String()
	i := strings.Index(list, `href="/agents/new-agent"`)
	if i < 0 {
		t.Fatal("new-agent missing")
	}
	row := list[i:]
	if j := strings.Index(row, "</a>"); j > 0 {
		row = row[:j]
	}
	if strings.Contains(row, "ISSUE_CREDIT") || strings.Contains(row, "Connected") {
		t.Errorf("a claimed request shows as new-agent's activity: %s", row)
	}
	if !strings.Contains(row, "Not connected") {
		t.Errorf("new-agent row = %s, want Not connected", row)
	}
	detail := send(h, c, http.MethodGet, "/agents/new-agent").Body.String()
	if strings.Contains(detail, "Connected.") {
		t.Error("the agent page counts a claimed request as a connection")
	}
}

// An agent crowded out of the tenant's newest 100 decisions is not
// "not connected": its absence from the page read is unknown.
func TestAgents_CrowdedOutAgentIsUnknownNotAbsent(t *testing.T) {
	t.Parallel()
	h, c := agentsRig(t, &agentsAPI{crowded: true})
	list := send(h, c, http.MethodGet, "/agents").Body.String()
	i := strings.Index(list, `href="/agents/new-agent"`)
	if i < 0 {
		t.Fatal("new-agent missing")
	}
	row := list[i:]
	if j := strings.Index(row, "</a>"); j > 0 {
		row = row[:j]
	}
	for _, want := range []string{"Not seen recently", "Not in the latest 100 decisions"} {
		if !strings.Contains(row, want) {
			t.Errorf("crowded-out agent row missing %q: %s", want, row)
		}
	}
	if strings.Contains(row, "Not connected") || strings.Contains(row, "No recent decisions") {
		t.Errorf("crowded-out agent stated as a fact: %s", row)
	}
	if detail := send(h, c, http.MethodGet, "/agents/new-agent").Body.String(); !strings.Contains(detail, "Not seen recently.") {
		t.Error("agent page states a crowded-out agent as not connected")
	}
}

// A full page holding only a revoked key's request says nothing about
// the agent's active key: unknown, in the list and the Overview alike.
// With no active key left, it is definitely not connected.
func TestAgents_CrowdedOutActiveKeyIsUnknown(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		api  *agentsAPI
		want string
		not  string
	}{
		{"active key may be beyond the page", &agentsAPI{crowdedRevoked: true}, "Not seen recently", "Not connected"},
		{"no active key left", &agentsAPI{crowdedRevoked: true, keyRevoked: true}, "Not connected", "Not seen recently"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, c := agentsRig(t, tc.api)
			list := send(h, c, http.MethodGet, "/agents").Body.String()
			row := list[strings.Index(list, `href="/agents/refund-agent"`):]
			row = row[:strings.Index(row, "</a>")]
			if !strings.Contains(row, ">"+tc.want) || strings.Contains(row, ">"+tc.not) {
				t.Errorf("row should say %q, not %q: %s", tc.want, tc.not, row)
			}
			overview := send(h, c, http.MethodGet, "/agents/refund-agent").Body.String()
			if !strings.Contains(overview, "<strong>"+tc.want+".</strong>") {
				t.Errorf("overview should say %q", tc.want)
			}
			if !strings.Contains(overview, "(revoked)") {
				t.Error("the revoked key's request should stay marked as history")
			}
		})
	}
}

// When the recent decisions cannot be read, nothing is known about any
// agent's decisions: no agent reads "Not connected" or "No recent
// decisions". Runtime evidence still stands.
func TestAgents_FailedDecisionsReadIsUnknown(t *testing.T) {
	t.Parallel()
	h, c := agentsRig(t, &agentsAPI{proposalsFail: true})
	list := send(h, c, http.MethodGet, "/agents").Body.String()
	rowOf := func(id string) string {
		i := strings.Index(list, `href="/agents/`+id+`"`)
		if i < 0 {
			t.Fatalf("%s missing", id)
		}
		row := list[i:]
		if j := strings.Index(row, "</a>"); j > 0 {
			row = row[:j]
		}
		return row
	}
	fresh := rowOf("new-agent")
	for _, want := range []string{"Activity unknown", "Recent decisions could not be read"} {
		if !strings.Contains(fresh, want) {
			t.Errorf("new-agent row missing %q: %s", want, fresh)
		}
	}
	if strings.Contains(list, "Not connected") || strings.Contains(list, "No recent decisions") {
		t.Error("a failed read is stated as absence")
	}
	if !strings.Contains(rowOf("refund-agent"), "Enforcing") {
		t.Error("a live enforcing runtime must still show, whatever the decisions read")
	}

	detail := send(h, c, http.MethodGet, "/agents/new-agent").Body.String()
	if !strings.Contains(detail, "Activity unknown.") || strings.Contains(detail, "Not connected yet.") {
		t.Error("the agent page states a failed read as not connected")
	}
}

func TestAgents_FailedDecisionsReadWithStaleRuntimeIsUnknown(t *testing.T) {
	t.Parallel()
	h, c := agentsRig(t, &agentsAPI{proposalsFail: true, staleRuntime: true})
	list := send(h, c, http.MethodGet, "/agents").Body.String()
	i := strings.Index(list, `href="/agents/refund-agent"`)
	if i < 0 {
		t.Fatal("refund-agent missing")
	}
	row := list[i:]
	if j := strings.Index(row, "</a>"); j > 0 {
		row = row[:j]
	}
	if !strings.Contains(row, "Activity unknown") || strings.Contains(row, "Last seen") {
		t.Errorf("a stale guard does not establish activity when decisions fail: %s", row)
	}
	detail := send(h, c, http.MethodGet, "/agents/refund-agent").Body.String()
	if !strings.Contains(detail, "Activity unknown.") {
		t.Error("agent detail must agree with the list when decisions fail")
	}
}

// The Keys tab tells look-alike keys apart and revokes one only after a
// confirmation, which posts to the account's revoke route and comes back.
func TestAgents_KeysTabRevokesAfterConfirming(t *testing.T) {
	t.Parallel()
	h, c := agentsRig(t, &agentsAPI{})
	keys := send(h, c, http.MethodGet, "/agents/refund-agent/keys").Body.String()
	for _, want := range []string{`class="ac-srow" href="/agents/refund-agent/keys/`, "last used", `href="/agents/refund-agent/keys/new"`} {
		if !strings.Contains(keys, want) {
			t.Errorf("keys tab missing %q", want)
		}
	}
	if strings.Contains(keys, "/revoke\"") {
		t.Error("a key can be revoked in one click")
	}
	if fresh := send(h, c, http.MethodGet, "/agents/new-agent/keys").Body.String(); !strings.Contains(fresh, "No key yet") {
		t.Error("an agent without keys gets no help")
	}
	// The row opens the key's own sheet: what tells it apart, then Revoke.
	i := strings.Index(keys, `href="/agents/refund-agent/keys/`)
	rest := keys[i+len(`href="/agents/refund-agent/keys/`):]
	id := rest[:strings.IndexByte(rest, '"')]
	sheet := send(h, c, http.MethodGet, "/agents/refund-agent/keys/"+id).Body.String()
	for _, want := range []string{"<dt>Last use</dt>", "<dt>Key ID</dt>", `href="/agents/refund-agent/keys/` + id + `/revoke"`, "Close"} {
		if !strings.Contains(sheet, want) {
			t.Errorf("key sheet missing %q", want)
		}
	}
	if strings.Contains(sheet, `action="/dashboard/keys/`) {
		t.Error("the key sheet revokes without asking")
	}
	confirm := send(h, c, http.MethodGet, "/agents/refund-agent/keys/"+id+"/revoke").Body.String()
	for _, want := range []string{"Revoke this key?", `action="/dashboard/keys/` + id + `/revoke"`, `name="next" value="/agents/refund-agent/keys"`, "Keep it"} {
		if !strings.Contains(confirm, want) {
			t.Errorf("confirmation missing %q", want)
		}
	}
	if rec := send(h, c, http.MethodGet, "/agents/refund-agent/keys/not-its-key"); rec.Code != http.StatusNotFound {
		t.Errorf("another agent's or an unknown key = %d, want 404", rec.Code)
	}
	rec := postForm(h, c, "/dashboard/keys/"+id+"/revoke", url.Values{"next": {"/agents/refund-agent/keys"}})
	if loc := rec.Header().Get("Location"); loc != "/agents/refund-agent/keys" {
		t.Errorf("revoke returned to %q", loc)
	}
}

// The New key sheet asks where the key is used, and makes that kind.
func TestAgents_NewKeySheetMakesTheChosenKind(t *testing.T) {
	t.Parallel()
	h, c := gatewayAgentRig(t, &agentsAPI{}, true)
	page := send(h, c, http.MethodGet, "/agents/refund-agent/keys/new").Body.String()
	for _, want := range []string{`action="/agents/refund-agent/keys/new"`, `value="gateway" checked`, `value="guard"`} {
		if !strings.Contains(page, want) {
			t.Errorf("new key sheet missing %q", want)
		}
	}
	for kind, where := range map[string]string{"gateway": "/agents/refund-agent/tools#gateway", "guard": "/agents/refund-agent/keys", "": "/agents/refund-agent/keys/new"} {
		if loc := postForm(h, c, "/agents/refund-agent/keys/new", url.Values{"kind": {kind}}).Header().Get("Location"); loc != where {
			t.Errorf("kind %q went to %q, want %q", kind, loc, where)
		}
	}
}

// Every link on the agent's tabs reaches a dashboard route: an unknown
// path would fall through to the marketing site and leave the dashboard.
func TestAgents_TabLinksAreRoutes(t *testing.T) {
	t.Parallel()
	h, c := gatewayAgentRig(t, &agentsAPI{}, true)
	href := regexp.MustCompile(`href="(/[^"#]*)`)
	for _, page := range []string{"/agents/refund-agent", "/agents/new-agent", "/agents/refund-agent/keys", "/agents/new-agent/keys", "/agents/refund-agent/tools", "/agents/refund-agent/cards"} {
		for _, m := range href.FindAllStringSubmatch(send(h, c, http.MethodGet, page).Body.String(), -1) {
			link, _, _ := strings.Cut(html.UnescapeString(m[1]), "?")
			if strings.HasPrefix(link, "/static/") {
				continue
			}
			if rec := send(h, c, http.MethodGet, link); rec.Code == http.StatusNotFound {
				t.Errorf("%s links to %s, which is not a route", page, link)
			}
		}
	}
}

// Without a gateway (an API-only deployment) the New key sheet still
// makes kiff-guard keys, and offers no gateway key it cannot make.
func TestAgents_NewKeySheetWithoutAGatewayMakesGuardKeys(t *testing.T) {
	t.Parallel()
	api := &agentsAPI{}
	h, c := agentsRig(t, api)
	rec := send(h, c, http.MethodGet, "/agents/refund-agent/keys/new")
	page := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("new key sheet without a gateway = %d", rec.Code)
	}
	if !strings.Contains(page, `value="guard" checked`) || strings.Contains(page, `value="gateway"`) {
		t.Errorf("without a gateway the sheet should offer only a kiff-guard key: %s", page)
	}
	if loc := postForm(h, c, "/agents/refund-agent/keys/new", url.Values{"kind": {"guard"}}).Header().Get("Location"); loc != "/agents/refund-agent/keys" {
		t.Errorf("guard key went to %q", loc)
	}
	if len(api.keyReqs) != 1 || api.keyReqs[0]["agent_id"] != "refund-agent" {
		t.Errorf("key requests = %v", api.keyReqs)
	}
}
