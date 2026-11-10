package gateway_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/gateway/internal/gateway"
	"github.com/kiff/cloud/gateway/internal/invocation"
	"github.com/kiff/cloud/gateway/internal/kiffapi"
)

// pending reads kiff_pending with key: its text and its calls.
func (r *rig) pending(t *testing.T, key string) (string, []gateway.PendingCall) {
	t.Helper()
	text, calls, _ := r.pendingPage(t, key, "")
	return text, calls
}

// pendingPage reads one page of kiff_pending: its text, calls and next_cursor.
func (r *rig) pendingPage(t *testing.T, key, cursor string) (string, []gateway.PendingCall, string) {
	t.Helper()
	args := map[string]any{}
	if cursor != "" {
		args["cursor"] = cursor
	}
	status, out := r.rpc(t, key, "tools/call", map[string]any{"name": gateway.PendingTool, "arguments": args})
	if status != http.StatusOK || out["result"] == nil {
		t.Fatalf("kiff_pending: %d %v", status, out)
	}
	res := out["result"].(map[string]any)
	if res["isError"] == true {
		t.Fatalf("kiff_pending error: %v", res)
	}
	text := res["content"].([]any)[0].(map[string]any)["text"].(string)
	sc := res["structuredContent"].(map[string]any)
	b, _ := json.Marshal(sc["calls"])
	var calls []gateway.PendingCall
	if err := json.Unmarshal(b, &calls); err != nil {
		t.Fatal(err)
	}
	next, _ := sc["next_cursor"].(string)
	return text, calls, next
}

// A held call is listed with the owner's answer and the call that
// collects it; that call forwards once; another agent's key sees nothing;
// reading sends nothing.
// Invariant: reading the list of pending calls decides, draws and sends
// nothing, and an agent's key sees only its own calls.
// Category: authority
// Attack: read kiff_pending with another agent's key, or read it repeatedly hoping it sends the held call
// Refused: the other key sees nothing, and reading never forwards; only the original call collects it, once
func TestPendingListsAHeldCallUntilItIsCollected(t *testing.T) {
	r := newRig(t)
	held := r.call(t, "agent-key", amount(5000, "op-held"))
	exc, _ := held.call["exception_id"].(string)
	if !strings.Contains(held.text, "Waiting") || exc == "" {
		t.Fatalf("hold: %+v", held)
	}
	_, calls := r.pending(t, "agent-key")
	if len(calls) != 1 {
		t.Fatalf("calls: %+v", calls)
	}
	c := calls[0]
	if c.Tool != "issue_refund" || c.State != "held" || c.Answer != "waiting" || c.OperationID != "op-held" || c.ExceptionID != exc ||
		c.Arguments["order"] != "o-1" || c.Arguments[gateway.OperationArg] != nil {
		t.Fatalf("held: %+v", c)
	}
	if _, others := r.pending(t, "agent-key-2"); len(others) != 0 {
		t.Fatalf("another key sees %+v", others)
	}

	r.api.approveAll()
	r.api.holds = map[string]kiffapi.Hold{exc: {ExceptionID: exc, Action: "issue_refund", Status: "approved"}}
	text, calls := r.pending(t, "agent-key")
	if len(calls) != 1 || calls[0].Answer != "approved" ||
		!strings.Contains(text, "The owner answered. Call issue_refund again with these arguments and kiff_operation_id op-held") {
		t.Fatalf("approved: %s %+v", text, calls)
	}
	if r.tool.count() != 0 || len(r.api.drawn) != 0 {
		t.Fatalf("reading sent or drew: tool calls = %d, drawn = %v", r.tool.count(), r.api.drawn)
	}
	args := calls[0].Arguments
	args[gateway.OperationArg] = calls[0].OperationID
	if got := r.call(t, "agent-key", args); got.isError || r.tool.count() != 1 {
		t.Fatalf("collect: %+v, tool calls = %d", got, r.tool.count())
	}
	if _, calls := r.pending(t, "agent-key"); len(calls) != 0 {
		t.Fatalf("collected call still listed: %+v", calls)
	}
}

