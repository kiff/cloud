package gateway

import "testing"

// A tool a reviewed table opens for one value gives that narrow opening; one
// the table opens whole, or that no table names, gives none.
func TestANarrowOpeningComesFromTheTablesCategory(t *testing.T) {
	class, opens := Tool{Name: "status", App: "qonto", VendorTool: "change_client_invoice_status"}.ClassRecord()
	if class != string(ClassIrreversible) || string(opens) != `[{"argument":"operation","value":"finalize"}]` {
		t.Fatalf("change_client_invoice_status: %s %s", class, opens)
	}
	if class, opens := (Tool{Name: "send", App: "qonto", VendorTool: "send_client_invoice"}).ClassRecord(); class != string(ClassIrreversible) || opens != nil {
		t.Fatalf("send_client_invoice: %s %s", class, opens)
	}
	if class, opens := (Tool{Name: "refund"}).ClassRecord(); class != string(ClassMoney) || opens != nil {
		t.Fatalf("refund: %s %s", class, opens)
	}
}
