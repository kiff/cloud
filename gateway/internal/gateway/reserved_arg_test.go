package gateway

import (
	"encoding/json"
	"strings"
	"testing"
)

// A tool stored with an argument named after one of KIFF's own parameters
// never lets the agent set them: KIFF's are written last, and that
// argument's value never reaches the decision.
func TestAnArgumentNamedAfterKIFFsOwnNeverSetsThem(t *testing.T) {
	money := Tool{Name: "refund_order", UpstreamTool: "refund_order", ResourceArg: "kiff_class"}
	p, err := money.params(map[string]any{"kiff_class": "read"})
	if err != nil {
		t.Fatal(err)
	}
	if p[ClassParam] != string(ClassMoney) {
		t.Errorf("the agent classed a money call: %v", p)
	}
	by := Tool{Name: "send_x", UpstreamTool: "send_client_invoice", ResourceArg: "kiff_class_by", Universal: true}
	p, err = by.params(map[string]any{"kiff_class_by": ClassByUnknown})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := p[ClassByParam]; ok || p[ClassParam] != string(ClassIrreversible) {
		t.Errorf("the agent set kiff_class_by: %v", p)
	}
	ask := Tool{Name: "refund_order", UpstreamTool: "refund_order", AmountArg: "kiff_owner_ask"}
	p, err = ask.params(map[string]any{"kiff_owner_ask": json.Number("5")})
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := p[OwnerAskParam]; ok && v != "" {
		t.Errorf("the agent's number stood in for the owner ask: %v", p)
	}
	// The contract refuses such a tool too.
	for _, tool := range []Tool{
		{Name: "a", ResourceArg: "kiff_class"},
		{Name: "b", AmountArg: "kiff_amount_unverified"},
		{Name: "c", IdempotencyArg: "KIFF_op"},
	} {
		if _, err := Contract([]Tool{tool}); err == nil || !strings.Contains(err.Error(), "kiff_") {
			t.Errorf("%s: %v", tool.Name, err)
		}
	}
	if _, err := Contract([]Tool{{Name: "d", ResourceArg: "order_id"}}); err != nil {
		t.Errorf("an ordinary tool: %v", err)
	}
	if !ReservedArg(" KIFF_x") || ReservedArg("kiffer") || ReservedArg("order_kiff_x") {
		t.Error("ReservedArg")
	}
}
