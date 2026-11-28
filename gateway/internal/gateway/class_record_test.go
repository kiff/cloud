package gateway

import "testing"

// A tool a reviewed table opens for one value gives that narrow opening; one
// the table opens whole, or that no table names, gives none; one whose
// table lowers its class on a condition says so.
func TestANarrowOpeningComesFromTheTablesCategory(t *testing.T) {
	r := Tool{Name: "status", App: "qonto", VendorTool: "change_client_invoice_status"}.ClassRecord()
	if r.Class != string(ClassIrreversible) || string(r.Opens) != `[{"argument":"operation","value":"finalize"}]` || r.Conditioned {
		t.Fatalf("change_client_invoice_status: %+v", r)
	}
	if r := (Tool{Name: "send", App: "qonto", VendorTool: "send_client_invoice"}).ClassRecord(); r.Class != string(ClassIrreversible) || r.Opens != nil || r.Conditioned {
		t.Fatalf("send_client_invoice: %+v", r)
	}
	if r := (Tool{Name: "invoice", App: "qonto", VendorTool: "create_client_invoice"}).ClassRecord(); r.Class != string(ClassIrreversible) || r.Opens != nil || !r.Conditioned {
		t.Fatalf("create_client_invoice: %+v", r)
	}
	if r := (Tool{Name: "refund"}).ClassRecord(); r.Class != string(ClassMoney) || r.Opens != nil || r.Conditioned {
		t.Fatalf("refund: %+v", r)
	}
}