// A hold made without kiff_operation_id is listed with its own key as
// the operation id, so it can be collected after the identical-call window
// has passed. An identical call without an id after the window is a new
// call; the key with other arguments is a reused id.
func TestPendingCollectsACallMadeWithoutAnOperationID(t *testing.T) {
	r := newRig(t)
	held := r.call(t, "agent-key", amount(5000, ""))
	exc, _ := held.call["exception_id"].(string)
	if !strings.Contains(held.text, "Waiting") {
		t.Fatalf("hold: %+v", held)
	}
	_, calls := r.pending(t, "agent-key")
	if len(calls) != 1 || !strings.HasPrefix(calls[0].OperationID, "tc-") {
		t.Fatalf("calls: %+v", calls)
	}
	key := calls[0].OperationID
	r.api.approveAll()
	r.api.holds = map[string]kiffapi.Hold{exc: {ExceptionID: exc, Status: "approved"}}
	*r.now = t0.Add(invocation.DefaultWindow + time.Minute)

	other := amount(4000, key)
	if got := r.call(t, "agent-key", other); !strings.Contains(got.text, "already used") || r.tool.count() != 0 {
		t.Fatalf("the key with other arguments: %+v", got)
	}
	if got := r.call(t, "agent-key-2", amount(5000, key)); !strings.Contains(got.text, "Waiting") || r.tool.count() != 0 {
		t.Fatalf("another key with the key as its id: %+v, tool calls = %d", got, r.tool.count())
	}
	if got := r.call(t, "agent-key", amount(5000, key)); got.isError || r.tool.count() != 1 {
		t.Fatalf("collect after the window: %+v, tool calls = %d", got, r.tool.count())
	}
	if got := r.call(t, "agent-key", amount(5000, key)); got.isError || r.tool.count() != 1 {
		t.Fatalf("again: %+v, tool calls = %d", got, r.tool.count())
	}
	if got := r.call(t, "agent-key", amount(5000, "")); !strings.Contains(got.text, "Waiting") || r.tool.count() != 1 {
		t.Fatalf("an identical call without an id after the window is a new call: %+v", got)
	}
}

// Every answer is said, a call being sent or with an unknown outcome
// is listed (a withdrawn hold apart from an ended one), KIFF's idempotency argument is never shown, and an
// unreachable KIFF is said rather than guessed.
func TestPendingSaysEachStateAndAnswer(t *testing.T) {
	r := newRig(t)
	schema := json.RawMessage(`{"type":"object","properties":{"amount_eur":{"type":"integer"},"order":{"type":"string"},"idempotency_key":{"type":"string"}},"required":["amount_eur"]}`)
	r.gw.Catalog = catalog{{Name: "issue_refund", InputSchema: schema, AmountArg: "amount_eur", IdempotencyArg: "idempotency_key"}}
	exc := map[string]string{}
	for _, op := range []string{"op-refused", "op-ended", "op-expired", "op-withdrawn", "op-waiting", "op-no-room", "op-unavailable"} {
		a := amount(5000, op)
		a["idempotency_key"] = "agent-set"
		got := r.call(t, "agent-key", a)
		exc[op], _ = got.call["exception_id"].(string)
		*r.now = r.now.Add(time.Second)
	}
	r.abandoned(t, "op-sending", invocation.StateForwarding)
	r.abandoned(t, "op-unknown", invocation.StateUnknown)
	r.api.holds = map[string]kiffapi.Hold{
		exc["op-refused"]:     {ExceptionID: exc["op-refused"], Status: "rejected"},
		exc["op-ended"]:       {ExceptionID: exc["op-ended"], Status: "invalidated"}, // its Card was revoked
		exc["op-expired"]:     {ExceptionID: exc["op-expired"], Status: "expired"},
		exc["op-withdrawn"]:   {ExceptionID: exc["op-withdrawn"], Status: "invalidated", Withdrawn: true},
		exc["op-no-room"]:     {ExceptionID: exc["op-no-room"], Status: "approved_no_room"},
		exc["op-unavailable"]: {ExceptionID: exc["op-unavailable"], Status: "unavailable"},
	}
	_, calls := r.pending(t, "agent-key")
	got := map[string]gateway.PendingCall{}
	for _, c := range calls {
		got[c.OperationID] = c
		// Listed as the agent sent it, never KIFF's own value.
		if v, shown := c.Arguments["idempotency_key"]; shown && v != "agent-set" {
			t.Errorf("%s lists idempotency_key %v", c.OperationID, v)
		}
	}
	for op, want := range map[string]string{"op-refused": "refused", "op-waiting": "waiting", "op-no-room": "approved_no_room", "op-unavailable": "unavailable"} {
		if got[op].State != "held" || got[op].Answer != want {
			t.Errorf("%s: %+v, want answer %s", op, got[op], want)
		}
	}
	// A hold the agent's own run withdrew, or one that ended (expired, or
	// its Card withdrawn) without the owner's answer, is open to no one:
	// none of them is listed.
	for _, op := range []string{"op-withdrawn", "op-ended", "op-expired"} {
		if c, listed := got[op]; listed {
			t.Errorf("%s is listed: %+v", op, c)
		}
	}
	if got["op-sending"].State != "sending" || got["op-unknown"].State != "unknown" {
		t.Errorf("sending %+v, unknown %+v", got["op-sending"], got["op-unknown"])
	}
	if r.tool.count() != 0 {
		t.Errorf("reading sent %d calls", r.tool.count())
	}
	r.api.holdsFail = true
	_, calls = r.pending(t, "agent-key")
	for _, c := range calls {
		if c.State == "held" && c.Answer != "unavailable" {
			t.Errorf("%s with KIFF unreachable: %+v", c.OperationID, c)
		}
	}
}

