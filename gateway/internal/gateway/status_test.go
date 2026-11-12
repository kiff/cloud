package gateway_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/kiff/cloud/gateway/internal/gateway"
	"github.com/kiff/cloud/gateway/internal/mcp"
)

// vendor is a payment provider whose refund write can take effect and
// lose its answer, or answer an error, and whose list_refunds read lists
// the refunds it holds with the operation id the connection wrote on each.
type vendor struct {
	mu sync.Mutex
	// lands: a refund write takes effect.
	lands bool
	// answers is how a write answers: "lost" or "error".
	answers string
	// others are refunds made elsewhere, listed with the call's own.
	others []map[string]any
	// readFails makes list_refunds answer an error.
	readFails bool
	refunds   []map[string]any
	writes    int
	reads     int
}

func (v *vendor) CallTool(_ context.Context, _, _, tool string, args json.RawMessage) (json.RawMessage, error) {
	var a map[string]any
	_ = json.Unmarshal(args, &a)
	v.mu.Lock()
	defer v.mu.Unlock()
	switch tool {
	case "refund":
		v.writes++
		if v.lands {
			v.refunds = append(v.refunds, map[string]any{"id": fmt.Sprintf("re_%d", v.writes), "amount": a["amount_eur"],
				"order": a["order"], "metadata": map[string]any{"kiff_op": a["idempotency_key"]}})
		}
		if v.answers == "error" {
			b, _ := json.Marshal(map[string]any{"content": []map[string]any{{"type": "text", "text": "500: the processor had an internal error"}}, "isError": true})
			return b, nil
		}
		return nil, &mcp.CallError{Sent: true, Err: errors.New("response lost")}
	case "list_refunds":
		v.reads++
		if v.readFails {
			return mcp.TextResult("503: unavailable", true), nil
		}
		data := append(append([]map[string]any{}, v.others...), v.refunds...)
		text, _ := json.Marshal(map[string]any{"object": "list", "data": data})
		return mcp.TextResult(string(text), false), nil
	}
	return nil, errors.New("no such tool " + tool)
}

var listRefunds = gateway.Tool{Name: "list_refunds", Description: "List an order's refunds",
	InputSchema: json.RawMessage(`{"type":"object","properties":{"order":{"type":"string"}},"required":["order"]}`),
	UpstreamURL: refundTool.UpstreamURL, UpstreamTool: "list_refunds", ReadOnly: true}

// checkedRig is a rig whose refund moves money and names a status read,
// with no wait before the read.
func checkedRig(t *testing.T, v *vendor) *rig {
	t.Helper()
	r := newRig(t)
	r.gw.Forward = v
	refund := refundTool
	refund.Money = true
	refund.Status = &gateway.StatusRead{Tool: "list_refunds", Args: map[string]string{"order": "$args.order"}, Match: "data[0].metadata.kiff_op"}
	r.gw.Catalog = catalog{refund, listRefunds}
	wait := gateway.StatusWait
	gateway.StatusWait = 0
	t.Cleanup(func() { gateway.StatusWait = wait })
	return r
}

type stored struct {
	key, state, outcome, toolError, message string
	evidence                                map[string]any
}

func storedCall(t *testing.T, r *rig, op string) stored {
	t.Helper()
	var s stored
	var ev []byte
	if err := r.pool.QueryRow(context.Background(), `SELECT key, state, tool_outcome, tool_error, message, status_evidence
		FROM gateway.invocations WHERE operation_id = $1`, op).Scan(&s.key, &s.state, &s.outcome, &s.toolError, &s.message, &ev); err != nil {
		t.Fatal(err)
	}
	if ev != nil {
		_ = json.Unmarshal(ev, &s.evidence)
	}
	return s
}

// Invariant: a money call whose answer was lost is looked for at the vendor first; found by its own operation id, it's done and nothing is asked.
// Category: integrity
// Attack: a refund that went through but lost its answer would wait for the owner's "Did this happen?"
// Refused: nothing is sent again; the call is recorded as done, with what the read found
func TestALostRefundFoundByItsOperationIDIsDoneAndNotAsked(t *testing.T) {
	v := &vendor{lands: true, answers: "lost"}
	r := checkedRig(t, v)
	got := r.call(t, "agent-key", orderCall(500, "o-1", "op-found"))
	if got.isError || !strings.Contains(got.text, "found it by its operation id") {
		t.Fatalf("the agent is told it went through: %+v", got)
	}
	s := storedCall(t, r, "op-found")
	if s.state != "forwarded" || s.outcome != "done" || s.message != gateway.FoundMessage {
		t.Fatalf("recorded %+v", s)
	}
	if s.evidence["read"] != "list_refunds" || s.evidence["record"] != "re_1" || s.evidence["was"] != "unknown" || s.evidence["match"] != "data[0].metadata.kiff_op" {
		t.Fatalf("evidence %v", s.evidence)
	}
	if v.writes != 1 || v.reads != 1 {
		t.Fatalf("sent %d, read %d: the write is never sent again", v.writes, v.reads)
	}
}

