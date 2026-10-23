package gateway_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/kiff/cloud/gateway/internal/gateway"
	"github.com/kiff/cloud/gateway/internal/kiffapi"
)

// One operation of a generic vendor tool as its own governed tool.
// Stripe's stripe_api_write takes the operation as an argument and the
// amount inside "parameters"; the connection fixes the operation and reads
// the amount and the charge there.

var stripeRefund = gateway.Tool{Name: "stripe_refund", Description: "Write data via any Stripe API POST operation.",
	InputSchema: json.RawMessage(`{"type":"object","properties":{` +
		`"stripe_api_operation_id":{"type":"string"},"parameters":{"type":"object"},"human_confirmation":{"type":"object"}},` +
		`"required":["stripe_api_operation_id"]}`),
	UpstreamURL: "http://tool.test/mcp", UpstreamTool: "stripe_api_write",
	AmountArg: "amount", AmountPath: "parameters.amount", AmountUnit: "EUR", AmountScale: 2,
	ResourceArg: "charge", ResourcePath: "parameters.charge",
	FixedArgs: json.RawMessage(`{"stripe_api_operation_id":"PostRefunds"}`)}

func refundCall(r *rig, t *testing.T, args map[string]any) map[string]any {
	t.Helper()
	_, out := r.rpc(t, "agent-key", "tools/call", map[string]any{"name": "stripe_refund", "arguments": args})
	return out
}

func TestFixedArgumentIsSentAndNestedAmountDecided(t *testing.T) {
	r := newRig(t)
	r.gw.Catalog = catalog{stripeRefund}
	out := refundCall(r, t, map[string]any{
		"parameters":         map[string]any{"amount": 2000, "charge": "ch_1", "reason": "requested_by_customer"},
		gateway.OperationArg: "op-refund-1",
	})
	if out["result"] == nil || r.tool.count() != 1 {
		t.Fatalf("got %v, tool calls %d", out, r.tool.count())
	}
	sent := r.tool.calls[0]
	if sent["stripe_api_operation_id"] != "PostRefunds" {
		t.Fatalf("the fixed operation was not sent: %v", sent)
	}
	if p, _ := sent["parameters"].(map[string]any); p["amount"] != float64(2000) || p["charge"] != "ch_1" || p["reason"] != "requested_by_customer" {
		t.Fatalf("the agent's parameters were not forwarded as given: %v", sent)
	}
	var params map[string]any
	for _, p := range r.api.params {
		params = p
	}
	if params["amount"] != int64(2000) || params["charge"] != "ch_1" || len(params) != 2 {
		t.Fatalf("proposal parameters = %v, want the nested amount and charge under their Card names", params)
	}
}

// The agent never sees the fixed argument, and the tool says KIFF sends it.
func TestFixedArgumentIsHiddenFromTheAgent(t *testing.T) {
	r := newRig(t)
	r.gw.Catalog = catalog{stripeRefund}
	status, out := r.rpc(t, "agent-key", "tools/list", nil)
	if status != http.StatusOK || out["result"] == nil {
		t.Fatalf("tools/list: %d %v", status, out)
	}
	var tool map[string]any
	for _, x := range out["result"].(map[string]any)["tools"].([]any) {
		if m := x.(map[string]any); m["name"] == "stripe_refund" {
			tool = m
		}
	}
	schema := tool["inputSchema"].(map[string]any)
	props := schema["properties"].(map[string]any)
	if _, ok := props["stripe_api_operation_id"]; ok {
		t.Fatalf("the fixed argument is offered to the agent: %v", props)
	}
	if _, ok := props["parameters"]; !ok {
		t.Fatalf("the agent's own arguments are missing: %v", props)
	}
	for _, req := range schema["required"].([]any) {
		if req == "stripe_api_operation_id" {
			t.Fatalf("the fixed argument is still required of the agent: %v", schema["required"])
		}
	}
	if !strings.Contains(tool["description"].(string), `KIFF always sends stripe_api_operation_id = "PostRefunds"`) {
		t.Fatalf("description = %q", tool["description"])
	}
}

// Attack: pick another operation through a tool fixed to refunds.
func TestAgentCannotSetAFixedArgument(t *testing.T) {
	r := newRig(t)
	r.gw.Catalog = catalog{stripeRefund}
	out := refundCall(r, t, map[string]any{
		"stripe_api_operation_id": "PostCoupons",
		"parameters":              map[string]any{"amount": 100, "charge": "ch_1"},
		gateway.OperationArg:      "op-override",
	})
	e, _ := out["error"].(map[string]any)
	if e == nil || e["code"] != float64(-32602) || !strings.Contains(e["message"].(string), "stripe_api_operation_id is set by KIFF") {
		t.Fatalf("got %v", out)
	}
	if r.tool.count() != 0 || len(r.api.params) != 0 {
		t.Fatalf("an override was decided or sent: tool %d, decided %d", r.tool.count(), len(r.api.params))
	}
}