// A refusal that names what to do instead is listed with it, so the
// agent can act on the owner's answer; a plain refusal reads as before.
func TestPendingSaysTheOwnersNextStep(t *testing.T) {
	r := newRig(t)
	exc := map[string]string{}
	for _, op := range []string{"op-return", "op-plain"} {
		got := r.call(t, "agent-key", amount(5000, op))
		exc[op], _ = got.call["exception_id"].(string)
		*r.now = r.now.Add(time.Second)
	}
	r.api.holds = map[string]kiffapi.Hold{
		exc["op-return"]: {ExceptionID: exc["op-return"], Status: "rejected", NextStep: "return_first", Note: "Label sent."},
		exc["op-plain"]:  {ExceptionID: exc["op-plain"], Status: "rejected"},
	}
	_, calls := r.pending(t, "agent-key")
	got := map[string]gateway.PendingCall{}
	for _, c := range calls {
		got[c.OperationID] = c
	}
	ret := got["op-return"]
	if ret.Answer != "refused" || ret.NextStep != "return_first" || ret.Note != "Label sent." ||
		!strings.Contains(ret.Next, "next step return_first") || !strings.Contains(ret.Next, `"Label sent."`) || !strings.Contains(ret.Next, "Nothing was sent") {
		t.Fatalf("refused with a next step: %+v", ret)
	}
	plain := got["op-plain"]
	if plain.Answer != "refused" || plain.NextStep != "" || plain.Note != "" || strings.Contains(plain.Next, "next step") {
		t.Fatalf("plain refusal: %+v", plain)
	}
	if r.tool.count() != 0 {
		t.Errorf("reading sent %d calls", r.tool.count())
	}
}

// Kiff_pending is the gateway's own: listed, read-only, and a
// connected tool under its name is never reached.
func TestPendingIsListedAndReserved(t *testing.T) {
	r := newRig(t)
	if !gateway.ReservedToolName("KIFF_Pending") {
		t.Error("kiff_pending is not reserved")
	}
	_, out := r.rpc(t, "agent-key", "tools/list", nil)
	var found bool
	for _, raw := range out["result"].(map[string]any)["tools"].([]any) {
		tl := raw.(map[string]any)
		if tl["name"] == gateway.PendingTool {
			found = tl["annotations"].(map[string]any)["readOnlyHint"] == true
		}
	}
	if !found {
		t.Error("kiff_pending is not listed as read-only")
	}
}

