package handlers_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kiff/cloud/web/internal/gatewayclient"
	"github.com/kiff/cloud/web/internal/handlers"
	"github.com/kiff/cloud/web/internal/session"
)

const toolCred = "sk_tool_7f3a9c"

// toolsGateway stubs the gateway's /v1/connections. It only answers the
// signed-in owner's bearer, as the real gateway only answers a key or
// session apps/api accepts.
type toolsGateway struct {
	mu       sync.Mutex
	bearers  []string
	created  []map[string]any
	creds    []string
	removed  []string
	modes    []map[string]any
	settings []map[string]any
	forbid   bool
	refuse   string
	testFail bool
}

func (g *toolsGateway) serve(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.bearers = append(g.bearers, r.Header.Get("Authorization"))
	if r.Header.Get("Authorization") != "Bearer b" {
		writeJSONForTest(w, http.StatusUnauthorized, map[string]string{"error": "the key was not accepted"})
		return
	}
	if g.forbid {
		writeJSONForTest(w, http.StatusForbidden, map[string]string{"error": "connecting tools requires the account's owner or an admin"})
		return
	}
	body := map[string]any{}
	if b, _ := io.ReadAll(r.Body); len(b) > 0 {
		_ = json.Unmarshal(b, &body)
	}
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/v1/connections":
		writeJSONForTest(w, http.StatusOK, map[string]any{"connections": []map[string]any{{
			"name": "issue_refund", "description": "Refund an order", "url": "https://tools.example.com/mcp", "tool": "refund",
			"amount_argument": "amount_eur", "idempotency_argument": "idem", "read_only": false, "has_credential": true,
			"resource_argument": "order", "mode": "sealed",
			"input_schema": map[string]any{"type": "object", "properties": map[string]any{
				"order": map[string]any{"type": "string"}, "note": map[string]any{"type": "string"},
				"amount_eur": map[string]any{"type": "integer"}, "idem": map[string]any{"type": "string"}}},
		}}})
	case r.Method == http.MethodPost && r.URL.Path == "/v1/connections/discover":
		if body["credential"] != toolCred {
			writeJSONForTest(w, http.StatusBadGateway, map[string]string{"error": "KIFF could not list the tools at that URL: 401 [redacted]"})
			return
		}
		writeJSONForTest(w, http.StatusOK, map[string]any{"url": "https://tools.example.com/mcp", "tools": []map[string]any{
			{"name": "refund", "description": "Refund an order", "integer_arguments": []string{"amount_eur"}, "string_arguments": []string{"idem", "order"}},
			{"name": "get-balance", "description": "Read a balance", "integer_arguments": []string{}, "string_arguments": []string{"account"}},
		}})
	case r.Method == http.MethodPost && r.URL.Path == "/v1/connections":
		g.created = append(g.created, body)
		if g.refuse != "" {
			writeJSONForTest(w, http.StatusConflict, map[string]string{"error": g.refuse})
			return
		}
		writeJSONForTest(w, http.StatusCreated, map[string]any{"name": body["name"], "tool": body["tool"], "has_credential": true})
	case r.Method == http.MethodPost && r.URL.Path == "/v1/connections/issue_refund/test":
		if g.testFail {
			writeJSONForTest(w, http.StatusOK, map[string]any{"ok": false, "error": "the server has no tool named refund"})
			return
		}
		writeJSONForTest(w, http.StatusOK, map[string]any{"ok": true, "message": "KIFF reached the tool server with the stored credential and found refund."})
	case r.Method == http.MethodPut && r.URL.Path == "/v1/connections/issue_refund/credential":
		cred, _ := body["credential"].(string)
		g.creds = append(g.creds, cred)
		if cred != toolCred {
			writeJSONForTest(w, http.StatusBadGateway, map[string]string{"error": "the new credential was not saved: 401 [redacted]"})
			return
		}
		writeJSONForTest(w, http.StatusOK, map[string]any{"name": "issue_refund"})
	case r.Method == http.MethodPut && r.URL.Path == "/v1/connections/issue_refund/mode":
		g.modes = append(g.modes, body)
		writeJSONForTest(w, http.StatusOK, map[string]any{"name": "issue_refund", "mode": body["mode"]})
	case r.Method == http.MethodPut && r.URL.Path == "/v1/connections/issue_refund/settings":
		g.settings = append(g.settings, body)
		writeJSONForTest(w, http.StatusOK, map[string]any{"name": "issue_refund"})
	case r.Method == http.MethodDelete && r.URL.Path == "/v1/connections/issue_refund":
		g.removed = append(g.removed, "issue_refund")
		w.WriteHeader(http.StatusNoContent)
	default:
		writeJSONForTest(w, http.StatusNotFound, map[string]string{"error": "no tool named " + r.URL.Path + " is connected"})
	}
}

