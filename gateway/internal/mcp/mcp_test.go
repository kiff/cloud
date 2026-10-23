package mcp_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/kiff/cloud/gateway/internal/mcp"
)

type echo struct{}

func (echo) ListTools(context.Context) ([]mcp.Tool, error) { return nil, nil }
func (echo) CallTool(_ context.Context, name string, args json.RawMessage) (json.RawMessage, error) {
	return mcp.TextResult(name+" "+string(args), false), nil
}

func server(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(&mcp.Server{Name: "t", Version: "1",
		Auth: func(*http.Request) (mcp.Handler, error) { return echo{}, nil }})
	t.Cleanup(srv.Close)
	return srv
}

// The client speaks to the gateway's own server end to end.
func TestClientAgainstServer(t *testing.T) {
	srv := server(t)
	res, err := (&mcp.Client{}).CallTool(context.Background(), srv.URL, "", "refund", json.RawMessage(`{"a":1}`))
	if err != nil || !strings.Contains(string(res), `refund {\"a\":1}`) {
		t.Fatalf("%s %v", res, err)
	}
}

// A server that answers with an event stream is read to the response
// with the request's id; a JSON-RPC error comes back as *RPCError.
func TestClientReadsEventStreamAndErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &req)
		if len(req.ID) == 0 {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		w.Header().Set("Mcp-Session-Id", "s-1")
		if req.Method == "initialize" {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":"2025-06-18"}}`, req.ID)
			return
		}
		if r.Header.Get("Mcp-Session-Id") != "s-1" {
			http.Error(w, "no session", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\"}\n\n")
		if strings.Contains(string(b), `"refuse"`) {
			fmt.Fprintf(w, "data: {\"jsonrpc\":\"2.0\",\"id\":%s,\"error\":{\"code\":-32000,\"message\":\"no such order\"}}\n\n", req.ID)
			return
		}
		fmt.Fprintf(w, "data: {\"jsonrpc\":\"2.0\",\"id\":%s,\"result\":{\"content\":[{\"type\":\"text\",\"text\":\"ok\"}]}}\n\n", req.ID)
	}))
	defer srv.Close()
	c := &mcp.Client{}
	res, err := c.CallTool(context.Background(), srv.URL, "", "refund", nil)
	if err != nil || !strings.Contains(string(res), `"ok"`) {
		t.Fatalf("%s %v", res, err)
	}
	_, err = c.CallTool(context.Background(), srv.URL, "", "refuse", nil)
	var rpc *mcp.RPCError
	if !errors.As(err, &rpc) || rpc.Message != "no such order" {
		t.Fatalf("tool error: %v", err)
	}
}

// A server that cannot be reached was certainly not sent the call.
func TestClientUnreachableIsNotSent(t *testing.T) {
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := l.Addr().String()
	l.Close()
	_, err := (&mcp.Client{}).CallTool(context.Background(), "http://"+addr, "", "refund", nil)
	var ce *mcp.CallError
	if !errors.As(err, &ce) || ce.Sent {
		t.Fatalf("%v", err)
	}
}

