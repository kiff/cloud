package gateway_test

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/kiff/cloud/gateway/internal/gateway"
)

var browsePayTool = gateway.Tool{Name: "browse_pay", Description: "Pay a checkout",
	InputSchema: json.RawMessage(`{"type":"object","properties":{"url":{"type":"string"},"ref":{"type":"string"}},"required":["url","ref"]}`),
	UpstreamURL: "http://planque.test/relay/t1/grt_1", UpstreamTool: "browse_pay", IdempotencyArg: "op", Mode: gateway.ModeVerify, Audience: "planque"}

var checkoutBlock = json.RawMessage(`{"block":{"v":"planque/checkout/v1","op":"x"},"sig":"c2lnbmVkLWJ5LXBsYW5xdWU"}`)

func payRig(t *testing.T, purchases bool) *rig {
	t.Helper()
	r := newRig(t)
	r.gw.Catalog = catalog{browsePayTool}
	r.gw.Purchases = purchases
	answer, _ := json.Marshal(map[string]any{"content": []map[string]any{{"type": "text", "text": "The checkout as read."}},
		"structuredContent": map[string]any{"browser_block": checkoutBlock}})
	r.tool.payDescribed = answer
	return r
}

// Invariant: with purchases on, a relay's pay is decided only with planque's signed read of its checkout, read back by a describe KIFF allowed on its own
// Category: authority
// Attack: a taf's pay is decided with no checkout, or one the taf wrote, so KIFF opens a purchase from the taf's words
// Refused: the describe is decided as "<call>:describe" with its own permit for exactly {url, ref, describe, op}; the pay's decision carries planque's block byte for byte; a pay planque won't describe is refused and nothing is bought
func TestAPayIsDescribedBeforeItsDecided(t *testing.T) {
	r := payRig(t, true)
	call := r.callNamed(t, "browse_pay", map[string]any{"url": "https://shop.example/checkout", "ref": "c7", gateway.OperationArg: "op-pay"})
	key, _ := call["invocation"].(string)
	if key == "" {
		t.Fatalf("no invocation: %v", call)
	}
	describe := key + ":describe"
	pr, ok := r.api.permits[describe]
	if !ok || pr.Tool != "browse_pay" || pr.Audience != "planque" {
		t.Fatalf("the describe's permit request: %+v", pr)
	}
	var args map[string]any
	_ = json.Unmarshal(pr.Arguments, &args)
	if len(args) != 4 || args["url"] != "https://shop.example/checkout" || args["ref"] != "c7" || args["describe"] != true || args["op"] != describe {
		t.Fatalf("the describe's arguments: %v", args)
	}
	if string(r.api.blocks[key]) != string(checkoutBlock) {
		t.Fatalf("the pay's block: %s", r.api.blocks[key])
	}
	if !slices.Equal(r.tool.tools, []string{"browse_pay", "browse_pay"}) {
		t.Fatalf("sent: %v", r.tool.tools)
	}
	if r.tool.calls[0]["describe"] != true || r.tool.calls[1]["describe"] != nil {
		t.Fatalf("the describe went first, then the pay: %v", r.tool.calls)
	}
}

func TestAPayPlanqueWontDescribeIsRefused(t *testing.T) {
	r := payRig(t, true)
	r.tool.payDescribed, _ = json.Marshal(map[string]any{"isError": true, "content": []map[string]any{{"type": "text", "text": "no checkout kept"}},
		"structuredContent": map[string]any{"code": "no_checkout"}})
	call := r.callNamed(t, "browse_pay", map[string]any{"url": "https://shop.example/checkout", "ref": "c7", gateway.OperationArg: "op-pay"})
	if call["state"] != "refused" || len(r.tool.tools) != 1 {
		t.Fatalf("refused: %v, sent %v", call, r.tool.tools)
	}
	if _, decided := r.api.blocks[call["invocation"].(string)]; decided {
		t.Fatal("the pay was decided")
	}
}

func TestWithPurchasesOffAPayIsNotDescribed(t *testing.T) {
	r := payRig(t, false)
	r.callNamed(t, "browse_pay", map[string]any{"url": "https://shop.example/checkout", "ref": "c7", gateway.OperationArg: "op-pay"})
	if !slices.Equal(r.tool.tools, []string{"browse_pay"}) || r.tool.calls[0]["describe"] != nil {
		t.Fatalf("sent: %v %v", r.tool.tools, r.tool.calls)
	}
}
