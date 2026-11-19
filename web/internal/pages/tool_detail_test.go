package pages

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/kiff/cloud/web/internal/gatewayclient"
)

// A generic tool's operation shows where its amount and resource are
// read and what KIFF always sends. The resource row has no sheet: it is
// set when connecting.
func TestToolSettingsShowAGenericToolOperation(t *testing.T) {
	c := gatewayclient.Connection{Name: "stripe_refund", URL: "https://mcp.stripe.com", Mode: "sealed",
		AmountArg: "amount", AmountPath: "parameters.amount", AmountUnit: "EUR", AmountScale: 2,
		ResourceArg: "charge", ResourcePath: "parameters.charge",
		FixedArgs: json.RawMessage(`{"stripe_api_operation_id": "PostRefunds", "limit": 3}`)}
	rows := map[string]settingsRow{}
	for _, g := range toolSettingsGroups(c) {
		for _, r := range g.Rows {
			rows[r.Label] = r
		}
	}
	if r := rows["Amount"]; !strings.HasPrefix(r.Value, "parameters.amount") {
		t.Fatalf("amount row = %+v", r)
	}
	if r := rows["Each call is on"]; r.Value != "parameters.charge" || r.Href != "" {
		t.Fatalf("resource row = %+v", r)
	}
	if r := rows["Always sends"]; r.Value != "limit = 3, stripe_api_operation_id = PostRefunds" || !r.Mono {
		t.Fatalf("fixed row = %+v", r)
	}
	if _, ok := rows["Always sends"]; ok && fixedArgsText(nil) != "" {
		t.Fatal("no fixed arguments must show nothing")
	}
}
