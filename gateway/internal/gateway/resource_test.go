package gateway_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/kiff/cloud/gateway/internal/gateway"
	"github.com/kiff/cloud/gateway/internal/kiffapi"
)

// A tool's resource argument reaches KIFF with the amount, so a
// Card limited to named orders can read which order a call is on.

var scopedRefundTool = func() gateway.Tool {
	t := refundTool
	t.ResourceArg = "order"
	return t
}()

func TestResourceArgumentIsSentWithTheAmount(t *testing.T) {
	r := newRig(t)
	r.gw.Catalog = catalog{scopedRefundTool}
	args := amount(500, "op-scoped")
	args["order"] = "ord_123"
	if got := r.call(t, "agent-key", args); got.isError {
		t.Fatalf("call: %+v", got)
	}
	var params map[string]any
	for _, p := range r.api.params {
		params = p
	}
	if params["order"] != "ord_123" || params["amount_eur"] != int64(500) || len(params) != 2 {
		t.Fatalf("proposal parameters = %v, want the amount and the order only", params)
	}
}

func TestResourceArgumentMustBeText(t *testing.T) {
	r := newRig(t)
	r.gw.Catalog = catalog{scopedRefundTool}
	for name, value := range map[string]any{
		"missing":  nil,
		"a number": 123,
		"empty":    "",
		"too long": strings.Repeat("x", gateway.MaxResourceLength+1),
	} {
		t.Run(name, func(t *testing.T) {
			args := amount(500, "op-"+strings.ReplaceAll(name, " ", "-"))
			delete(args, "order")
			if value != nil {
				args["order"] = value
			}
			// Refused as invalid parameters, as a bad amount is.
			_, out := r.rpc(t, "agent-key", "tools/call", map[string]any{"name": "issue_refund", "arguments": args})
			e, _ := out["error"].(map[string]any)
			if e == nil || e["code"] != float64(-32602) || !strings.Contains(e["message"].(string), "order is required and must be text") {
				t.Fatalf("got %v", out)
			}
		})
	}
	if r.tool.count() != 0 || len(r.api.params) != 0 {
		t.Fatalf("an invalid call was decided or sent: tool %d, decided %d", r.tool.count(), len(r.api.params))
	}
}

func TestContractDeclaresTheResourceArgument(t *testing.T) {
	y, err := gateway.Contract([]gateway.Tool{scopedRefundTool})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"required_parameters: [amount_eur, order]",
		"      - name: order\n        type: string\n        min_length: 1\n        max_length: 256\n",
	} {
		if !strings.Contains(y, want) {
			t.Errorf("missing %q in\n%s", want, y)
		}
	}
	// Without one, the contract is what it was.
	plain, _ := gateway.Contract([]gateway.Tool{refundTool})
	if !strings.Contains(plain, "    required_parameters: [amount_eur]\n    parameters:\n      - name: amount_eur\n        type: int\n        min: 0\n") ||
		strings.Contains(plain, "type: string") {
		t.Errorf("contract without a resource argument changed:\n%s", plain)
	}
	onlyResource := gateway.Tool{Name: "close_ticket", ResourceArg: "ticket"}
	if y, _ := gateway.Contract([]gateway.Tool{onlyResource}); !strings.Contains(y, "required_parameters: [ticket]") || strings.Contains(y, "type: int") {
		t.Errorf("a tool with only a resource argument:\n%s", y)
	}
	for _, bad := range []gateway.Tool{
		{Name: "a", ResourceArg: "not valid"},
		{Name: "a", AmountArg: "n", ResourceArg: "n"},
		{Name: "a", ResourceArg: gateway.OperationArg},
	} {
		if _, err := gateway.Contract([]gateway.Tool{bad}); err == nil {
			t.Errorf("resource argument %q accepted", bad.ResourceArg)
		}
	}
}

func TestCardLineNamesTheResources(t *testing.T) {
	r := newRig(t)
	r.gw.Catalog = catalog{scopedRefundTool}
	card := refundCard
	card.Cards = append([]kiffapi.Card(nil), refundCard.Cards...)
	card.Cards[0].Grants = []kiffapi.CardGrant{{Action: "issue_refund",
		Resources: &kiffapi.CardResources{Parameter: "order", Values: []string{"ord_1", "ord_2"}}}}
	r.api.card = card
	_, out := r.rpc(t, "agent-key", "tools/call", map[string]any{"name": gateway.CardTool, "arguments": map[string]any{}})
	if !strings.Contains(strings.ToLower(asText(out)), "only for order ord_1, ord_2") {
		t.Fatalf("kiff_card does not name the scope: %v", out)
	}
}

func asText(out map[string]any) string {
	res, _ := out["result"].(map[string]any)
	var b strings.Builder
	for _, c := range res["content"].([]any) {
		b.WriteString(c.(map[string]any)["text"].(string))
	}
	return b.String()
}

// A result of a tool with a resource argument names it, and where its
// value is in the arguments, never the value (kiff/tafs#167): forwarded,
// held, and every other state an invocation can be in. An agent's runtime
// reads the value there to record what the call was on. A reused
// operation id is refused before there is an invocation, so it has no key
// to file a value under, and names none.
func TestResultsNameTheResourceArgument(t *testing.T) {
	r := newRig(t)
	r.gw.Catalog = catalog{scopedRefundTool}
	forwarded := r.call(t, "agent-key", amount(500, "op-small"))
	held := r.call(t, "agent-key", amount(5000, "op-big"))
	for what, got := range map[string]result{"forwarded": forwarded, "held": held} {
		want(t, what, got, map[string]any{"resource_argument": "order", "resource_path": "order"})
		if strings.Contains(fmt.Sprint(got.call), "o-1") {
			t.Fatalf("%s: the call state carries the value: %v", what, got.call)
		}
	}
	if forwarded.call["state"] != "forwarded" || held.call["state"] != "held" {
		t.Fatalf("states: %v / %v", forwarded.call, held.call)
	}
	// A tool without one names none.
	r.gw.Catalog = catalog{refundTool}
	plain := r.call(t, "agent-key", amount(500, "op-plain"))
	if _, ok := plain.call["resource_argument"]; ok || plain.call["state"] != "forwarded" {
		t.Fatalf("no resource argument: %v", plain.call)
	}
}

// A nested resource is named with its path.
func TestNestedResourceIsNamedWithItsPath(t *testing.T) {
	r := newRig(t)
	r.gw.Catalog = catalog{stripeRefund}
	out := refundCall(r, t, map[string]any{
		"parameters":         map[string]any{"amount": 2000, "charge": "ch_1"},
		gateway.OperationArg: "op-nested",
	})
	res, _ := out["result"].(map[string]any)
	meta, _ := res["_meta"].(map[string]any)
	call, _ := meta[gateway.MetaKey].(map[string]any)
	if call["resource_argument"] != "charge" || call["resource_path"] != "parameters.charge" || call["state"] != "forwarded" {
		t.Fatalf("call state = %v", call)
	}
}
