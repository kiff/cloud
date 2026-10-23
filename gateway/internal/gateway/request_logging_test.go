package gateway_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kiff/cloud/gateway/internal/connection"
	"github.com/kiff/cloud/gateway/internal/gateway"
	"github.com/kiff/cloud/gateway/internal/kiffapi"
	"github.com/kiff/cloud/gateway/internal/requestlog"
)

type loggingCatalog struct{}

func (loggingCatalog) Tools(context.Context, string) ([]gateway.Tool, error) { return nil, nil }

func TestRequestLogsThroughGatewayAuthentication(t *testing.T) {
	const secret = "PRIVATE-request-credentials-arguments-and-card"
	const initialize = `{"jsonrpc":"2.0","id":"` + secret + `","method":"initialize","params":{"protocolVersion":"2025-03-26","clientInfo":{"name":"` + secret + `","version":"1"}}}`
	for _, tc := range []struct {
		name, method, path, key, body, origin, identity, operation string
		status                                                     int
	}{
		{name: "anonymous", method: "POST", path: "/mcp", body: initialize, identity: "anonymous", status: 401},
		{name: "invalid-key", method: "POST", path: "/mcp", key: secret, body: initialize, identity: "anonymous", status: 401},
		{name: "invalid-oauth", method: "POST", path: "/mcp", key: "kiff_oat_" + secret, body: initialize, identity: "anonymous", status: 401},
		{name: "origin", method: "POST", path: "/mcp", key: "valid-agent", body: initialize, origin: "https://" + secret, identity: "anonymous", status: 403},
		{name: "get", method: "GET", path: "/mcp", key: "valid-agent", identity: "anonymous", status: 405},
		{name: "agent", method: "POST", path: "/mcp", key: "valid-agent", body: initialize, identity: "agent", operation: "initialize", status: 200},
		{name: "oauth", method: "POST", path: "/mcp", key: "kiff_oat_valid", body: initialize, identity: "oauth", operation: "initialize", status: 200},
		{name: "oauth-other-resource", method: "POST", path: "/mcp", key: "kiff_oat_other", body: initialize, identity: "oauth", status: 401},
		{name: "malformed-json", method: "POST", path: "/mcp", key: "valid-agent", body: "{" + secret, identity: "agent", status: 400},
		{name: "notification", method: "POST", path: "/mcp", key: "valid-agent", body: `{"jsonrpc":"2.0","method":"notifications/initialized"}`, identity: "agent", operation: "notifications/initialized", status: 202},
		{name: "unknown-rpc", method: "POST", path: "/mcp", key: "valid-agent", body: `{"jsonrpc":"2.0","id":1,"method":"` + secret + `"}`, identity: "agent", operation: "other", status: 200},
		{name: "tools-list", method: "POST", path: "/mcp", key: "valid-agent", body: `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, identity: "agent", operation: "tools/list", status: 200},
		{name: "card-and-args", method: "POST", path: "/mcp", key: "valid-agent", body: `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"kiff_card","arguments":{"secret":"` + secret + `"}}}`, identity: "agent", operation: "tools/call", status: 200},
		{name: "person-cannot-act", method: "POST", path: "/mcp", key: "valid-person", body: initialize, identity: "person", status: 401},
		{name: "account-cannot-act", method: "POST", path: "/mcp", key: "valid-account", body: initialize, identity: "account_key", status: 401},
		{name: "agent-cannot-connect-tools", method: "POST", path: "/v1/connections/discover", key: "valid-agent", body: "{" + secret, identity: "agent", status: 403},
		{name: "person-connect-validation", method: "POST", path: "/v1/connections/discover", key: "valid-person", body: "{" + secret, identity: "person", status: 400},
		{name: "account-connect-validation", method: "POST", path: "/v1/connections/discover", key: "valid-account", body: "{" + secret, identity: "account_key", status: 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ids := make(chan string, 4)
			apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				ids <- r.Header.Get(requestlog.Header)
				if r.URL.Path == "/v1/me/card" {
					_ = json.NewEncoder(w).Encode(map[string]any{"agent_id": secret, "cards": []map[string]any{{"id": secret}}})
					return
				}
				me := kiffapi.Me{TenantID: secret, AgentID: secret, APIKey: true, CredentialID: secret, Resource: "https://mcp.kiff.dev/mcp"}
				switch strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ") {
				case "valid-agent", "kiff_oat_valid":
				case "kiff_oat_other":
					me.Resource = "https://mcp.kiff.dev/a/other/mcp"
				case "valid-person":
					me.AgentID, me.APIKey = "", false
					me.Capabilities = []string{"tools.connect"}
				case "valid-account":
					me.AgentID, me.Roles = "", []string{"admin"}
				default:
					w.WriteHeader(401)
					_, _ = w.Write([]byte(secret))
					return
				}
				_ = json.NewEncoder(w).Encode(me)
			}))
			defer apiServer.Close()
			api := kiffapi.New(apiServer.URL, nil)
			g := &gateway.Gateway{API: api, Catalog: loggingCatalog{}, Resource: "https://mcp.kiff.dev/mcp"}
			mux := http.NewServeMux()
			mux.Handle("/mcp", g.Server("test"))
			(&connection.Handler{API: api}).Register(mux)
			var b bytes.Buffer
			logger := slog.New(requestlog.Handler(slog.NewJSONHandler(&b, nil)))
			req := httptest.NewRequest(tc.method, tc.path+"?token="+secret, strings.NewReader(tc.body))
			if tc.key != "" {
				req.Header.Set("Authorization", "Bearer "+tc.key)
			}
			req.Header.Set(requestlog.Header, secret)
			req.Header.Set("Origin", tc.origin)
			w := httptest.NewRecorder()
			requestlog.Wrap(logger, mux).ServeHTTP(w, req)
			if w.Code != tc.status {
				t.Fatalf("HTTP %d, want %d: %s", w.Code, tc.status, w.Body.String())
			}
			if strings.Contains(b.String(), secret) || (tc.key != "" && strings.Contains(b.String(), tc.key)) {
				t.Fatalf("sensitive content leaked: %s", b.String())
			}
			var row map[string]any
			if err := json.Unmarshal(b.Bytes(), &row); err != nil {
				t.Fatal(err)
			}
			if row["status"] != float64(tc.status) || row["identity_type"] != tc.identity || row["path"] != tc.path || row["request_id"] != w.Header().Get(requestlog.Header) {
				t.Fatalf("wrong request log: %v", row)
			}
			if tc.operation == "" {
				if _, ok := row["mcp_operation"]; ok {
					t.Fatal("operation inferred from an unparsed/unauthenticated body")
				}
			} else if row["mcp_operation"] != tc.operation {
				t.Fatalf("wrong MCP operation: %v", row)
			}
			if tc.identity == "anonymous" && (row["tenant_ref"] != nil || row["agent_ref"] != nil) {
				t.Fatal("invented an identity for a rejected credential")
			}
			for len(ids) > 0 {
				if id := <-ids; id != w.Header().Get(requestlog.Header) {
					t.Fatalf("API correlation ID %q does not match gateway ID", id)
				}
			}
		})
	}
}
