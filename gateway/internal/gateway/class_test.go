package gateway

import (
	"encoding/json"
	"os"
	"testing"
)

// K4: KIFF's class for each of Qonto's tools is the reviewer's, never lower
// (classes/qonto.json). planque's read_only stands in for the vendor's
// readOnlyHint, as planque sends it: true only for the file's reads.
func TestKIFFClassesQontoAsTheReviewerDoes(t *testing.T) {
	raw, err := os.ReadFile("classes/qonto.json")
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Tools []struct {
			Name  string `json:"name"`
			Class Class  `json:"class"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	if len(f.Tools) < 61 {
		t.Fatalf("the file has %d tools", len(f.Tools))
	}
	for _, tool := range f.Tools {
		in := ClassedTool{App: "qonto", Name: tool.Name, ReadOnly: tool.Class == ClassRead}
		if got := Classify(in); got != tool.Class {
			t.Errorf("%s: KIFF says %s, the reviewer %s", tool.Name, got, tool.Class)
		}
		// Without the app's table, the rules alone never class it lower.
		if got := Classify(ClassedTool{Name: tool.Name, ReadOnly: tool.Class == ClassRead}); classRank[got] < classRank[tool.Class] {
			t.Errorf("%s by the rules alone: %s, under the reviewer's %s", tool.Name, got, tool.Class)
		}
	}
}

// A read needs planque's read_only and a read verb; a name never lowers what
// planque says; the profile's money mark makes money; an unknown name is a
// write, never a read.
func TestTheRulesNeverMakeAReadOnAGuess(t *testing.T) {
	for _, c := range []struct {
		in   ClassedTool
		want Class
	}{
		{ClassedTool{Name: "get_balance", ReadOnly: true}, ClassRead},
		{ClassedTool{Name: "get_balance", ReadOnly: false}, ClassWrite},
		{ClassedTool{Name: "summarize", ReadOnly: true}, ClassWrite},
		{ClassedTool{Name: "do_thing"}, ClassWrite},
		{ClassedTool{Name: "get_thing", ReadOnly: true, Money: true}, ClassMoney},
		{ClassedTool{Name: "delete_customer"}, ClassDelete},
		{ClassedTool{Name: "send_email"}, ClassIrreversible},
		{ClassedTool{Name: "create_refund"}, ClassMoney},
		{ClassedTool{Name: "list_transfers", ReadOnly: true}, ClassRead},
		{ClassedTool{Name: "create_transfer"}, ClassMoney},
		{ClassedTool{Name: "create_invoice"}, ClassIrreversible},
		{ClassedTool{App: "qonto", Name: "a_tool_qonto_adds_later"}, ClassWrite},
	} {
		if got := Classify(c.in); got != c.want {
			t.Errorf("%+v: %s, want %s", c.in, got, c.want)
		}
	}
	if !ClassMoney.Gated() || !ClassDelete.Gated() || !ClassIrreversible.Gated() || ClassWrite.Gated() || ClassRead.Gated() {
		t.Error("only irreversible, delete and money are refused until opened")
	}
	if err := ClassTablesErr(); err != nil {
		t.Fatal(err)
	}
}

// A call's class can depend on its arguments, as the reviewer's table says:
// Qonto's create_client_invoice is a write only with status draft, and
// change_client_invoice_status carries its operation to KIFF, derived from
// its flags, so an opening can name finalize alone. The class and those values reach KIFF in the call's
// parameters, set by the gateway over anything the agent sent.
func TestACallsClassAndItsArgumentsReachKIFF(t *testing.T) {
	create := Tool{Name: "create_client_invoice_q1", UpstreamTool: "create_client_invoice", App: "qonto"}
	if c, _ := create.CallClass(map[string]any{"status": "draft"}); c != ClassWrite {
		t.Errorf("a draft invoice: %s", c)
	}
	for _, args := range []map[string]any{{}, {"status": "finalized"}, {"status": 1}} {
		if c, _ := create.CallClass(args); c != ClassIrreversible {
			t.Errorf("an invoice with %v: %s", args, c)
		}
	}
	status := Tool{Name: "change_client_invoice_status_q1", UpstreamTool: "change_client_invoice_status", App: "qonto"}
	// Its operation is the one flag set true, derived by KIFF: finalize alone
	// opens send_invoices; anything else is another operation, or mixed.
	for _, tc := range []struct {
		args map[string]any
		want string
	}{
		{map[string]any{"id": "inv_1", "finalize": true}, "finalize"},
		{map[string]any{"id": "inv_1", "finalize": true, "cancel": false, "unmark_as_paid": nil}, "finalize"},
		{map[string]any{"id": "inv_1", "cancel": true}, "cancel"},
		{map[string]any{"id": "inv_1", "unmark_as_paid": true}, "unmark_as_paid"},
		{map[string]any{"id": "inv_1", "finalize": true, "cancel": true}, OperationMixed},
		{map[string]any{"id": "inv_1"}, OperationMixed},
		{map[string]any{"id": "inv_1", "finalize": "true"}, OperationMixed},
		// The agent's own operation is never read.
		{map[string]any{"id": "inv_1", "operation": "finalize", "cancel": true}, "cancel"},
		{map[string]any{"id": "inv_1", "operation": "finalize"}, OperationMixed},
	} {
		c, args := status.CallClass(tc.args)
		if c != ClassIrreversible || len(args) != 1 || args["operation"] != tc.want {
			t.Errorf("%v: %s %v, want operation %s", tc.args, c, args, tc.want)
		}
	}
	// Without the app, no table: the rules, and no argument values.
	if c, args := (Tool{Name: "create_client_invoice"}).CallClass(map[string]any{"status": "draft"}); c != ClassIrreversible || args != nil {
		t.Errorf("no app: %s %v", c, args)
	}
	// The parameters KIFF decides on carry both, whatever the agent sent.
	p, err := status.params(map[string]any{"finalize": true, ClassParam: "read", ClassArgsParam: map[string]any{"operation": "cancel"}})
	if err != nil {
		t.Fatal(err)
	}
	if p[ClassParam] != "irreversible" {
		t.Errorf("class sent: %v", p[ClassParam])
	}
	if a, _ := p[ClassArgsParam].(map[string]string); a["operation"] != "finalize" {
		t.Errorf("arguments sent: %v", p[ClassArgsParam])
	}
	// A read sends its class too, and no argument values.
	read := Tool{Name: "list_clients_q1", UpstreamTool: "list_clients", App: "qonto", ReadOnly: true}
	if p, _ := read.params(map[string]any{}); p[ClassParam] != "read" || p[ClassArgsParam] != nil {
		t.Errorf("a read's parameters: %v", p)
	}
}

func TestTheVendorsDestructiveHintOnlyRaises(t *testing.T) {
	cases := []struct {
		tool ClassedTool
		want Class
	}{
		{ClassedTool{Name: "update_label", Destructive: true}, ClassIrreversible},
		{ClassedTool{Name: "remove_label", Destructive: true}, ClassDelete},
		{ClassedTool{Name: "list_labels", ReadOnly: true, Destructive: true}, ClassIrreversible},
		{ClassedTool{Name: "create_transfer", Destructive: true}, ClassMoney},
		{ClassedTool{Name: "update_label"}, ClassWrite},
		{ClassedTool{Name: "list_labels", ReadOnly: true}, ClassRead},
	}
	for _, c := range cases {
		if got := Classify(c.tool); got != c.want {
			t.Errorf("%+v: %s, want %s", c.tool, got, c.want)
		}
	}
}

func TestOnlyATableConditionedToolIsConditioned(t *testing.T) {
	if !(Tool{App: "qonto", Name: "create_client_invoice"}).Conditioned() {
		t.Fatal("create_client_invoice has the draft condition")
	}
	if (Tool{App: "qonto", Name: "list_client_invoices"}).Conditioned() || (Tool{Name: "create_client_invoice"}).Conditioned() {
		t.Fatal("no condition without one in the app's table")
	}
}
