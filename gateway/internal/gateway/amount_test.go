package gateway_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/kiff/cloud/gateway/internal/gateway"
)

// A write can act on an amount it doesn't carry: answering a dispute
// concedes the dispute's amount, and the call names only the dispute
// (amount lookups). KIFF reads the amount with the agent's own read
// on the same connection and limits the call by it, never by anything the
// agent sends, and sends nothing when it can't read it.

var disputeAnswer = func() gateway.Tool {
	t := refundTool
	t.Description = "Answer a dispute"
	t.InputSchema = json.RawMessage(`{"type":"object","properties":{"order":{"type":"string"},"evidence":{"type":"string"}},"required":["order"]}`)
	t.AmountUnit, t.AmountScale, t.ResourceArg = "EUR", 2, "order"
	t.AmountLookup = &gateway.UnitLookup{Tool: "order_details", Argument: "order", Path: "amount"}
	return t
}()

func disputeCall(order, op string) map[string]any {
	return map[string]any{"order": order, "evidence": "delivered", gateway.OperationArg: op}
}

// answers counts the calls the dispute tool received (the writes).
func answers(r *rig) (n int) {
	r.tool.mu.Lock()
	defer r.tool.mu.Unlock()
	for _, c := range r.tool.calls {
		if _, ok := c["evidence"]; ok {
			n++
		}
	}
	return n
}

// Invariant: a write that doesn't carry its amount is limited by the amount its resource has, as KIFF reads it.
// Category: authority
// Attack: answer a 9,000 EUR dispute through a Card that holds anything over 1,000, by leaving the amount out or naming a small one
// Refused: KIFF reads the dispute's amount itself and decides on it; an amount the agent sends is refused, and so is a call whose amount can't be read
func TestAWriteIsLimitedByTheAmountItsResourceHas(t *testing.T) {
	r := newRig(t)
	r.gw.Catalog = catalog{disputeAnswer, orderDetails}
	r.tool.amounts = map[string]any{"o-small": 30, "o-big": 9000}

	// A small dispute: KIFF read 30 and the Card allows it.
	got := r.call(t, "agent-key", disputeCall("o-small", "op-small"))
	if got.isError || answers(r) != 1 {
		t.Fatalf("small dispute: %+v, answers %d", got, answers(r))
	}
	if p := r.api.params[got.call["invocation"].(string)]; p["amount_eur"] != int64(30) || p["order"] != "o-small" {
		t.Fatalf("KIFF decided on %v", p)
	}
	// A big one: KIFF read 9,000, so the Card holds it for the owner.
	got = r.call(t, "agent-key", disputeCall("o-big", "op-big"))
	if answers(r) != 1 || got.call["state"] != "held" {
		t.Fatalf("big dispute: %+v, answers %d", got, answers(r))
	}
	if p := r.api.params[got.call["invocation"].(string)]; p["amount_eur"] != int64(9000) {
		t.Fatalf("KIFF decided the big one on %v", p)
	}
	// Retried while held, it's decided on the amount read again, never a missing one.
	if again := r.call(t, "agent-key", disputeCall("o-big", "op-big")); again.call["state"] != "held" || answers(r) != 1 {
		t.Fatalf("retry: %+v, answers %d", again, answers(r))
	}
}

// A relay tool (planque's) with a looked-up amount: the permit KIFF is asked
// for signs the call's own arguments, which never carry the amount, and the
// Card decides on the amount read for the very resource those arguments name
// (a read for one dispute can't stand behind a permit for another).
func TestARelayToolsPermitSignsTheResourceItsAmountWasReadFor(t *testing.T) {
	r := newRig(t)
	write, read := disputeAnswer, orderDetails
	write.Mode, write.Audience = gateway.ModeRelay, "https://relay.example.com"
	read.Mode, read.Audience = gateway.ModeRelay, "https://relay.example.com"
	r.gw.Catalog = catalog{write, read}
	r.gw.Credentials = creds{"t1/issue_refund": "relay-transport", "t1/order_details": "relay-transport"}
	r.tool.amounts = map[string]any{"o-small": 30}

	got := r.call(t, "agent-key", disputeCall("o-small", "op-relay"))
	if got.isError || answers(r) != 1 {
		t.Fatalf("relay dispute: %+v, answers %d", got, answers(r))
	}
	key := got.call["invocation"].(string)
	var signed map[string]any
	if err := json.Unmarshal(r.api.permits[key].Arguments, &signed); err != nil {
		t.Fatalf("permit arguments: %v", err)
	}
	if _, ok := signed["amount_eur"]; ok || signed["order"] != "o-small" {
		t.Fatalf("the permit signs %v", signed)
	}
	if p := r.api.params[key]; p["amount_eur"] != int64(30) || p["order"] != signed["order"] {
		t.Fatalf("KIFF decided on %v, the permit signs %v", p, signed)
	}
}

