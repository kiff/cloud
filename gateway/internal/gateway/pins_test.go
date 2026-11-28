package gateway

import (
	"encoding/json"
	"testing"
)

func TestOnlyPinsAPermitCanSignAreKept(t *testing.T) {
	ok := []VendorPin{{Path: "status", Value: "draft"}, {Path: "report_einvoicing", Value: false}, {Path: "metadata.kiff_op", Marker: true}}
	if msg := VendorPinsRefusal(ok); msg != "" {
		t.Fatal(msg)
	}
	for name, pins := range map[string][]VendorPin{
		"a number":            {{Path: "amount", Value: json.Number("5")}},
		"no value":            {{Path: "status"}},
		"a marker with value": {{Path: "m", Marker: true, Value: "x"}},
		"twice":               {{Path: "status", Value: "draft"}, {Path: "status", Value: "x"}},
		"an index":            {{Path: "lines.0.status", Value: "draft"}},
		"upper case":          {{Path: "Status", Value: "draft"}},
		"a hyphen":            {{Path: "a-b", Value: "draft"}},
		"too deep":            {{Path: "a.b.c.d.e", Value: "x"}},
		"nine":                make([]VendorPin, 9),
	} {
		if VendorPinsRefusal(pins) == "" {
			t.Errorf("%s: kept", name)
		}
	}
}

func TestAPinSitsOnlyOnAFieldTheSchemaLists(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{"status":{"type":"string"},"metadata":{"type":"object","properties":{"note":{"type":"string"}}}}}`)
	for path, want := range map[string]bool{"status": true, "metadata.note": true, "metadata": true, "report_einvoicing": false, "metadata.kiff_op": false, "status.x": false} {
		if got := PinInSchema(schema, path); got != want {
			t.Errorf("%s: %v, want %v", path, got, want)
		}
	}
}

// With its draft pin signed, create_client_invoice is a write because the
// permit signs status = draft, whatever the agent's own status says; with
// no pin signed, the agent's argument decides, as before.
func TestASignedDraftPinMakesTheInvoiceAWrite(t *testing.T) {
	pins := []VendorPin{{Path: "status", Value: "draft"}}
	signed := Tool{Name: "create_client_invoice_q1", UpstreamTool: "create_client_invoice", App: "qonto", VendorPins: pins, PinsSigned: true}
	for _, args := range []map[string]any{{}, {"status": "unpaid"}, {"status": "draft"}} {
		if c, _ := signed.CallClass(args); c != ClassWrite {
			t.Errorf("signed, %v: %s", args, c)
		}
	}
	unsigned := signed
	unsigned.PinsSigned = false
	if c, _ := unsigned.CallClass(map[string]any{}); c != ClassIrreversible {
		t.Errorf("unsigned, no status: %s", c)
	}
	if c, _ := unsigned.CallClass(map[string]any{"status": "draft"}); c != ClassWrite {
		t.Errorf("unsigned, status draft: %s", c)
	}
	// A pin of another value, or a marker there, never lowers it.
	other := signed
	other.VendorPins = []VendorPin{{Path: "status", Value: "unpaid"}}
	if c, _ := other.CallClass(map[string]any{"status": "draft"}); c != ClassIrreversible {
		t.Errorf("a pin of unpaid: %s", c)
	}
}