// A missing amount is never zero: for a Stripe refund it would refund the
// whole charge.
func TestNestedAmountIsRequired(t *testing.T) {
	r := newRig(t)
	r.gw.Catalog = catalog{stripeRefund}
	for name, params := range map[string]any{
		"no amount":         map[string]any{"charge": "ch_1"},
		"text amount":       map[string]any{"amount": "2000", "charge": "ch_1"},
		"decimal amount":    map[string]any{"amount": 20.5, "charge": "ch_1"},
		"parameters a list": []any{2000},
		"no parameters":     nil,
	} {
		t.Run(name, func(t *testing.T) {
			args := map[string]any{gateway.OperationArg: "op-" + strings.ReplaceAll(name, " ", "-")}
			if params != nil {
				args["parameters"] = params
			}
			out := refundCall(r, t, args)
			e, _ := out["error"].(map[string]any)
			if e == nil || e["code"] != float64(-32602) || !strings.Contains(e["message"].(string), "parameters.amount") {
				t.Fatalf("got %v", out)
			}
		})
	}
	if r.tool.count() != 0 || len(r.api.params) != 0 {
		t.Fatalf("a call without a valid amount was decided or sent: tool %d, decided %d", r.tool.count(), len(r.api.params))
	}
}

func TestNestedResourceIsRequired(t *testing.T) {
	r := newRig(t)
	r.gw.Catalog = catalog{stripeRefund}
	out := refundCall(r, t, map[string]any{"parameters": map[string]any{"amount": 2000}, gateway.OperationArg: "op-no-charge"})
	e, _ := out["error"].(map[string]any)
	if e == nil || !strings.Contains(e["message"].(string), "parameters.charge is required") {
		t.Fatalf("got %v", out)
	}
	if r.tool.count() != 0 {
		t.Fatalf("sent without its resource")
	}
}

// The fixed arguments are part of the bytes forwarded, so a verify-mode
// permit binds them (the forwardArgs invariant).
func TestPermitBindsTheFixedArguments(t *testing.T) {
	r := newRig(t)
	verify := refundTool
	verify.Mode, verify.Audience = gateway.ModeVerify, "https://refunds.example.com/mcp"
	verify.InputSchema = json.RawMessage(`{"type":"object","properties":{"amount_eur":{"type":"integer"},"order":{"type":"string"},"channel":{"type":"string"}},"required":["amount_eur"]}`)
	verify.FixedArgs = json.RawMessage(`{"channel":"support"}`)
	r.gw.Catalog = catalog{verify}
	if got := r.call(t, "agent-key", amount(500, "op-fixed-permit")); got.isError || r.tool.count() != 1 {
		t.Fatalf("%+v, tool calls = %d", got, r.tool.count())
	}
	key := strings.TrimPrefix(r.tool.permits[0], "permit-for-")
	if pr := r.api.permits[key]; !bytes.Equal(pr.Arguments, r.tool.raw[0]) || !bytes.Contains(pr.Arguments, []byte(`"channel":"support"`)) {
		t.Fatalf("permit over %s, forwarded %s", pr.Arguments, r.tool.raw[0])
	}
}

// A connection without paths or fixed arguments reads top-level arguments
// exactly as before.
func TestTopLevelAmountUnchanged(t *testing.T) {
	r := newRig(t)
	if got := r.call(t, "agent-key", amount(700, "op-top")); got.isError {
		t.Fatalf("%+v", got)
	}
	var params map[string]any
	for _, p := range r.api.params {
		params = p
	}
	if params["amount_eur"] != int64(700) {
		t.Fatalf("params = %v", params)
	}
	if _, fixed := r.tool.calls[0]["stripe_api_operation_id"]; fixed {
		t.Fatalf("a fixed argument leaked into an ordinary tool: %v", r.tool.calls[0])
	}
}

// A held call to a tool with fixed arguments is listed by
// kiff_pending without them, and the listed call collects it once.
func TestPendingCollectsAHeldCallWithFixedArguments(t *testing.T) {
	r := newRig(t)
	tool := stripeRefund
	tool.AmountArg = "amount_eur" // the fake API holds amount_eur above 1000
	r.gw.Catalog = catalog{tool}
	held := refundCall(r, t, map[string]any{"parameters": map[string]any{"amount": 5000, "charge": "ch_1"}, gateway.OperationArg: "op-held-refund"})
	if res, _ := held["result"].(map[string]any); res == nil || r.tool.count() != 0 {
		t.Fatalf("hold: %v", held)
	}
	_, calls := r.pending(t, "agent-key")
	if len(calls) != 1 || calls[0].State != "held" {
		t.Fatalf("calls: %+v", calls)
	}
	c := calls[0]
	if _, listed := c.Arguments["stripe_api_operation_id"]; listed || c.Arguments["parameters"] == nil {
		t.Fatalf("listed arguments: %v", c.Arguments)
	}
	r.api.approveAll()
	r.api.holds = map[string]kiffapi.Hold{c.ExceptionID: {ExceptionID: c.ExceptionID, Status: "approved"}}
	args := c.Arguments
	args[gateway.OperationArg] = c.OperationID
	out := refundCall(r, t, args)
	if res, _ := out["result"].(map[string]any); res == nil || res["isError"] == true || r.tool.count() != 1 {
		t.Fatalf("collect: %v, tool calls = %d", out, r.tool.count())
	}
	if r.tool.calls[0]["stripe_api_operation_id"] != "PostRefunds" {
		t.Fatalf("sent: %v", r.tool.calls[0])
	}
	refundCall(r, t, args)
	if r.tool.count() != 1 {
		t.Fatalf("collected twice: tool calls = %d", r.tool.count())
	}
}