// Invariant: a call already sent, or whose outcome is unknown, keeps its
// recorded outcome and key when the same operation comes again and its amount
// can't be read now.
// Category: failure-modes
// Attack: make the vendor's read fail between a call and its retry, so the retry reads as never sent and the agent does it again under a new id
// Refused: the retry answers with the recorded outcome and key; a held call stays held, never decided without its amount
func TestARetryKeepsItsOutcomeWhenTheAmountCantBeReadNow(t *testing.T) {
	r := newRig(t)
	r.gw.Catalog = catalog{disputeAnswer, orderDetails}
	r.tool.amounts = map[string]any{"o-sent": 30, "o-lost": 30, "o-big": 9000}

	sent := r.call(t, "agent-key", disputeCall("o-sent", "op-sent"))
	r.tool.hang = true
	lost := r.call(t, "agent-key", disputeCall("o-lost", "op-lost"))
	r.tool.hang = false
	held := r.call(t, "agent-key", disputeCall("o-big", "op-big"))
	if sent.call["state"] != "forwarded" || lost.call["sent"] != "unknown" || held.call["state"] != "held" || answers(r) != 2 {
		t.Fatalf("before: %v / %v / %v, answers %d", sent.call, lost.call, held.call, answers(r))
	}
	// The vendor's read now says nothing about any of them.
	r.tool.mu.Lock()
	r.tool.amounts = nil
	r.tool.mu.Unlock()
	for _, c := range []struct {
		order, op string
		before    result
	}{{"o-sent", "op-sent", sent}, {"o-lost", "op-lost", lost}, {"o-big", "op-big", held}} {
		again := r.call(t, "agent-key", disputeCall(c.order, c.op))
		for _, k := range []string{"state", "sent", "invocation"} {
			if again.call[k] != c.before.call[k] {
				t.Errorf("%s: %s = %v, was %v", c.op, k, again.call[k], c.before.call[k])
			}
		}
		if strings.Contains(again.text, "couldn't read this call's amount") {
			t.Errorf("%s: %q", c.op, again.text)
		}
	}
	if answers(r) != 2 || unread(r) != 0 {
		t.Fatalf("answers %d, unread %d", answers(r), unread(r))
	}
}

func TestAnAmountTheAgentSendsIsRefused(t *testing.T) {
	r := newRig(t)
	r.gw.Catalog = catalog{disputeAnswer, orderDetails}
	r.tool.amounts = map[string]any{"o-big": 9000}

	call := disputeCall("o-big", "op-1")
	call["amount_eur"] = 5
	status, out := r.rpc(t, "agent-key", "tools/call", map[string]any{"name": "issue_refund", "arguments": call})
	b, _ := json.Marshal(out)
	if status != 200 || !strings.Contains(string(b), "amount_eur is read by KIFF from the call's order") || r.tool.count() != 0 {
		t.Fatalf("status %d, %s, tool calls %d", status, b, r.tool.count())
	}
}

