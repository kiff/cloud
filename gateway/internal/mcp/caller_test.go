package mcp_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kiff/cloud/gateway/internal/mcp"
)

// eliciting asks for a URL elicitation on every call and records what
// the server told it about the caller.
type eliciting struct {
	saw   mcp.Caller
	asked bool
}

func (*eliciting) ListTools(context.Context) ([]mcp.Tool, error) { return nil, nil }
func (e *eliciting) CallTool(ctx context.Context, name string, _ json.RawMessage) (json.RawMessage, error) {
	e.saw = mcp.CallerFrom(ctx)
	e.asked = mcp.RequestURLElicitation(ctx, mcp.URLElicitation{ID: "kiff-exc-1", URL: "https://app.example/needs-you/exc-1", Message: "Open KIFF to answer."})
	return mcp.TextResult("held", true), nil
}

const withURL = `"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":{"name":"claude-code","version":"2.1.287"},"io.modelcontextprotocol/clientCapabilities":{"elicitation":{"form":{},"url":{}}}}`

// A modern client that declares URL mode gets an
// input_required result that asks it to open the link; any other client,
// and a call that already carries the client's answer, gets the result.
func TestServerURLElicitation(t *testing.T) {
	h := &eliciting{}
	srv := httptest.NewServer(&mcp.Server{Name: "t", Version: "1", Auth: func(*http.Request) (mcp.Handler, error) { return h, nil }})
	t.Cleanup(srv.Close)
	hdr := map[string]string{"MCP-Protocol-Version": "2026-07-28", "Mcp-Method": "tools/call", "Mcp-Name": "refund"}
	call := func(params string) map[string]any {
		_, out := modernPost(t, srv.URL, hdr, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"refund",`+params+`}}`)
		res, _ := out["result"].(map[string]any)
		return res
	}

	res := call(withURL)
	if h.saw.Name != "claude-code" || h.saw.Version != "2.1.287" || !h.saw.URLElicitation || !h.asked {
		t.Fatalf("caller: %+v asked=%v", h.saw, h.asked)
	}
	req, _ := res["inputRequests"].(map[string]any)["kiff-exc-1"].(map[string]any)
	p, _ := req["params"].(map[string]any)
	if res["resultType"] != "input_required" || res["requestState"] != "kiff-exc-1" || req["method"] != "elicitation/create" ||
		p["mode"] != "url" || p["url"] != "https://app.example/needs-you/exc-1" || res["content"] != nil {
		t.Fatalf("input_required: %v", res)
	}

	// The client's answer comes back on the same call: whatever it is,
	// the call is answered with its result and not asked again.
	for _, action := range []string{"accept", "decline", "cancel"} {
		res = call(`"inputResponses":{"kiff-exc-1":{"action":"` + action + `"}},"requestState":"kiff-exc-1",` + withURL)
		if res["resultType"] != "complete" || res["content"] == nil || h.asked || h.saw.Responses["kiff-exc-1"] != action {
			t.Errorf("%s: %v asked=%v", action, res, h.asked)
		}
	}

	noURL := `"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":{"name":"x","version":"1"},"io.modelcontextprotocol/clientCapabilities":{"elicitation":{"form":{}}}}`
	if res = call(noURL); res["resultType"] != "complete" || h.asked {
		t.Errorf("form-only client: %v", res)
	}

	_, out := modernPost(t, srv.URL, map[string]string{"MCP-Protocol-Version": "2025-11-25"},
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"refund"}}`)
	if res, _ := out["result"].(map[string]any); res == nil || res["inputRequests"] != nil || h.asked {
		t.Errorf("legacy client: %v", out)
	}
}