func toolsRig(t *testing.T, g *toolsGateway) (http.Handler, *http.Cookie) {
	t.Helper()
	mgr := newTestSessionManager(t)
	dash := handlers.NewDashboardHandler(nil, mgr, handlers.StaticBearerSource(), nil)
	if g != nil {
		srv := httptest.NewServer(http.HandlerFunc(g.serve))
		t.Cleanup(srv.Close)
		gw, err := gatewayclient.New(srv.URL)
		if err != nil {
			t.Fatal(err)
		}
		dash.SetGateway(gw)
	}
	mux := http.NewServeMux()
	dash.Register(mux)
	issue := httptest.NewRecorder()
	if err := mgr.Issue(issue, session.Session{Subject: "alice", Bearer: "b", Email: "a@example.com", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	return handlers.RequireSession(mgr)(mux), issue.Result().Cookies()[0]
}

func postForm(h http.Handler, c *http.Cookie, path string, form url.Values) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(c)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// followFlash renders /tools with the flash a redirect set.
func followFlash(t *testing.T, h http.Handler, c *http.Cookie, rec *httptest.ResponseRecorder) string {
	t.Helper()
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/tools" {
		t.Fatalf("redirect = %d → %q", rec.Code, rec.Header().Get("Location"))
	}
	req := httptest.NewRequest(http.MethodGet, "/tools", nil)
	req.AddCookie(c)
	for _, ck := range rec.Result().Cookies() {
		if ck.Name == session.FlashCookieName {
			req.AddCookie(ck)
		}
	}
	out := httptest.NewRecorder()
	h.ServeHTTP(out, req)
	return out.Body.String()
}

func TestTools_ListsConnectedTools(t *testing.T) {
	t.Parallel()
	h, c := toolsRig(t, &toolsGateway{})
	body := send(h, c, http.MethodGet, "/tools").Body.String()
	for _, want := range []string{"issue_refund", "Refund an order", "tools.example.com", "1 tool · KIFF holds the key, encrypted", `href="/tools/issue_refund"`,
		`href="/tools/new"`, `class="dash-nav-item dash-nav-active" href="/tools"`, ">Amount<", `href="/tools/new?url=https%3A%2F%2Ftools.example.com%2Fmcp"`} {
		if !strings.Contains(body, want) {
			t.Errorf("tools page missing %q", want)
		}
	}
	// A search that matches nothing says so; the server is filled in
	// when adding another tool from it.
	if none := send(h, c, http.MethodGet, "/tools?q=zzz").Body.String(); !strings.Contains(none, "No tool matches.") || strings.Contains(none, `id="tool-issue_refund"`) {
		t.Error("an unmatched search still lists the tool")
	}
	if form := send(h, c, http.MethodGet, "/tools/new?url=https%3A%2F%2Ftools.example.com%2Fmcp").Body.String(); !strings.Contains(form, `value="https://tools.example.com/mcp"`) {
		t.Error("Add a tool from this server does not fill the server in")
	}
	// The tool's own page: Overview with its test, Settings as rows that
	// each open one sheet.
	overview := send(h, c, http.MethodGet, "/tools/issue_refund").Body.String()
	for _, want := range []string{`action="/tools/issue_refund/test"`, `href="/tools/issue_refund/card"`, `href="/tools/issue_refund/settings"`} {
		if !strings.Contains(overview, want) {
			t.Errorf("tool overview missing %q", want)
		}
	}
	settings := send(h, c, http.MethodGet, "/tools/issue_refund/settings").Body.String()
	for _, want := range []string{"amount_eur", "Each call is on", ">order<", "If a call did nothing", "KIFF, encrypted", "tools.example.com",
		`href="/tools/issue_refund/settings/resource"`, `href="/tools/issue_refund/settings/release"`, `href="/tools/issue_refund/settings/key"`,
		`href="/tools/issue_refund/settings/credential"`, `href="/tools/issue_refund/settings/remove"`} {
		if !strings.Contains(settings, want) {
			t.Errorf("tool settings missing %q", want)
		}
	}
	for sheet, want := range map[string]string{"key": `action="/tools/issue_refund/mode"`, "credential": `action="/tools/issue_refund/credential"`,
		"remove": `action="/tools/issue_refund/remove"`, "release": `name="outcome_contract"`, "resource": `name="resource_argument"`} {
		if body := send(h, c, http.MethodGet, "/tools/issue_refund/settings/"+sheet).Body.String(); !strings.Contains(body, want) {
			t.Errorf("%s sheet missing %q", sheet, want)
		}
	}
	if rec := send(h, c, http.MethodGet, "/tools/issue_refund/settings/nope"); rec.Code != http.StatusNotFound {
		t.Errorf("unknown sheet = %d", rec.Code)
	}
	if rec := send(h, c, http.MethodGet, "/tools/other_tool"); rec.Code != http.StatusNotFound {
		t.Errorf("unknown tool = %d", rec.Code)
	}
}

// The resource sheet offers the tool's text arguments, never its amount
// or same-call key, and saving changes the setting without
// reconnecting, then returns to the tool's Settings.
func TestTools_SettingsChangeWithoutReconnecting(t *testing.T) {
	t.Parallel()
	g := &toolsGateway{}
	h, c := toolsRig(t, g)
	sheet := send(h, c, http.MethodGet, "/tools/issue_refund/settings/resource").Body.String()
	for _, want := range []string{`value="order" checked`, `value="note"`, "Nothing in particular"} {
		if !strings.Contains(sheet, want) {
			t.Errorf("resource sheet missing %q", want)
		}
	}
	for _, not := range []string{`value="idem"`, `value="amount_eur"`} {
		if strings.Contains(sheet, not) {
			t.Errorf("resource sheet offers %s", not)
		}
	}
	back := "/tools/issue_refund/settings"
	page := followFlashTo(t, h, c, postForm(h, c, "/tools/issue_refund/settings",
		url.Values{"setting": {"resource"}, "resource_argument": {"note"}, "back": {back}}), back)
	if !strings.Contains(page, "Each call to issue_refund is now on note.") {
		t.Fatalf("flash: %s", page)
	}
	followFlashTo(t, h, c, postForm(h, c, "/tools/issue_refund/settings",
		url.Values{"setting": {"release"}, "outcome_contract": {""}, "back": {back}}), back)
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.settings) != 2 || g.settings[0]["resource_argument"] != "note" || g.settings[0]["outcome_contract"] != nil ||
		g.settings[1]["outcome_contract"] != "" || g.settings[1]["resource_argument"] != nil {
		t.Fatalf("settings sent: %v", g.settings)
	}
}