// Review of the plugin's polling (kiff-plugins#12): every held call it
// keeps is listed with its answer, past the API's 20 ids per read.
func TestPendingAnswersMoreThanTwentyHolds(t *testing.T) {
	r := newRig(t)
	r.api.holds = map[string]kiffapi.Hold{}
	var last string
	for i := 0; i < 25; i++ {
		got := r.call(t, "agent-key", amount(5000, fmt.Sprintf("op-%02d", i)))
		exc, _ := got.call["exception_id"].(string)
		r.api.holds[exc] = kiffapi.Hold{ExceptionID: exc, Status: "held"}
		last = exc
		*r.now = r.now.Add(time.Second)
	}
	first := ""
	_, calls := r.pending(t, "agent-key")
	for _, c := range calls {
		if c.OperationID == "op-00" {
			first = c.ExceptionID
		}
	}
	r.api.holds[first] = kiffapi.Hold{ExceptionID: first, Status: "approved"}
	_, calls = r.pending(t, "agent-key")
	answers := map[string]string{}
	for _, c := range calls {
		answers[c.OperationID] = c.Answer
	}
	if len(calls) != 25 || answers["op-00"] != "approved" || answers["op-24"] != "waiting" || last == "" {
		t.Fatalf("%d calls, answers %v", len(calls), answers)
	}
}

// When the agent sent the tool's idempotency argument
// itself, the listed call is exactly what KIFF stored, so sending it again
// collects the approved hold, with or without an operation id. The tool
// still receives KIFF's own value, never the agent's.
func TestPendingCollectsACallThatCarriedTheIdempotencyArgument(t *testing.T) {
	r := newRig(t)
	schema := json.RawMessage(`{"type":"object","properties":{"amount_eur":{"type":"integer"},"order":{"type":"string"},"idempotency_key":{"type":"string"}},"required":["amount_eur"]}`)
	r.gw.Catalog = catalog{{Name: "issue_refund", InputSchema: schema, AmountArg: "amount_eur", IdempotencyArg: "idempotency_key"}}
	withID := amount(5000, "op-idem")
	withID["idempotency_key"] = "agent-set"
	r.call(t, "agent-key", withID)
	withoutID := map[string]any{"amount_eur": 5000, "order": "o-2", "idempotency_key": "agent-set-2"}
	r.call(t, "agent-key", withoutID)
	r.api.approveAll()
	r.api.holds = map[string]kiffapi.Hold{}
	_, calls := r.pending(t, "agent-key")
	for _, c := range calls {
		r.api.holds[c.ExceptionID] = kiffapi.Hold{ExceptionID: c.ExceptionID, Status: "approved"}
	}
	*r.now = t0.Add(invocation.DefaultWindow + time.Minute) // past the window, for the call without an id
	_, calls = r.pending(t, "agent-key")
	if len(calls) != 2 {
		t.Fatalf("calls: %+v", calls)
	}
	for i, c := range calls {
		if c.Answer != "approved" || c.Arguments["idempotency_key"] == nil {
			t.Fatalf("listed: %+v", c)
		}
		args := c.Arguments
		args[gateway.OperationArg] = c.OperationID
		if got := r.call(t, "agent-key", args); got.isError || r.tool.count() != i+1 {
			t.Fatalf("collect %s with the listed call: %+v, tool calls = %d", c.OperationID, got, r.tool.count())
		}
	}
	for _, sent := range r.tool.calls {
		if k, _ := sent["idempotency_key"].(string); !strings.HasPrefix(k, "tc-") {
			t.Errorf("the tool received idempotency_key %q, not KIFF's", k)
		}
	}
}