func TestACallWhoseAmountCantBeReadIsNotSent(t *testing.T) {
	r := newRig(t)
	r.gw.Catalog = catalog{disputeAnswer, orderDetails}
	r.tool.amounts = map[string]any{"o-cents": 12.5, "o-neg": -3}

	for _, order := range []string{"o-none", "o-cents", "o-neg"} {
		got := r.call(t, "agent-key", disputeCall(order, "op-"+order))
		if !got.isError || answers(r) != 0 || !strings.Contains(got.text, "KIFF couldn't read this call's amount") {
			t.Fatalf("%s: %+v, answers %d", order, got, answers(r))
		}
		want(t, order, got, map[string]any{"state": "refused", "sent": "no", "retry": "none", "reasons": []string{gateway.ReasonAmountUnverified}})
		// KIFF records it as blocked, told the amount couldn't be read, so
		// the owner's record has it (amount lookups); it asks no permit.
		key, _ := got.call["invocation"].(string)
		if p := r.api.params[key]; key == "" || p[gateway.AmountUnverifiedParam] != true || p["amount_eur"] != nil {
			t.Fatalf("%s: KIFF was told %v", order, p)
		}
		if _, ok := r.api.permits[key]; ok {
			t.Fatalf("%s: a permit was asked", order)
		}
	}
	// The same operation again is refused the same way, and recorded once.
	if got := r.call(t, "agent-key", disputeCall("o-none", "op-o-none")); !got.isError || answers(r) != 0 ||
		!strings.Contains(got.text, "KIFF couldn't read this call's amount") || unread(r) != 3 {
		t.Fatalf("again: %+v, recorded %d", got, unread(r))
	}
	// Not a read on the same connection: refused the same way.
	elsewhere := orderDetails
	elsewhere.UpstreamURL = "http://other.test/mcp"
	r.gw.Catalog = catalog{disputeAnswer, elsewhere}
	if got := r.call(t, "agent-key", disputeCall("o-cents", "op-elsewhere")); !strings.Contains(got.text, "isn't a read on the same connection") || answers(r) != 0 {
		t.Fatalf("elsewhere: %+v", got)
	}
}

func TestAnAmountLookupNeedsItsNamesAndAnotherRead(t *testing.T) {
	ok := gateway.UnitLookup{Tool: "order_details", Argument: "order", Path: "amount"}
	for name, c := range map[string]struct {
		tool gateway.Tool
		l    gateway.UnitLookup
		want string
	}{
		"fine":        {disputeAnswer, ok, ""},
		"no amount":   {gateway.Tool{Name: "issue_refund", ResourceArg: "order"}, ok, "needs the tool's amount argument"},
		"no resource": {gateway.Tool{Name: "issue_refund", AmountArg: "amount_eur"}, ok, "needs the tool's amount argument"},
		"amount path": {gateway.Tool{Name: "issue_refund", AmountArg: "amount_eur", ResourceArg: "order", AmountPath: "p.amount"}, ok, "can't also take it from the call"},
		"itself":      {disputeAnswer, gateway.UnitLookup{Tool: "issue_refund", Argument: "order", Path: "amount"}, "must name another"},
		"no argument": {disputeAnswer, gateway.UnitLookup{Tool: "order_details", Path: "amount"}, "argument must name"},
		"no path":     {disputeAnswer, gateway.UnitLookup{Tool: "order_details", Argument: "order"}, "path must be"},
	} {
		if got := c.l.ValidAmount(c.tool); (c.want == "" && got != "") || !strings.Contains(got, c.want) {
			t.Errorf("%s: %q, want %q", name, got, c.want)
		}
	}
}

// Two reads whose lookups name each other (stored before connect refused a
// read with one) never call each other in a loop: a read with a lookup of its
// own isn't used, so the call is refused and the gateway stays up for every
// account.
func TestLookupsNeverCallEachOtherInALoop(t *testing.T) {
	r := newRig(t)
	a := orderDetails
	a.AmountArg, a.ResourceArg = "amount_eur", "order"
	a.AmountLookup = &gateway.UnitLookup{Tool: "order_lookup", Argument: "order", Path: "amount"}
	b := orderDetails
	b.Name = "order_lookup"
	b.AmountArg, b.ResourceArg = "amount_eur", "order"
	b.AmountLookup = &gateway.UnitLookup{Tool: "order_details", Argument: "order", Path: "amount"}
	write := disputeAnswer
	r.gw.Catalog = catalog{write, a, b}
	r.tool.amounts = map[string]any{"o-1": 30}

	got := r.call(t, "agent-key", disputeCall("o-1", "op-loop"))
	if !got.isError || answers(r) != 0 || !strings.Contains(got.text, "has a lookup of its own") {
		t.Fatalf("%+v, answers %d", got, answers(r))
	}
	// The same for a unit lookup whose read has a lookup.
	unitWrite := lookupRefund
	r.gw.Catalog = catalog{unitWrite, a, b}
	r.tool.currency = map[string]string{"o-1": "eur"}
	if got := r.call(t, "agent-key", orderCall(50, "o-1", "op-unit-loop")); !got.isError || !strings.Contains(got.text, "has a lookup of its own") {
		t.Fatalf("unit: %+v", got)
	}
}

// unread is how many calls KIFF was told the amount of couldn't be read.
func unread(r *rig) int {
	n := 0
	for _, p := range r.api.params {
		if p[gateway.AmountUnverifiedParam] == true {
			n++
		}
	}
	return n
}