// Step 1 → step 2: the server's tools, each with only the arguments the
// gateway would accept, and the credential carried on a page no one
// stores. A credential the server rejects stays on step 1 and is not
// echoed back.
func TestTools_DiscoverListsTheServersTools(t *testing.T) {
	t.Parallel()
	g := &toolsGateway{}
	h, c := toolsRig(t, g)
	rec := postForm(h, c, "/tools/discover", url.Values{"url": {" https://tools.example.com/mcp "}, "credential": {toolCred}})
	body := rec.Body.String()
	if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("discover = %d, Cache-Control %q", rec.Code, rec.Header().Get("Cache-Control"))
	}
	for _, want := range []string{"2 tools found", `<option value="amount_eur">amount_eur</option>`, `<option value="idem">idem</option>`,
		`name="tool" value="refund"`, `name="tool" value="get-balance"`, `name="credential" value="` + toolCred + `"`,
		"has no whole-number argument"} {
		if !strings.Contains(body, want) {
			t.Errorf("step 2 missing %q", want)
		}
	}
	// get-balance is not a name the gateway accepts, so the owner names it.
	if !strings.Contains(body, `name="name" type="text" required maxlength="64" pattern="[A-Za-z][A-Za-z0-9_]*" value=""`) {
		t.Error("an invalid tool name is offered as the default")
	}

	rec = postForm(h, c, "/tools/discover", url.Values{"url": {"https://tools.example.com/mcp"}, "credential": {"sk_wrong_value"}})
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "could not list the tools") ||
		strings.Contains(rec.Body.String(), "sk_wrong_value") || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("wrong credential = %d %s", rec.Code, rec.Body.String())
	}
}