// An agent with more pending calls than one read lists
// reaches the older ones through next_cursor. An approved hold behind 55
// newer unknown calls is found on the second page and collected.
func TestPendingPagesPastTheNewestFifty(t *testing.T) {
	r := newRig(t)
	held := r.call(t, "agent-key", amount(5000, "op-old-hold"))
	exc, _ := held.call["exception_id"].(string)
	for i := 0; i < 55; i++ {
		*r.now = r.now.Add(time.Second)
		r.abandoned(t, fmt.Sprintf("op-unknown-%02d", i), invocation.StateUnknown)
	}
	r.api.approveAll()
	r.api.holds = map[string]kiffapi.Hold{exc: {ExceptionID: exc, Status: "approved"}}

	text, first, next := r.pendingPage(t, "agent-key", "")
	if len(first) != 50 || next == "" || !strings.Contains(text, "read kiff_pending again with cursor "+next) {
		t.Fatalf("first page: %d calls, next %q", len(first), next)
	}
	seen := map[string]bool{}
	for _, c := range first {
		seen[c.OperationID] = true
		if c.OperationID == "op-old-hold" {
			t.Fatal("the oldest call is on the first page")
		}
	}
	_, second, after := r.pendingPage(t, "agent-key", next)
	if len(second) != 6 || after != "" {
		t.Fatalf("second page: %d calls, next %q", len(second), after)
	}
	var hold gateway.PendingCall
	for _, c := range second {
		if seen[c.OperationID] {
			t.Errorf("%s listed twice", c.OperationID)
		}
		if c.OperationID == "op-old-hold" {
			hold = c
		}
	}
	if hold.Answer != "approved" {
		t.Fatalf("the approved hold on the second page: %+v", hold)
	}
	args := hold.Arguments
	args[gateway.OperationArg] = hold.OperationID
	if got := r.call(t, "agent-key", args); got.isError || r.tool.count() != 1 {
		t.Fatalf("collect: %+v, tool calls = %d", got, r.tool.count())
	}

	// A cursor that is not one kiff_pending returned is refused.
	_, out := r.rpc(t, "agent-key", "tools/call", map[string]any{"name": gateway.PendingTool, "arguments": map[string]any{"cursor": "not-a-cursor"}})
	if out["error"] == nil {
		t.Errorf("a made-up cursor: %v", out)
	}
}

// A hold the agent's own run withdrew is open to no one, so kiff_pending
// leaves it out, however many there are, and still finds the open call
// behind them (an eval's fifty withdrawn holds read as
// "about 30 requests still open", and the taf skipped the work).
func TestPendingLeavesOutHoldsTheAgentWithdrew(t *testing.T) {
	for _, withdrawn := range []int{40, 60, 260} {
		t.Run(fmt.Sprint(withdrawn), func(t *testing.T) {
			r := newRig(t)
			waiting := r.call(t, "agent-key", amount(5000, "op-waiting"))
			if _, ok := waiting.call["exception_id"].(string); !ok {
				t.Fatalf("the waiting call wasn't held: %+v", waiting)
			}
			r.api.holds = map[string]kiffapi.Hold{}
			for i := 0; i < withdrawn; i++ {
				*r.now = r.now.Add(time.Second)
				got := r.call(t, "agent-key", amount(5000, fmt.Sprintf("op-withdrawn-%03d", i)))
				exc, _ := got.call["exception_id"].(string)
				r.api.holds[exc] = kiffapi.Hold{ExceptionID: exc, Status: "invalidated", Withdrawn: true}
			}
			// Read on, as an agent does, until there is no cursor.
			var listed []gateway.PendingCall
			text, calls, next := r.pendingPage(t, "agent-key", "")
			first := len(calls)
			listed = append(listed, calls...)
			for pages := 0; next != "" && pages < 10; pages++ {
				_, calls, next = r.pendingPage(t, "agent-key", next)
				listed = append(listed, calls...)
			}
			if len(listed) != 1 || listed[0].OperationID != "op-waiting" || listed[0].Answer != "waiting" {
				t.Fatalf("%d withdrawn and 1 waiting listed %d calls: %+v", withdrawn, len(listed), listed)
			}
			if strings.Contains(text, "withdrew") {
				t.Errorf("the text names a withdrawn hold: %q", text)
			}
			// Within the reads one listing makes (5 of 50), the waiting call
			// is on the first page itself, so an agent that reads one page
			// still finds it.
			if withdrawn < 5*50 && (first != 1 || !strings.Contains(text, "op-waiting") || next != "") {
				t.Errorf("%d withdrawn: the first page lists %d: %q", withdrawn, first, text)
			}
			if r.tool.count() != 0 {
				t.Errorf("reading sent %d calls", r.tool.count())
			}
		})
	}
}

