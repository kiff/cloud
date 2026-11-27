package gateway_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/kiff/cloud/gateway/internal/gateway"
)

// A planque browser call that doesn't fit its stored schema is refused as
// invalid params, and nothing is decided, drawn or sent: the owner is never
// asked about a call the tool would refuse. The same arguments to a tool the
// check doesn't cover yet go on as before.
// Category: authority
// Attack: a taf sends a browse_act step the tool can't run (its field's words in option, no label), so the owner approves a card that shows the wrong words
// Refused: invalid params before any decision or hold
func TestABrowserCallThatDoesntFitItsSchemaIsRefusedBeforeTheHold(t *testing.T) {
	const planque = "https://planque.example"
	schema := json.RawMessage(`{"type":"object","required":["url","steps"],"properties":{"url":{"type":"string"},` +
		`"steps":{"type":"array","items":{"type":"object","oneOf":[` +
		`{"type":"object","required":["fill","label"],"additionalProperties":false,"properties":{"fill":{"type":"string"},"value":{"type":"string"},"label":{"type":"string"}}},` +
		`{"type":"object","required":["click","label"],"additionalProperties":false,"properties":{"click":{"type":"string"},"label":{"type":"string"}}}]}}}}`)
	browser := gateway.Tool{Name: "browse_act", Description: "Fill in and click on a page", InputSchema: schema, UpstreamTool: "browse_act",
		Mode: gateway.ModeRelay, UpstreamURL: planque + "/mcp/t1/grt_1"}
	browser.Audience = browser.UpstreamURL
	other := browser
	other.Name, other.UpstreamTool = "browse_like", "browse_like"
	other.UpstreamURL, other.Audience = "https://relay.customer.example/mcp/t1/grt_1", "https://relay.customer.example/mcp/t1/grt_1"
	held := map[string]any{"url": "https://www.httpbin.org/forms/post", "steps": []any{map[string]any{"fill": "f1", "value": "Test", "option": "Customer name:"}}}

	for i, c := range []struct {
		name    string
		tool    gateway.Tool
		refused bool
	}{
		{"planque's browser tool", browser, true},
		{"a tool the check doesn't cover", other, false},
	} {
		r := newRig(t)
		r.gw.Relays = []string{planque + "/"}
		r.gw.Catalog = catalog{c.tool}
		args := map[string]any{}
		for k, v := range held {
			args[k] = v
		}
		args[gateway.OperationArg] = fmt.Sprintf("op-schema-%d", i)
		_, out := r.rpc(t, "agent-key", "tools/call", map[string]any{"name": c.tool.Name, "arguments": args})
		e, _ := out["error"].(map[string]any)
		refused := e != nil && e["code"] == float64(-32602) && strings.Contains(fmt.Sprint(e["message"]), "the arguments don't fit the tool's schema: arguments.steps.1")
		if refused != c.refused {
			t.Fatalf("%s: refused %v, want %v (%v)", c.name, refused, c.refused, out)
		}
		if c.refused && (len(r.tool.calls) != 0 || len(r.api.drawn) != 0) {
			t.Fatalf("%s: refused, but sent %d calls and drew %v", c.name, len(r.tool.calls), r.api.drawn)
		}
	}
}