func TestTools_ConnectSendsTheOwnersChoices(t *testing.T) {
	t.Parallel()
	g := &toolsGateway{}
	h, c := toolsRig(t, g)
	form := url.Values{"url": {"https://tools.example.com/mcp"}, "credential": {toolCred}, "tool": {"refund"}, "name": {"issue_refund"},
		"amount_argument": {"amount_eur"}, "idempotency_argument": {"idem"}, "read_only": {"true"}, "outcome_contract": {"terminal_no_effect_v1"}}
	form.Set("resource_argument", "order")
	page := followFlashTo(t, h, c, postForm(h, c, "/tools", form), "/tools/issue_refund")
	if !strings.Contains(page, "issue_refund is connected.") || strings.Contains(page, toolCred) {
		t.Fatalf("after connect: %s", page)
	}
	got := g.created[0]
	if got["name"] != "issue_refund" || got["tool"] != "refund" || got["amount_argument"] != "amount_eur" || got["idempotency_argument"] != "idem" ||
		got["credential"] != toolCred || got["read_only"] != true || got["url"] != "https://tools.example.com/mcp" || got["outcome_contract"] != "terminal_no_effect_v1" ||
		got["resource_argument"] != "order" {
		t.Fatalf("gateway got %v", got)
	}

	// A refused save goes back to step 2 with the gateway's reason and
	// the owner's choices kept.
	g.refuse = "a tool named issue_refund is already connected; remove it first, or choose another name"
	rec := postForm(h, c, "/tools", form)
	body := rec.Body.String()
	if rec.Code != http.StatusBadRequest || rec.Header().Get("Cache-Control") != "no-store" ||
		!strings.Contains(body, "The tool was not connected: a tool named issue_refund is already connected") ||
		!strings.Contains(body, `<option value="amount_eur" selected>`) || !strings.Contains(body, `value="true" checked`) || !strings.Contains(body, `value="terminal_no_effect_v1" checked`) {
		t.Fatalf("refused connect = %d %s", rec.Code, body)
	}
}

func TestTools_TestReplaceRemove(t *testing.T) {
	t.Parallel()
	g := &toolsGateway{}
	h, c := toolsRig(t, g)
	if page := followFlash(t, h, c, postForm(h, c, "/tools/issue_refund/test", nil)); !strings.Contains(page, "found refund.") {
		t.Errorf("test: %s", page)
	}
	g.testFail = true
	if page := followFlash(t, h, c, postForm(h, c, "/tools/issue_refund/test", nil)); !strings.Contains(page, "The test of issue_refund failed: the server has no tool named refund") {
		t.Errorf("failed test: %s", page)
	}
	page := followFlash(t, h, c, postForm(h, c, "/tools/issue_refund/credential", url.Values{"credential": {"sk_wrong_value"}}))
	if !strings.Contains(page, "the new credential was not saved") || strings.Contains(page, "sk_wrong_value") {
		t.Errorf("bad credential: %s", page)
	}
	if page := followFlash(t, h, c, postForm(h, c, "/tools/issue_refund/credential", url.Values{"credential": {toolCred}})); !strings.Contains(page, "The credential for issue_refund is replaced.") {
		t.Errorf("replace: %s", page)
	}
	if page := followFlash(t, h, c, postForm(h, c, "/tools/issue_refund/remove", nil)); !strings.Contains(page, "issue_refund is removed.") {
		t.Errorf("remove: %s", page)
	}
	if len(g.removed) != 1 || len(g.creds) != 2 {
		t.Errorf("removed %v, credentials sent %d", g.removed, len(g.creds))
	}
	for _, b := range g.bearers {
		if b != "Bearer b" {
			t.Errorf("gateway called with %q, not the owner's session bearer", b)
		}
	}
}

// Who may manage tools is the gateway's decision; its sentence is shown.
func TestTools_GatewayRefusalIsShown(t *testing.T) {
	t.Parallel()
	h, c := toolsRig(t, &toolsGateway{forbid: true})
	body := send(h, c, http.MethodGet, "/tools").Body.String()
	if !strings.Contains(body, "This is not a statement that none are connected.") || !strings.Contains(body, "requires the account&#39;s owner or an admin") {
		t.Fatalf("forbidden: %s", body)
	}
}

func TestTools_WithoutAGateway(t *testing.T) {
	t.Parallel()
	h, c := toolsRig(t, nil)
	for _, path := range []string{"/tools", "/tools/new"} {
		if body := send(h, c, http.MethodGet, path).Body.String(); !strings.Contains(body, "no KIFF gateway configured") {
			t.Errorf("%s: %s", path, body)
		}
	}
	if page := followFlash(t, h, c, postForm(h, c, "/tools/issue_refund/remove", nil)); !strings.Contains(page, "no KIFF gateway configured") {
		t.Errorf("remove: %s", page)
	}
}