// Browser requests and non-POST methods are refused; notifications get
// 202; unknown methods a JSON-RPC error.
func TestServerTransportRules(t *testing.T) {
	srv := server(t)
	req, _ := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`))
	req.Header.Set("Origin", "https://evil.example")
	if resp, _ := http.DefaultClient.Do(req); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("origin: %d", resp.StatusCode)
	}
	if resp, _ := http.Get(srv.URL); resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("GET: %d", resp.StatusCode)
	}
	if resp, _ := http.Post(srv.URL, "application/json", strings.NewReader(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("notification: %d", resp.StatusCode)
	}
	resp, _ := http.Post(srv.URL, "application/json", strings.NewReader(`{"jsonrpc":"2.0","id":7,"method":"resources/list"}`))
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if out["error"] == nil || out["id"] != float64(7) {
		t.Fatalf("unknown method: %v", out)
	}
}

func modernPost(t *testing.T, url string, headers map[string]string, body string) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

const meta = `"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":{"name":"t","version":"1"},"io.modelcontextprotocol/clientCapabilities":{}}`

func errCode(out map[string]any) float64 {
	e, _ := out["error"].(map[string]any)
	c, _ := e["code"].(float64)
	return c
}

// The 2026-07-28 rules: discover, headers that must match the body,
// resultType on results, version and method errors with their statuses.
func TestServerModernRequests(t *testing.T) {
	srv := server(t)
	h := func(method, name string) map[string]string {
		m := map[string]string{"MCP-Protocol-Version": "2026-07-28", "Mcp-Method": method}
		if name != "" {
			m["Mcp-Name"] = name
		}
		return m
	}
	status, out := modernPost(t, srv.URL, h("server/discover", ""), `{"jsonrpc":"2.0","id":1,"method":"server/discover","params":{`+meta+`}}`)
	res, _ := out["result"].(map[string]any)
	if status != 200 || res["resultType"] != "complete" || res["cacheScope"] != "public" || res["ttlMs"] == nil ||
		!strings.Contains(fmt.Sprint(res["supportedVersions"]), "2026-07-28") ||
		!strings.Contains(fmt.Sprint(res["supportedVersions"]), "2025-06-18") || res["capabilities"] == nil {
		t.Fatalf("discover: %d %v", status, out)
	}
	call := `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"refund","arguments":{"a":1},` + meta + `}}`
	status, out = modernPost(t, srv.URL, h("tools/call", "refund"), call)
	if res, _ := out["result"].(map[string]any); status != 200 || res["resultType"] != "complete" || res["content"] == nil {
		t.Fatalf("tools/call: %d %v", status, out)
	}
	status, out = modernPost(t, srv.URL, h("tools/list", ""), `{"jsonrpc":"2.0","id":4,"method":"tools/list","params":{`+meta+`}}`)
	if res, _ := out["result"].(map[string]any); status != 200 || res["cacheScope"] != "private" || res["ttlMs"] == nil || res["tools"] == nil {
		t.Fatalf("tools/list caching hints: %d %v", status, out)
	}
	if status, out = modernPost(t, srv.URL, h("tools/call", "=?base64?cmVmdW5k?="), call); status != 200 {
		t.Fatalf("base64 name: %d %v", status, out)
	}
	for label, hdr := range map[string]map[string]string{
		"no Mcp-Name":      h("tools/call", ""),
		"other Mcp-Name":   h("tools/call", "drop"),
		"other Mcp-Method": h("tools/list", "refund"),
		"no version":       {"Mcp-Method": "tools/call", "Mcp-Name": "refund"},
		"version mismatch": {"MCP-Protocol-Version": "2025-06-18", "Mcp-Method": "tools/call", "Mcp-Name": "refund"},
	} {
		if status, out := modernPost(t, srv.URL, hdr, call); status != 400 || errCode(out) != -32020 {
			t.Errorf("%s: %d %v", label, status, out)
		}
	}
	future := strings.ReplaceAll(call, "2026-07-28", "2099-01-01")
	status, out = modernPost(t, srv.URL, map[string]string{"MCP-Protocol-Version": "2099-01-01", "Mcp-Method": "tools/call", "Mcp-Name": "refund"}, future)
	if status != 400 || errCode(out) != -32022 || !strings.Contains(fmt.Sprint(out["error"]), "supported") {
		t.Fatalf("unsupported version: %d %v", status, out)
	}
	status, out = modernPost(t, srv.URL, h("prompts/list", ""), `{"jsonrpc":"2.0","id":3,"method":"prompts/list","params":{`+meta+`}}`)
	if status != 404 || errCode(out) != -32601 {
		t.Fatalf("unknown method: %d %v", status, out)
	}
}

// An initialize-era client is still served: initialize, then tools/call
// without a resultType.
func TestServerLegacyRequests(t *testing.T) {
	srv := server(t)
	_, out := modernPost(t, srv.URL, nil, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`)
	if res, _ := out["result"].(map[string]any); res["protocolVersion"] != "2025-06-18" {
		t.Fatalf("initialize: %v", out)
	}
	_, out = modernPost(t, srv.URL, map[string]string{"MCP-Protocol-Version": "2025-06-18"}, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"refund"}}`)
	if res, _ := out["result"].(map[string]any); res == nil || res["resultType"] != nil {
		t.Fatalf("legacy tools/call: %v", out)
	}
}

type recorder struct {
	mu      sync.Mutex
	methods []string
	next    http.Handler
}

func (r *recorder) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	b, _ := io.ReadAll(req.Body)
	var m struct {
		Method string `json:"method"`
	}
	_ = json.Unmarshal(b, &m)
	r.mu.Lock()
	r.methods = append(r.methods, m.Method)
	r.mu.Unlock()
	req.Body = io.NopCloser(bytes.NewReader(b))
	r.next.ServeHTTP(w, req)
}

// Against a modern server the client probes once, then calls without a
// handshake.
func TestClientModernServer(t *testing.T) {
	rec := &recorder{next: &mcp.Server{Name: "t", Version: "1",
		Auth: func(*http.Request) (mcp.Handler, error) { return echo{}, nil }}}
	srv := httptest.NewServer(rec)
	defer srv.Close()
	c := &mcp.Client{}
	for i := 0; i < 2; i++ {
		res, err := c.CallTool(context.Background(), srv.URL, "", "refund", json.RawMessage(`{"a":1}`))
		if err != nil || !strings.Contains(string(res), `"resultType":"complete"`) {
			t.Fatalf("%s %v", res, err)
		}
	}
	if got := strings.Join(rec.methods, ","); got != "server/discover,tools/call,tools/call" {
		t.Fatalf("methods = %s", got)
	}
}

// Against a legacy server the probe is server/discover, never a
// tools/call, so a server that would run an uninitialized call cannot run
// it twice.
func TestClientLegacyServerNeverProbesWithACall(t *testing.T) {
	calls := 0
	rec := &recorder{next: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		switch req.Method {
		case "server/discover":
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"error":{"code":-32601,"message":"Method not found"}}`, req.ID)
		case "initialize":
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":"2025-06-18"}}`, req.ID)
		case "tools/call":
			calls++
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{"content":[{"type":"text","text":"done"}]}}`, req.ID)
		default:
			w.WriteHeader(http.StatusAccepted)
		}
	})}
	srv := httptest.NewServer(rec)
	defer srv.Close()
	res, err := (&mcp.Client{}).CallTool(context.Background(), srv.URL, "", "refund", nil)
	if err != nil || !strings.Contains(string(res), "done") || calls != 1 {
		t.Fatalf("%s %v calls=%d", res, err, calls)
	}
	if got := strings.Join(rec.methods, ","); got != "server/discover,initialize,notifications/initialized,tools/call" {
		t.Fatalf("methods = %s", got)
	}
}

// Modern notifications carry the routing headers too: a missing or
// mismatched Mcp-Method, or Mcp-Name on a tools/call, is refused; a
// matching one is accepted.
func TestServerModernNotificationsAreValidated(t *testing.T) {
	srv := server(t)
	note := func(method string) string {
		return `{"jsonrpc":"2.0","method":"` + method + `","params":{"name":"refund",` + meta + `}}`
	}
	v := map[string]string{"MCP-Protocol-Version": "2026-07-28"}
	with := func(extra map[string]string) map[string]string {
		m := map[string]string{}
		for k, x := range v {
			m[k] = x
		}
		for k, x := range extra {
			m[k] = x
		}
		return m
	}
	for label, c := range map[string]struct {
		hdr    map[string]string
		method string
	}{
		"no Mcp-Method":               {with(nil), "notifications/cancelled"},
		"other Mcp-Method":            {with(map[string]string{"Mcp-Method": "tools/list"}), "notifications/cancelled"},
		"tools/call without Mcp-Name": {with(map[string]string{"Mcp-Method": "tools/call"}), "tools/call"},
		"tools/call other Mcp-Name":   {with(map[string]string{"Mcp-Method": "tools/call", "Mcp-Name": "drop"}), "tools/call"},
	} {
		if status, out := modernPost(t, srv.URL, c.hdr, note(c.method)); status != 400 || errCode(out) != -32020 {
			t.Errorf("%s: %d %v", label, status, out)
		}
	}
	if status, _ := modernPost(t, srv.URL, with(map[string]string{"Mcp-Method": "notifications/cancelled"}), note("notifications/cancelled")); status != 202 {
		t.Errorf("valid notification: %d", status)
	}
	if status, _ := modernPost(t, srv.URL, with(map[string]string{"Mcp-Method": "tools/call", "Mcp-Name": "refund"}), note("tools/call")); status != 202 {
		t.Errorf("valid tools/call notification: %d", status)
	}
}