// Leaving withdrawn holds out never loses an open call or lists one
// twice: a page filled part way through a later read hands a cursor to
// the rest.
func TestPendingPagesPastWithdrawnHolds(t *testing.T) {
	r := newRig(t)
	for i := 0; i < 70; i++ {
		*r.now = r.now.Add(time.Second)
		r.abandoned(t, fmt.Sprintf("op-unknown-%02d", i), invocation.StateUnknown)
	}
	r.api.holds = map[string]kiffapi.Hold{}
	for i := 0; i < 10; i++ {
		*r.now = r.now.Add(time.Second)
		got := r.call(t, "agent-key", amount(5000, fmt.Sprintf("op-withdrawn-%02d", i)))
		exc, _ := got.call["exception_id"].(string)
		r.api.holds[exc] = kiffapi.Hold{ExceptionID: exc, Status: "invalidated", Withdrawn: true}
	}
	_, first, next := r.pendingPage(t, "agent-key", "")
	if len(first) != 50 || next == "" {
		t.Fatalf("first page: %d calls, next %q", len(first), next)
	}
	seen := map[string]bool{}
	for _, c := range first {
		seen[c.OperationID] = true
	}
	_, second, after := r.pendingPage(t, "agent-key", next)
	if len(second) != 20 || after != "" {
		t.Fatalf("second page: %d calls, next %q", len(second), after)
	}
	for _, c := range second {
		if seen[c.OperationID] {
			t.Errorf("%s listed twice", c.OperationID)
		}
		seen[c.OperationID] = true
	}
	for i := 0; i < 70; i++ {
		if !seen[fmt.Sprintf("op-unknown-%02d", i)] {
			t.Errorf("op-unknown-%02d is never listed", i)
		}
	}
}

// A hold that ended without the owner's answer (it expired, or its Card
// was withdrawn) is open to no one either, so kiff_pending leaves it out
// too, and the open call behind sixty of them is on the first page (the
// chargebacks eval read "over 100 earlier submissions" as waiting after
// the withdrawn ones were left out).
func TestPendingLeavesOutHoldsThatEnded(t *testing.T) {
	r := newRig(t)
	if got := r.call(t, "agent-key", amount(5000, "op-waiting")); got.call["exception_id"] == nil {
		t.Fatalf("the waiting call wasn't held: %+v", got)
	}
	r.api.holds = map[string]kiffapi.Hold{}
	for i := 0; i < 60; i++ {
		*r.now = r.now.Add(time.Second)
		got := r.call(t, "agent-key", amount(5000, fmt.Sprintf("op-ended-%02d", i)))
		exc, _ := got.call["exception_id"].(string)
		status := "expired"
		if i%2 == 1 {
			status = "invalidated" // its Card was withdrawn
		}
		r.api.holds[exc] = kiffapi.Hold{ExceptionID: exc, Status: status}
	}
	text, calls, next := r.pendingPage(t, "agent-key", "")
	if len(calls) != 1 || calls[0].OperationID != "op-waiting" || calls[0].Answer != "waiting" || next != "" {
		t.Fatalf("60 ended and 1 waiting: %d calls, next %q: %q", len(calls), next, text)
	}
	// A refusal is the owner's answer and stays listed: it says what to do.
	exc, _ := r.call(t, "agent-key", amount(5000, "op-refused")).call["exception_id"].(string)
	r.api.holds[exc] = kiffapi.Hold{ExceptionID: exc, Status: "rejected"}
	if _, calls, _ := r.pendingPage(t, "agent-key", ""); len(calls) != 2 || calls[0].Answer != "refused" {
		t.Fatalf("with a refusal: %+v", calls)
	}
}

