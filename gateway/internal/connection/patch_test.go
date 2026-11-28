package connection_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

// A relay brings a tool it registered up to what it registers today, in
// place (K4's app and destructive_hint, step 3's pins): only the key that
// registered it, only a relay's tool, never lowering its class, each change
// recorded beside the tool.
func TestARelayPatchesItsOwnToolWithoutLoweringItsClass(t *testing.T) {
	r := newRig(t, dev)
	ctx := context.Background()
	relay, direct := openToolServer(t), openToolServer(t)
	r.h.RelayURLs = []string{relay.URL + "/v1/relay"}
	tool := func(name, url, vendor string) map[string]any {
		return map[string]any{"name": name, "url": url, "tool": "refund", "vendor_tool": vendor, "mode": "verify", "audience": "https://planque.example/v1/relay",
			"idempotency_argument": "idem"}
	}
	// Registered before the relay sent an app, a hint or pins.
	for _, c := range []map[string]any{tool("invoice", relay.URL, "create_client_invoice"), tool("status", relay.URL, "change_client_invoice_status"), tool("direct", direct.URL, "create_client_invoice")} {
		if status, body := r.do(t, "connector-a", http.MethodPost, "/v1/connections", c); status != http.StatusCreated {
			t.Fatalf("connect %v: %d %s", c["name"], status, body)
		}
	}
	patch := func(key, name string, body any) (int, string) {
		return r.do(t, key, http.MethodPatch, "/v1/connections/"+name, body)
	}
	pins := []map[string]any{{"path": "status", "value": "draft"}}
	qonto := map[string]any{"app": "qonto", "destructive_hint": false, "vendor_pins": pins}

	// Only the key that registered it: not another connector, not an agent, not the owner's own session.
	for _, key := range []string{"connector-a2", "agent-a", "owner-a"} {
		if status, body := patch(key, "invoice", qonto); status != http.StatusForbidden {
			t.Fatalf("%s: %d %s", key, status, body)
		}
	}
	// Only a relay's tool, by its stored URL.
	if status, body := patch("connector-a", "direct", qonto); status != http.StatusConflict {
		t.Fatalf("a tool connected straight to KIFF: %d %s", status, body)
	}
	// All three fields, and nothing else.
	if status, body := patch("connector-a", "invoice", map[string]any{"app": "qonto"}); status != http.StatusBadRequest {
		t.Fatalf("one field: %d %s", status, body)
	}
	if status, body := patch("connector-a", "invoice", map[string]any{"app": "qonto", "destructive_hint": false, "vendor_pins": pins, "url": "https://evil.example"}); status != http.StatusBadRequest {
		t.Fatalf("another field: %d %s", status, body)
	}
	if status, body := patch("connector-a", "nope", qonto); status != http.StatusNotFound {
		t.Fatalf("no such tool: %d %s", status, body)
	}

	// "" to the app whose table names the tool: kept, with the pins.
	if status, body := patch("connector-a", "invoice", qonto); status != http.StatusNoContent {
		t.Fatalf("app and pins: %d %s", status, body)
	}
	invoice, err := r.store.Get(ctx, "tenant-a", "invoice")
	if err != nil || invoice.App != "qonto" || len(invoice.VendorPins) != 1 || invoice.VendorPins[0].Path != "status" {
		t.Fatalf("kept: %+v %v", invoice, err)
	}
	// Never changed after, nor taken away.
	for _, app := range []string{"stripe", ""} {
		if status, body := patch("connector-a", "invoice", map[string]any{"app": app, "destructive_hint": false, "vendor_pins": pins}); status != http.StatusConflict || !strings.Contains(body, "lower") {
			t.Fatalf("qonto to %q: %d %s", app, status, body)
		}
	}
	// An app whose table doesn't name the tool isn't given to it.
	if status, body := patch("connector-a", "status", map[string]any{"app": "stripe", "destructive_hint": false, "vendor_pins": []any{}}); status != http.StatusConflict {
		t.Fatalf("an app whose table doesn't name the tool: %d %s", status, body)
	}
	// The hint may be set, never cleared; a pin may be removed.
	if status, body := patch("connector-a", "invoice", map[string]any{"app": "qonto", "destructive_hint": true, "vendor_pins": []any{}}); status != http.StatusNoContent {
		t.Fatalf("set the hint, drop the pin: %d %s", status, body)
	}
	if status, body := patch("connector-a", "invoice", map[string]any{"app": "qonto", "destructive_hint": false, "vendor_pins": []any{}}); status != http.StatusConflict {
		t.Fatalf("clear the hint: %d %s", status, body)
	}
	// The same state again changes nothing and records nothing.
	if status, body := patch("connector-a", "invoice", map[string]any{"app": "qonto", "destructive_hint": true, "vendor_pins": []any{}}); status != http.StatusNoContent {
		t.Fatalf("unchanged: %d %s", status, body)
	}
	var n int
	var first string
	if err := r.store.Pool.QueryRow(ctx, `SELECT count(*), min(old::text) FROM gateway.tool_patches WHERE tenant_id = 'tenant-a' AND name = 'invoice'`).Scan(&n, &first); err != nil || n != 2 {
		t.Fatalf("recorded %d %v", n, err)
	}
	if !strings.Contains(first, `"app": ""`) {
		t.Fatalf("the first record's old values: %s", first)
	}
}
