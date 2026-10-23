package gateway_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/kiff/cloud/gateway/internal/gateway"
	"github.com/kiff/cloud/gateway/internal/kiffapi"
)

// A Card (kiff/tafs#138 item 50) counts a tool's amounts in one
// currency, and a refund is in its payment's, which the call doesn't say.
// KIFF reads the order's currency with the agent's own read on the same
// connection before deciding, and sends nothing in another currency, or
// when it can't tell.

var (
	lookupRefund = func() gateway.Tool {
		t := refundTool
		t.AmountUnit, t.AmountScale, t.ResourceArg = "EUR", 2, "order"
		t.UnitLookup = &gateway.UnitLookup{Tool: "order_details", Argument: "order", Path: "currency"}
		return t
	}()
	orderDetails = gateway.Tool{Name: "order_details", Description: "Look up an order",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"order":{"type":"string"}},"required":["order"]}`),
		UpstreamURL: refundTool.UpstreamURL, UpstreamTool: "get_order", ReadOnly: true}
)

func writes(r *rig) (n int) {
	r.tool.mu.Lock()
	defer r.tool.mu.Unlock()
	for _, c := range r.tool.calls {
		if _, ok := c["amount_eur"]; ok {
			n++
		}
	}
	return n
}

func reads(r *rig) int { return r.tool.count() - writes(r) }

func orderCall(n int, order, op string) map[string]any {
	a := amount(n, op)
	a["order"] = order
	return a
}

// Invariant: a Card's amounts are counted in its own currency only.
// Category: authority
// Attack: refund $300 on a USD payment against a Card limited to 50 EUR
// Refused: KIFF reads the payment's currency first; a call in another currency, or one it can't check, is refused and nothing is sent
func TestACallInAnotherCurrencyIsNotSent(t *testing.T) {
	r := newRig(t)
	r.gw.Catalog = catalog{lookupRefund, orderDetails}
	r.tool.currency = map[string]string{"o-eur": "eur", "o-usd": "usd"}

	if got := r.call(t, "agent-key", orderCall(500, "o-eur", "op-eur")); got.isError || writes(r) != 1 {
		t.Fatalf("EUR order: %+v, writes %d", got, writes(r))
	}
	got := r.call(t, "agent-key", orderCall(300, "o-usd", "op-usd"))
	if !got.isError || writes(r) != 1 || !strings.Contains(got.text, "This call is in USD and the Card's limits are in EUR, so KIFF didn't send it") {
		t.Fatalf("USD order: %+v, writes %d", got, writes(r))
	}
	want(t, "mismatch", got, map[string]any{"state": "refused", "sent": "no", "retry": "none", "reasons": []string{gateway.ReasonUnitMismatch}})
	// KIFF records it, told the currency the read said, so the owner's
	// record has it as blocked; it is refused here whatever KIFF answers.
	if p := r.api.params[got.call["invocation"].(string)]; p[gateway.UnitParam] != "USD" {
		t.Fatalf("KIFF was told %v", p)
	}
	// The read was made once per order, through the agent's own path.
	if reads(r) != 2 {
		t.Fatalf("reads = %d, want one per order", reads(r))
	}
	// A second refund on the same order doesn't read again.
	if got := r.call(t, "agent-key", orderCall(200, "o-eur", "op-eur-2")); got.isError || reads(r) != 2 || writes(r) != 2 {
		t.Fatalf("second EUR refund: %+v, reads %d, writes %d", got, reads(r), writes(r))
	}
	// A retry of the refused call is answered from its record.
	if again := r.call(t, "agent-key", orderCall(300, "o-usd", "op-usd")); !strings.Contains(again.text, "in USD") || reads(r) != 2 {
		t.Fatalf("retry: %+v, reads %d", again, reads(r))
	}
}

// A vendor's read carries its own _meta (kiff/tafs#138, item 50): Stripe's
// names a widget session with a string. KIFF reads only its own key there,
// so the currency is still read, and a call in another one still refused.
func TestTheLookupReadsPastTheVendorsOwnMeta(t *testing.T) {
	r := newRig(t)
	r.gw.Catalog = catalog{lookupRefund, orderDetails}
	r.tool.currency = map[string]string{"o-eur": "eur", "o-usd": "usd"}
	r.tool.readMeta = map[string]any{"openai/widgetSessionId": "7de28af2", "io.example/trace": map[string]any{"id": 1}}

	if got := r.call(t, "agent-key", orderCall(50, "o-eur", "op-eur")); got.isError || writes(r) != 1 {
		t.Fatalf("EUR order: %+v, writes %d", got, writes(r))
	}
	if got := r.call(t, "agent-key", orderCall(300, "o-usd", "op-usd")); !strings.Contains(got.text, "This call is in USD") || writes(r) != 1 {
		t.Fatalf("USD order: %+v, writes %d", got, writes(r))
	}
}

// The read is called with the call's arguments its schema also takes
// (kiff/tafs#138, item 50): on the owner's iPad every checked call, in the
// right currency too, was refused as unverified, because the lookup left out
// the account argument the call carried and the vendor requires. Fixed and
// idempotency arguments, the amount and anything the read doesn't take stay out.
func TestTheLookupCarriesTheCallsSharedArguments(t *testing.T) {
	r := newRig(t)
	refund := lookupRefund
	refund.InputSchema = json.RawMessage(`{"type":"object","properties":{"amount_eur":{"type":"integer"},"order":{"type":"string"},` +
		`"account":{"type":"string"},"note":{"type":"string"},"mode":{"type":"string"}},"required":["amount_eur"]}`)
	refund.FixedArgs = json.RawMessage(`{"mode":"refund"}`)
	details := orderDetails
	details.InputSchema = json.RawMessage(`{"type":"object","properties":{"order":{"type":"string"},"account":{"type":"string"},` +
		`"order_alt":{"type":"string"},"mode":{"type":"string"},"livemode":{"type":"boolean"},"amount_eur":{"type":"integer"}},"required":["order"]}`)
	details.FixedArgs = json.RawMessage(`{"livemode":false}`)
	r.gw.Catalog = catalog{refund, details}
	r.tool.currency = map[string]string{"o-eur": "eur"}

	r.tool.currency = map[string]string{"o-eur": "eur", "o-usd": "usd"}
	call := orderCall(500, "o-eur", "op-1")
	// order_alt is a selector only the read declares: the agent must not
	// choose what its own check reads.
	call["account"], call["note"], call["order_alt"] = "acct_1", "the refund's own", "o-usd"
	if got := r.call(t, "agent-key", call); got.isError || writes(r) != 1 || reads(r) != 1 {
		t.Fatalf("%+v, writes %d, reads %d", got, writes(r), reads(r))
	}
	r.tool.mu.Lock()
	read := r.tool.calls[0]
	r.tool.mu.Unlock()
	b, _ := json.Marshal(read)
	if string(b) != `{"account":"acct_1","livemode":false,"order":"o-eur"}` {
		t.Fatalf("the lookup read with %s", b)
	}
	// The unit is kept per scope: the same order in another account is read again.
	other := orderCall(400, "o-eur", "op-2")
	other["account"] = "acct_2"
	if got := r.call(t, "agent-key", other); got.isError || reads(r) != 2 {
		t.Fatalf("another account: %+v, reads %d", got, reads(r))
	}
	same := orderCall(300, "o-eur", "op-3")
	same["account"] = "acct_1"
	if got := r.call(t, "agent-key", same); got.isError || reads(r) != 2 {
		t.Fatalf("the same account again: %+v, reads %d", got, reads(r))
	}
}

// When the read can't say, the call is refused and says why, in words.
func TestACallWhoseCurrencyCantBeCheckedIsNotSent(t *testing.T) {
	cases := map[string]struct {
		setup func(r *rig)
		why   string
	}{
		"the read is used up": {func(r *rig) {
			r.api.refuse[0] = kiffapi.Decision{Outcome: "blocked", Reasons: []string{"mandate_limit_reached"}}
		}, "the Card's reads for this period are used up"},
		"the read is not a read": {func(r *rig) {
			d := orderDetails
			d.ReadOnly = false
			r.gw.Catalog = catalog{lookupRefund, d}
		}, "isn't a read on the same connection"},
		"the read is elsewhere": {func(r *rig) {
			d := orderDetails
			d.UpstreamURL = "http://other.test/mcp"
			r.gw.Catalog = catalog{lookupRefund, d}
		}, "isn't a read on the same connection"},
		"the answer has no currency": {func(r *rig) {}, "the read's answer doesn't say it"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			r := newRig(t)
			r.gw.Catalog = catalog{lookupRefund, orderDetails}
			if name != "the answer has no currency" {
				r.tool.currency = map[string]string{"o-1": "eur"}
			}
			c.setup(r)
			got := r.call(t, "agent-key", orderCall(500, "o-1", "op-1"))
			if !got.isError || writes(r) != 0 || !strings.Contains(got.text, "KIFF couldn't check this call's currency") || !strings.Contains(got.text, c.why) {
				t.Fatalf("%+v, writes %d", got, writes(r))
			}
			want(t, name, got, map[string]any{"state": "refused", "sent": "no", "retry": "none", "reasons": []string{gateway.ReasonUnitUnverified}})
			if p := r.api.params[got.call["invocation"].(string)]; p[gateway.UnitParam] != gateway.UnitUnverified {
				t.Fatalf("KIFF was told %v", p)
			}
		})
	}
}

// A call without the resource the read takes is refused as invalid, before
// any lookup: a refund by another id can't be checked.
func TestACallWithoutItsResourceIsNotLookedUp(t *testing.T) {
	r := newRig(t)
	r.gw.Catalog = catalog{lookupRefund, orderDetails}
	r.tool.currency = map[string]string{"o-1": "eur"}
	_, out := r.rpc(t, "agent-key", "tools/call", map[string]any{"name": "issue_refund",
		"arguments": map[string]any{"amount_eur": 500, "payment": "pi_1", gateway.OperationArg: "op-other"}})
	if r.tool.count() != 0 {
		t.Fatalf("tool calls = %d: %v", r.tool.count(), out)
	}
}

func TestUnitLookupNeedsAnAmountUnitAndResource(t *testing.T) {
	l := gateway.UnitLookup{Tool: "order_details", Argument: "order", Path: "currency"}
	if msg := l.Valid(lookupRefund); msg != "" {
		t.Fatalf("valid lookup: %s", msg)
	}
	for _, bad := range []gateway.Tool{refundTool, func() gateway.Tool { t := lookupRefund; t.ResourceArg = ""; return t }()} {
		if l.Valid(bad) == "" {
			t.Errorf("accepted on %+v", bad)
		}
	}
	for _, bad := range []gateway.UnitLookup{{Tool: "issue_refund", Argument: "order", Path: "currency"},
		{Tool: "order_details", Argument: "", Path: "currency"}, {Tool: "order_details", Argument: "order", Path: "a..b"}} {
		if bad.Valid(lookupRefund) == "" {
			t.Errorf("accepted %+v", bad)
		}
	}
}
