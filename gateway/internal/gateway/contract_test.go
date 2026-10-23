package gateway_test

import (
	"strings"
	"testing"

	"github.com/kiff/cloud/gateway/internal/gateway"
)

// The generated contract carries what the spike against apps/api showed
// it needs; acceptance against the live API is in the slice 1 notes.
func TestContract(t *testing.T) {
	y, err := gateway.Contract([]gateway.Tool{refundTool, {Name: "deploy_service"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"domain: gateway", "entity: ToolCall", "states: [requested]",
		"  - name: issue_refund\n", "required_parameters: [amount_eur]", "type: int",
		"  - name: deploy_service\n", "executor: cloud.proxy", "requires_authority: true",
		"gateway_agent: [tools.issue_refund, tools.deploy_service]",
	} {
		if !strings.Contains(y, want) {
			t.Errorf("missing %q in\n%s", want, y)
		}
	}
	if strings.Count(y, "requires_authority: true") != 2 || strings.Count(y, "type: int") != 1 {
		t.Errorf("per-tool fields wrong:\n%s", y)
	}
	ro, _ := gateway.Contract([]gateway.Tool{{Name: "get_balance", ReadOnly: true}, refundTool})
	if !strings.Contains(ro, "  - name: get_balance\n    allowed_states: [requested]\n    required_permissions: [tools.get_balance]\n    risk: low\n") ||
		strings.Count(ro, "risk: medium") != 1 {
		t.Errorf("read-only risk:\n%s", ro)
	}
	// The read-only tools, and only those, go to setup_reader too
	// (kiff/tafs#138); a contract with none has no setup_reader.
	if !strings.Contains(ro, "    gateway_agent: [tools.get_balance, tools.issue_refund]\n    setup_reader: [tools.get_balance]\n") {
		t.Errorf("setup_reader:\n%s", ro)
	}
	if strings.Contains(y, gateway.SetupReaderRole) {
		t.Errorf("setup_reader with no read-only tool:\n%s", y)
	}
	if _, err := gateway.Contract([]gateway.Tool{{Name: "rm -rf"}}); err == nil {
		t.Error("an unsafe tool name was accepted")
	}
}

// A declared amount unit reaches the domain, where apps/api puts it on the
// Card's limits; a unit that is not letters, or a scale past 6, is refused.
func TestContractCarriesTheAmountUnit(t *testing.T) {
	y, err := gateway.Contract([]gateway.Tool{{Name: "issue_refund", AmountArg: "amount_cents", AmountUnit: "EUR", AmountScale: 2}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(y, "      - name: amount_cents\n        type: int\n        min: 0\n        unit: EUR\n        scale: 2\n") {
		t.Fatalf("contract without the unit:\n%s", y)
	}
	if y, _ := gateway.Contract([]gateway.Tool{{Name: "issue_refund", AmountArg: "amount_cents"}}); strings.Contains(y, "unit:") {
		t.Fatalf("a tool without a unit got one:\n%s", y)
	}
	for _, bad := range []gateway.Tool{
		{Name: "a", AmountArg: "n", AmountUnit: "E-UR", AmountScale: 2},
		{Name: "a", AmountArg: "n", AmountUnit: "EUR", AmountScale: 7},
	} {
		if _, err := gateway.Contract([]gateway.Tool{bad}); err == nil {
			t.Errorf("unit %q scale %d accepted", bad.AmountUnit, bad.AmountScale)
		}
	}
}
