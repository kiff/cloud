package gateway_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/kiff/cloud/gateway/internal/gateway"
)

// A decimal amount is converted exactly, or refused: never rounded.
func TestMinorUnits(t *testing.T) {
	for _, c := range []struct {
		in    string
		scale int
		want  int64
		ok    bool
	}{
		{"80.00", 2, 8000, true}, {"80.5", 2, 8050, true}, {"80", 2, 8000, true}, {"0.01", 2, 1, true},
		{"80.500", 2, 8050, true}, {"1234", 0, 1234, true}, {"0.001", 3, 1, true},
		{"80.001", 2, 0, false}, {"-5.00", 2, 0, false}, {"+5", 2, 0, false}, {"1e3", 2, 0, false},
		{" 5", 2, 0, false}, {"05.00", 2, 0, false}, {".5", 2, 0, false}, {"5.", 2, 0, false}, {"", 2, 0, false},
		{"90071992547409.92", 2, 0, false}, {"1.5", 0, 0, false},
	} {
		got, err := gateway.MinorUnits(c.in, c.scale)
		if (err == nil) != c.ok || got != c.want {
			t.Errorf("MinorUnits(%q, %d) = %d, %v", c.in, c.scale, got, err)
		}
	}
}

// A tool that takes its amount as a decimal string is counted in minor
// units, exactly: "5.05" draws 505 and reaches the tool as "5.05". One
// with more decimals than its unit takes, or not text, is invalid and unsent.
func TestADecimalAmountIsCountedExactly(t *testing.T) {
	r := newRig(t)
	tool := refundTool
	tool.AmountFormat, tool.AmountScale, tool.AmountUnit = gateway.FormatDecimal, 2, "EUR"
	tool.InputSchema = json.RawMessage(`{"type":"object","properties":{"amount_eur":{"type":"string"},"order":{"type":"string"}},"required":["amount_eur"]}`)
	r.gw.Catalog = catalog{tool}
	if got := r.call(t, "agent-key", map[string]any{"amount_eur": "5.05", "order": "o-1", gateway.OperationArg: "op-dec"}); got.isError {
		t.Fatalf("5.05 EUR: %+v", got)
	}
	if n, draws := r.api.totalDrawn(); n != 505 || draws != 1 {
		t.Fatalf("drawn %d in %d draws, want 505 in 1", n, draws)
	}
	if r.tool.count() != 1 || r.tool.calls[0]["amount_eur"] != "5.05" {
		t.Fatalf("the tool received %v", r.tool.calls)
	}
	// The call keeps what its amount meant when it was decided.
	var unit string
	var scale int
	if err := r.pool.QueryRow(context.Background(), `SELECT measure->>'unit', (measure->>'scale')::int FROM gateway.invocations WHERE operation_id = 'op-dec'`).Scan(&unit, &scale); err != nil || unit != "EUR" || scale != 2 {
		t.Fatalf("measure %q %d %v", unit, scale, err)
	}
	for _, bad := range []any{"5.055", 505, "5,05"} {
		_, out := r.rpc(t, "agent-key", "tools/call", map[string]any{"name": "issue_refund", "arguments": map[string]any{"amount_eur": bad, "order": "o-1"}})
		if e, _ := out["error"].(map[string]any); e == nil || !strings.Contains(fmt.Sprint(e["message"]), "amount_eur") {
			t.Errorf("%v: %v", bad, out)
		}
	}
	if r.tool.count() != 1 {
		t.Fatalf("a refused amount reached the tool: %d calls", r.tool.count())
	}
}