// Invariant: a record matches only by this call's own operation id, never by its amount or what it acted on.
// Category: integrity
// Attack: another refund of the same amount on the same order, made in the vendor's dashboard, reads as this call's
// Refused: the call stays unknown and is asked
func TestARecordWithTheSameAmountButAnotherOperationIDIsNotThisCall(t *testing.T) {
	v := &vendor{answers: "lost", others: []map[string]any{
		{"id": "re_dash", "amount": 500, "order": "o-1", "metadata": map[string]any{}},
		{"id": "re_other", "amount": 500, "order": "o-1", "metadata": map[string]any{"kiff_op": "another-call"}},
	}}
	r := checkedRig(t, v)
	got := r.call(t, "agent-key", orderCall(500, "o-1", "op-twin"))
	if !strings.Contains(got.text, "response was lost") {
		t.Fatalf("not found, so lost as before: %+v", got)
	}
	if s := storedCall(t, r, "op-twin"); s.state != "unknown" || s.evidence != nil {
		t.Fatalf("recorded %+v", s)
	}
	if v.reads != 1 {
		t.Fatalf("read %d", v.reads)
	}
}

// A lost call the read doesn't find, or whose read fails, stays unknown:
// not finding it is never proof it didn't happen.
func TestALostCallNotFoundOrUnreadStaysUnknown(t *testing.T) {
	for name, v := range map[string]*vendor{
		"not found":      {answers: "lost"},
		"the read fails": {answers: "lost", lands: true, readFails: true},
	} {
		t.Run(name, func(t *testing.T) {
			r := checkedRig(t, v)
			got := r.call(t, "agent-key", orderCall(500, "o-1", "op-lost"))
			if !strings.Contains(got.text, "response was lost") {
				t.Fatalf("%+v", got)
			}
			if s := storedCall(t, r, "op-lost"); s.state != "unknown" || s.evidence != nil {
				t.Fatalf("recorded %+v", s)
			}
			if v.writes != 1 {
				t.Fatalf("sent %d times", v.writes)
			}
		})
	}
}

// A vendor's error on a call that did take effect: found, it's done, and
// the vendor's error is kept.
func TestARefusedRefundThatWentThroughIsFound(t *testing.T) {
	v := &vendor{lands: true, answers: "error"}
	r := checkedRig(t, v)
	got := r.call(t, "agent-key", orderCall(500, "o-1", "op-err"))
	if got.isError || !strings.Contains(got.text, "found it by its operation id") {
		t.Fatalf("%+v", got)
	}
	s := storedCall(t, r, "op-err")
	if s.state != "forwarded" || s.outcome != "done" || s.evidence["was"] != "error" || !strings.Contains(s.toolError, "internal error") {
		t.Fatalf("recorded %+v", s)
	}
}

// A vendor's error on a call that didn't take effect stays the vendor's
// error, asked as before.
func TestARefusedRefundNotFoundStaysRefused(t *testing.T) {
	v := &vendor{answers: "error"}
	r := checkedRig(t, v)
	r.call(t, "agent-key", orderCall(500, "o-1", "op-no"))
	if s := storedCall(t, r, "op-no"); s.state != "forwarded" || s.outcome != "error" || s.evidence != nil || v.reads != 1 {
		t.Fatalf("recorded %+v, read %d", s, v.reads)
	}
}

// Without a status read, or on an operation that moves no money, nothing
// is read: the call ends as before.
func TestNoStatusReadNoMoneyNothingIsRead(t *testing.T) {
	for name, edit := range map[string]func(*gateway.Tool){
		"no status read": func(t *gateway.Tool) { t.Status = nil },
		"no money":       func(t *gateway.Tool) { t.Money = false },
	} {
		t.Run(name, func(t *testing.T) {
			v := &vendor{lands: true, answers: "lost"}
			r := checkedRig(t, v)
			refund := r.gw.Catalog.(catalog)[0]
			edit(&refund)
			r.gw.Catalog = catalog{refund, listRefunds}
			r.call(t, "agent-key", orderCall(500, "o-1", "op-plain"))
			if s := storedCall(t, r, "op-plain"); s.state != "unknown" || v.reads != 0 {
				t.Fatalf("recorded %+v, read %d", s, v.reads)
			}
		})
	}
}