// A hold KIFF no longer has (an ended hold pruned from a test account, or
// one that isn't this agent's) will never get the owner's answer, so
// kiff_pending leaves it out as ended, never as waiting: the chargebacks
// eval listed 118 of them as waiting, and its key couldn't withdraw them.
// A live hold and one KIFF couldn't be asked about stay listed.
func TestPendingLeavesOutHoldsKIFFNoLongerHas(t *testing.T) {
	r := newRig(t)
	live, _ := r.call(t, "agent-key", amount(5000, "op-live")).call["exception_id"].(string)
	if live == "" {
		t.Fatal("the live call wasn't held")
	}
	r.api.pruned = map[string]bool{}
	for i := 0; i < 60; i++ {
		*r.now = r.now.Add(time.Second)
		exc, _ := r.call(t, "agent-key", amount(5000, fmt.Sprintf("op-pruned-%02d", i))).call["exception_id"].(string)
		r.api.pruned[exc] = true
	}
	text, calls, next := r.pendingPage(t, "agent-key", "")
	if len(calls) != 1 || calls[0].OperationID != "op-live" || calls[0].Answer != "waiting" || next != "" {
		t.Fatalf("60 pruned and 1 live: %d calls, next %q: %q", len(calls), next, text)
	}
	// When KIFF can't be asked, nothing is taken as gone: every hold says so.
	r.api.holdsFail = true
	if _, calls, _ := r.pendingPage(t, "agent-key", ""); len(calls) != 50 || calls[0].Answer != "unavailable" {
		t.Fatalf("KIFF down: %d calls, first %+v", len(calls), calls[0])
	}
}

// Invariant: kiff_pending's text names each listed call's arguments, and an owner's refusal reads as covering that one call, never the tool: an agent that reads only the text knows which item the owner refused, and another item's call of the same tool is still its to make.
// Category: authority
// Attack: the owner refuses a refund on one order; on its next run the agent reads kiff_pending's text, which named only the tool, and skips a valid refund on another order as if the owner had refused that too
// Refused: the text line carries the refused call's order and amount, and says the refusal covers this call only, so a call with other arguments is decided on its own
func TestPendingTextNamesTheCallTheOwnerRefused(t *testing.T) {
	r := newRig(t)
	refused := amount(5000, "op-refused")
	refused["order"] = "o-refused-7"
	exc, _ := r.call(t, "agent-key", refused).call["exception_id"].(string)
	*r.now = r.now.Add(time.Second)
	waiting := amount(4000, "op-waiting")
	waiting["order"] = "o-waiting-9"
	wexc, _ := r.call(t, "agent-key", waiting).call["exception_id"].(string)
	r.api.holds = map[string]kiffapi.Hold{
		exc:  {ExceptionID: exc, Status: "rejected"},
		wexc: {ExceptionID: wexc, Status: "held"},
	}
	text, _ := r.pending(t, "agent-key")
	var refusedLine, waitingLine string
	for _, line := range strings.Split(text, "\n") {
		switch {
		case strings.Contains(line, "op-refused"):
			refusedLine = line
		case strings.Contains(line, "op-waiting"):
			waitingLine = line
		}
	}
	for _, want := range []string{`"order":"o-refused-7"`, `"amount_eur":5000`, "refused this call", "covers this call only", "decided on its own"} {
		if !strings.Contains(refusedLine, want) {
			t.Errorf("the refused line has no %q: %s", want, refusedLine)
		}
	}
	if strings.Contains(refusedLine, "Do not call it again") || strings.Contains(refusedLine, "o-waiting-9") {
		t.Errorf("the refused line: %s", refusedLine)
	}
	if !strings.Contains(waitingLine, `"order":"o-waiting-9"`) || !strings.Contains(waitingLine, "Waiting") {
		t.Errorf("the waiting line: %s", waitingLine)
	}
}