// the execution-permits design: the owner moves a tool to verify. The
// form sends the mode and audience, and never a credential for verify
// even if one was typed.
func TestTools_ChangeCustodyMode(t *testing.T) {
	t.Parallel()
	g := &toolsGateway{}
	h, c := toolsRig(t, g)
	page := followFlash(t, h, c, postForm(h, c, "/tools/issue_refund/mode", url.Values{
		"mode": {"verify"}, "audience": {" https://refunds.example.com/mcp "}, "credential": {"typed-by-mistake"}}))
	if !strings.Contains(page, "now checks KIFF&#39;s execution permit") && !strings.Contains(page, "now checks KIFF's execution permit") {
		t.Fatalf("flash: %s", page)
	}
	if len(g.modes) != 1 || g.modes[0]["mode"] != "verify" || g.modes[0]["audience"] != "https://refunds.example.com/mcp" {
		t.Fatalf("sent %v", g.modes)
	}
	if _, sent := g.modes[0]["credential"]; sent {
		t.Fatalf("a credential was sent for verify: %v", g.modes[0])
	}
}

// The owner says what the amount is when connecting ('s
// amount_unit/amount_scale); a unit without an amount, or one that is
// not offered, is not sent.
func TestTools_ConnectSendsTheAmountUnit(t *testing.T) {
	t.Parallel()
	g := &toolsGateway{}
	h, c := toolsRig(t, g)
	base := url.Values{"url": {"https://tools.example.com/mcp"}, "credential": {toolCred}, "tool": {"refund"}, "name": {"issue_refund"}}
	with := func(kv ...string) url.Values {
		f := url.Values{}
		for k, v := range base {
			f[k] = v
		}
		for i := 0; i < len(kv); i += 2 {
			f.Set(kv[i], kv[i+1])
		}
		return f
	}
	for _, c2 := range []struct {
		form  url.Values
		unit  any
		scale any
	}{
		{with("amount_argument", "amount_eur", "amount_unit", "EUR:0"), "EUR", float64(0)},
		{with("amount_argument", "amount_cents", "amount_unit", "USD:2"), "USD", float64(2)},
		{with("amount_argument", "amount_eur", "amount_unit", ""), nil, nil},
		{with("amount_argument", "amount_eur", "amount_unit", "BTC:8"), nil, nil},
		{with("amount_unit", "EUR:0"), nil, nil},
	} {
		n := len(g.created)
		postForm(h, c, "/tools", c2.form)
		if len(g.created) != n+1 {
			t.Fatalf("%v: not sent", c2.form)
		}
		got := g.created[n]
		if got["amount_unit"] != c2.unit || got["amount_scale"] != c2.scale {
			t.Errorf("%v: gateway got unit %v scale %v, want %v %v", c2.form, got["amount_unit"], got["amount_scale"], c2.unit, c2.scale)
		}
	}
}

// One operation of a generic tool. The form sends the fixed arguments as
// JSON and the paths as typed, with the amount's unit; a fixed-arguments
// text that is not a JSON object comes back to the owner without
// reaching the gateway.
func TestTools_ConnectAGenericToolOperation(t *testing.T) {
	t.Parallel()
	g := &toolsGateway{}
	h, c := toolsRig(t, g)
	form := url.Values{"url": {"https://mcp.stripe.com"}, "credential": {toolCred}, "tool": {"get-balance"}, "name": {"stripe_refund"},
		"fixed_arguments": {` {"stripe_api_operation_id": "PostRefunds"} `}, "amount_path": {"parameters.amount"},
		"amount_unit": {"EUR:2"}, "resource_path": {" parameters.charge "}}
	postForm(h, c, "/tools", form)
	if len(g.created) != 1 {
		t.Fatalf("not sent: %v", g.created)
	}
	got := g.created[0]
	fixed, _ := got["fixed_arguments"].(map[string]any)
	if fixed["stripe_api_operation_id"] != "PostRefunds" || got["amount_path"] != "parameters.amount" || got["resource_path"] != "parameters.charge" ||
		got["amount_unit"] != "EUR" || got["amount_scale"] != float64(2) || got["amount_argument"] != nil {
		t.Fatalf("gateway got %v", got)
	}

	form.Set("fixed_arguments", `stripe_api_operation_id=PostRefunds`)
	rec := postForm(h, c, "/tools", form)
	body := rec.Body.String()
	if rec.Code != http.StatusBadRequest || len(g.created) != 1 || !strings.Contains(body, "must be a JSON object") ||
		!strings.Contains(body, `stripe_api_operation_id=PostRefunds</textarea>`) || !strings.Contains(body, `value="parameters.amount"`) ||
		!strings.Contains(body, `<option value="EUR:2" selected>`) {
		t.Fatalf("bad fixed arguments = %d, sent %d: %s", rec.Code, len(g.created), body)
	}
}
