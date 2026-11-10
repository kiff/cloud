package gateway_test

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kiff/cloud/gateway/internal/gateway"
	"github.com/kiff/cloud/gateway/internal/invocation"
	"github.com/kiff/cloud/gateway/internal/mcp"
	"github.com/kiff/cloud/gateway/internal/thune"
)

// processorTool is a fake payment processor behind one connection, as its
// test mode answers: one account, charges, and their refunds. The refund
// write keeps the marker KIFF sends (here the idempotency argument, which
// the connection writes into metadata.kiff_op), and every read lists the
// refunds it holds.
type processorTool struct {
	mu       sync.Mutex
	charges  map[string]int64
	disputed map[string]bool
	refunds  []procRefund
	// writes are the refund calls received, with their arguments.
	writes []map[string]any
	// lose makes the next refund write take effect and lose its answer.
	lose bool
	// noDisputeFlag leaves the dispute flag out of every charge read.
	noDisputeFlag bool
}

type procRefund struct {
	ID, Charge, Status, Marker string
	Amount                     int64
}

func newProcessorTool() *processorTool {
	return &processorTool{charges: map[string]int64{"ch_1": 10000}, disputed: map[string]bool{}}
}

func (p *processorTool) CallTool(ctx context.Context, _, _, tool string, args json.RawMessage) (json.RawMessage, error) {
	var a map[string]any
	dec := json.NewDecoder(strings.NewReader(string(args)))
	dec.UseNumber()
	_ = dec.Decode(&a)
	p.mu.Lock()
	defer p.mu.Unlock()
	answer := func(v any) (json.RawMessage, error) {
		b, _ := json.Marshal(v)
		return mcp.TextResult(string(b), false), nil
	}
	switch tool {
	case "get_account":
		return answer(map[string]any{"id": "acct_test_1"})
	case "get_charge":
		ch := fmt.Sprint(a["charge"])
		charge := map[string]any{"id": ch, "currency": "eur", "amount_captured": p.charges[ch], "disputed": p.disputed[ch]}
		if p.noDisputeFlag {
			delete(charge, "disputed")
		}
		return answer(charge)
	case "list_refunds":
		ch := fmt.Sprint(a["charge"])
		data := []map[string]any{}
		for i := len(p.refunds) - 1; i >= 0; i-- { // newest first
			r := p.refunds[i]
			if r.Charge == ch {
				data = append(data, map[string]any{"id": r.ID, "amount": r.Amount, "status": r.Status, "metadata": map[string]any{"kiff_op": r.Marker}})
			}
		}
		return answer(map[string]any{"data": data})
	case "refund":
		p.writes = append(p.writes, a)
		n, _ := a["amount_eur"].(json.Number).Int64()
		r := procRefund{ID: fmt.Sprintf("re_%d", len(p.refunds)+1), Charge: fmt.Sprint(a["charge"]), Status: "pending", Amount: n,
			Marker: fmt.Sprint(a["idempotency_key"])}
		p.refunds = append(p.refunds, r)
		if p.lose {
			p.lose = false
			return nil, &mcp.CallError{Sent: true, Err: context.DeadlineExceeded}
		}
		return answer(map[string]any{"id": r.ID, "amount": r.Amount, "status": r.Status})
	}
	return nil, &mcp.RPCError{Code: -32601, Message: "no such tool " + tool}
}

// dashboard refunds in the processor's dashboard: no marker.
func (p *processorTool) dashboard(charge string, amount int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.refunds = append(p.refunds, procRefund{ID: fmt.Sprintf("re_%d", len(p.refunds)+1), Charge: charge, Status: "succeeded", Amount: amount})
}

func (p *processorTool) refundWrites() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.writes)
}

const procURL = "http://processor.test/mcp"

func readTool(name string) gateway.Tool {
	return gateway.Tool{Name: name, InputSchema: json.RawMessage(`{"type":"object"}`), UpstreamURL: procURL, UpstreamTool: name, ReadOnly: true}
}

var governedRefund = gateway.Tool{Name: "issue_refund", Description: "Refund a charge",
	InputSchema: json.RawMessage(`{"type":"object","properties":{"amount_eur":{"type":"integer"},"charge":{"type":"string"}},"required":["amount_eur","charge"]}`),
	UpstreamURL: procURL, UpstreamTool: "refund", AmountArg: "amount_eur", IdempotencyArg: "idempotency_key",
	NoEffect: []int{400, 402, 404},
	Case: &gateway.CaseProfile{Kind: thune.KindRefund, ChargeArg: "charge",
		AccountRead: gateway.CaseRead{Tool: "get_account", Path: "id"},
		ChargeRead:  gateway.CaseChargeRead{Tool: "get_charge", Args: map[string]string{"charge": "$args.charge"}, Currency: "currency", Captured: "amount_captured", Disputed: "disputed"},
		RefundsRead: gateway.CaseRefundsRead{Tool: "list_refunds", Args: map[string]string{"charge": "$args.charge"}, List: "data", ID: "id", Amount: "amount", Status: "status", Marker: "metadata.kiff_op"}}}

type refundRig struct {
	*rig
	proc  *processorTool
	thune *thune.Store
}

func newRefundRig(t *testing.T) *refundRig {
	t.Helper()
	r := newRig(t)
	ctx := context.Background()
	if err := thune.ApplySchema(ctx, r.pool); err != nil {
		t.Fatal(err)
	}
	if _, err := r.pool.Exec(ctx, `TRUNCATE gateway.thune_claims, gateway.thune_charge_locks, gateway.thune_adjustments, gateway.thune_refunds, gateway.thune_events, gateway.thune_watch_work`); err != nil {
		t.Fatal(err)
	}
	rr := &refundRig{rig: r, proc: newProcessorTool(), thune: thune.NewStore(r.pool)}
	rr.thune.SetClock(func() time.Time { return *r.now })
	r.gw.Forward = rr.proc
	r.gw.Thune = rr.thune
	r.gw.Watch = &thune.Watcher{Store: rr.thune, Calls: gateway.ThuneCalls{Store: r.store}}
	r.gw.Catalog = catalog{governedRefund, readTool("get_account"), readTool("get_charge"), readTool("list_refunds")}
	return rr
}

func caseCall(amount int, request, op string) map[string]any {
	a := map[string]any{"amount_eur": amount, "charge": "ch_1", gateway.OperationArg: op}
	if request != "" {
		a[gateway.RequestArg] = request
	}
	return a
}

func (r *refundRig) adjustment(t *testing.T, op string) thune.Adjustment {
	t.Helper()
	var key string
	if err := r.pool.QueryRow(context.Background(), `SELECT key FROM gateway.invocations WHERE operation_id=$1 AND tool='issue_refund'`, op).Scan(&key); err != nil {
		t.Fatalf("no call %s: %v", op, err)
	}
	a, err := r.thune.Get(context.Background(), key)
	if err != nil {
		t.Fatalf("no adjustment for %s: %v", op, err)
	}
	return a
}

func (r *refundRig) state(t *testing.T, op string) string {
	t.Helper()
	var s string
	if err := r.pool.QueryRow(context.Background(), `SELECT state FROM gateway.invocations WHERE operation_id=$1 AND tool='issue_refund'`, op).Scan(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

// Invariant: one refund request is handled once, whichever taf picks it up.
// Category: authority
// Attack: the same customer request reaches two tafs, each with its own key
// Refused: the second is told the first is handling it, and nothing is sent for it
func TestARefundRequestIsHandledOnceAcrossTafs(t *testing.T) {
	r := newRefundRig(t)
	if got := r.call(t, "agent-key", caseCall(2000/10, "ticket-7", "op-1")); got.isError {
		t.Fatalf("first taf: %+v", got)
	}
	got := r.call(t, "agent-key-2", caseCall(2000/10, "ticket-7", "op-2"))
	if !got.isError || !strings.Contains(got.text, "already handling this request") {
		t.Fatalf("second taf: %+v", got)
	}
	if r.proc.refundWrites() != 1 {
		t.Fatalf("%d refunds sent, want 1", r.proc.refundWrites())
	}
	a := r.adjustment(t, "op-1")
	if a.Execution != thune.ExecSent || a.Authorized != 200 || a.Identity.Account != "acct_test_1" || a.Identity.Request != "ticket-7" {
		t.Fatalf("first: %+v", a)
	}
	if b := r.adjustment(t, "op-2"); b.Execution != thune.ExecRefused || b.Authorized != 0 {
		t.Fatalf("second: %+v", b)
	}
	// The request id is KIFF's: the processor never sees it.
	if _, sent := r.proc.writes[0][gateway.RequestArg]; sent {
		t.Fatalf("the request id reached the processor: %v", r.proc.writes[0])
	}
}

// A second refund on a charge goes to the owner whatever the Card says,
// and so does one with no request.
func TestASecondRefundAndOneWithNoRequestAskTheOwner(t *testing.T) {
	r := newRefundRig(t)
	r.call(t, "agent-key", caseCall(200, "ticket-7", "op-1"))
	r.call(t, "agent-key", caseCall(100, "ticket-8", "op-2"))
	r.call(t, "agent-key", caseCall(100, "", "op-3"))
	asks := map[string]string{}
	for _, op := range []string{"op-1", "op-2", "op-3"} {
		var key string
		_ = r.pool.QueryRow(context.Background(), `SELECT key FROM gateway.invocations WHERE operation_id=$1`, op).Scan(&key)
		r.api.mu.Lock()
		asks[op], _ = r.api.params[key][gateway.OwnerAskParam].(string)
		r.api.mu.Unlock()
	}
	if asks["op-1"] != "" || asks["op-2"] != gateway.OwnerAskSecondRefund || asks["op-3"] != gateway.OwnerAskNoRequestID {
		t.Fatalf("owner asks: %v", asks)
	}
	// The fake API doesn't hold on the ask, so all three were sent: the
	// hold itself is apps/api's (owner ask). What matters here is what
	// KIFF was told.
}

// Invariant: KIFF never changes a refund's amount quietly.
// Category: authority
// Attack: an agent asks for more than the charge has left
// Refused: refused before KIFF decides it, with what is left; nothing is sent or counted
func TestARefundOverTheRemainderIsRefusedBeforeDeciding(t *testing.T) {
	r := newRefundRig(t)
	r.proc.dashboard("ch_1", 9500)
	got := r.call(t, "agent-key", caseCall(600, "ticket-7", "op-1"))
	if !got.isError || !strings.Contains(got.text, "has 500 left") || r.proc.refundWrites() != 0 {
		t.Fatalf("over the remainder: %+v, writes %d", got, r.proc.refundWrites())
	}
	// Only its reads were decided; the refund itself never was.
	if n, _ := r.api.totalDrawn(); n != 0 {
		t.Fatalf("drawn %d", n)
	}
	r.api.mu.Lock()
	defer r.api.mu.Unlock()
	for _, p := range r.api.params {
		if _, ok := p["amount_eur"]; ok {
			t.Fatalf("the refund was decided: %v", p)
		}
	}
}

// The acceptance scenario through the gateway: the first taf's answer is
// lost, the second is told the first is handling it, the owner refunds in
// the dashboard, and the next governed call on the account settles all of
// it without sending anything more.
func TestALostRefundIsFoundByItsMarkerAndNeverResent(t *testing.T) {
	r := newRefundRig(t)
	r.proc.lose = true
	got := r.call(t, "agent-key", caseCall(200, "ticket-7", "op-1"))
	if !strings.Contains(got.text, "response was lost") {
		t.Fatalf("lost: %+v", got)
	}
	// The call's own touch of the account read the charge right after,
	// and found its refund by its marker: the lost call is sent, without
	// anything being sent again.
	if r.state(t, "op-1") != string(invocation.StateForwarded) {
		t.Fatalf("the lost call is %s", r.state(t, "op-1"))
	}
	if a := r.adjustment(t, "op-1"); a.Execution != thune.ExecSent || a.Financial != thune.FinPending {
		t.Fatalf("thune after the read: %+v", a)
	}
	// The same taf retries the same call: replayed, not sent.
	r.call(t, "agent-key", caseCall(200, "ticket-7", "op-1"))
	// The second taf asks for the same request.
	got = r.call(t, "agent-key-2", caseCall(200, "ticket-7", "op-2"))
	if !got.isError || !strings.Contains(got.text, "already handling this request") {
		t.Fatalf("second taf: %+v", got)
	}
	// The owner refunds €20 in the dashboard; the next governed call on
	// the account (another request) sees it, and it is the owner's.
	r.proc.dashboard("ch_1", 200)
	r.call(t, "agent-key", caseCall(100, "ticket-9", "op-3"))
	rep, err := r.thune.ChargeReport(context.Background(), "t1", "acct_test_1", "ch_1")
	if err != nil {
		t.Fatal(err)
	}
	// KIFF's two (op-1, found by its marker, and op-3, which the fake API
	// allows) and the dashboard's: 10000 - 200 - 200 - 100.
	if len(rep.KIFF) != 2 || len(rep.Outside) != 1 || rep.Outside[0].Amount != 200 || rep.Left != 9500 || !rep.RefundedTwice {
		t.Fatalf("report: %+v", rep)
	}
	if !r.adjustmentAsked(t, "op-3") {
		t.Fatal("a refund on a charge refunded twice wasn't the owner's")
	}
	// Nothing was sent again: the first refund once, and op-3, which the
	// fake API allows.
	if r.proc.refundWrites() != 2 {
		t.Fatalf("%d refund writes", r.proc.refundWrites())
	}
}

func (r *refundRig) adjustmentAsked(t *testing.T, op string) bool {
	a := r.adjustment(t, op)
	return a.Verdict == thune.VerdictAsk
}

// A held refund whose charge changes while it waits is voided when it's
// retried, and nothing is sent.
func TestAHeldRefundIsVoidedWhenTheChargeChanges(t *testing.T) {
	r := newRefundRig(t)
	r.proc.charges["ch_1"] = 500000
	got := r.call(t, "agent-key", caseCall(5000, "ticket-7", "op-1"))
	if r.state(t, "op-1") != string(invocation.StateHeld) {
		t.Fatalf("not held: %+v", got)
	}
	if a := r.adjustment(t, "op-1"); a.Execution != thune.ExecHeld {
		t.Fatalf("thune: %+v", a)
	}
	*r.now = r.now.Add(3 * time.Hour)
	r.proc.dashboard("ch_1", 1000)
	r.api.approveAll()
	got = r.call(t, "agent-key", caseCall(5000, "ticket-7", "op-1"))
	if !got.isError || !strings.Contains(got.text, "changed while this refund waited") || r.proc.refundWrites() != 0 {
		t.Fatalf("after the charge changed: %+v, writes %d", got, r.proc.refundWrites())
	}
	if a := r.adjustment(t, "op-1"); a.Execution != thune.ExecVoid {
		t.Fatalf("thune: %+v", a)
	}
	// Retried once more: still not sent.
	r.call(t, "agent-key", caseCall(5000, "ticket-7", "op-1"))
	if r.proc.refundWrites() != 0 {
		t.Fatal("a voided approval was sent")
	}
}

// A held refund whose charge didn't change is sent once approved, once.
func TestAHeldRefundIsSentOnceApproved(t *testing.T) {
	r := newRefundRig(t)
	r.proc.charges["ch_1"] = 500000
	r.call(t, "agent-key", caseCall(5000, "ticket-7", "op-1"))
	*r.now = r.now.Add(3 * time.Hour)
	r.api.approveAll()
	if got := r.call(t, "agent-key", caseCall(5000, "ticket-7", "op-1")); got.isError {
		t.Fatalf("approved: %+v", got)
	}
	r.call(t, "agent-key", caseCall(5000, "ticket-7", "op-1"))
	if r.proc.refundWrites() != 1 {
		t.Fatalf("%d writes", r.proc.refundWrites())
	}
	if a := r.adjustment(t, "op-1"); a.Execution != thune.ExecSent {
		t.Fatalf("thune: %+v", a)
	}
}

// The agent is shown the request argument on a governed refund only.
func TestTheRequestArgumentIsListedOnGovernedRefunds(t *testing.T) {
	r := newRefundRig(t)
	_, out := r.rpc(t, "agent-key", "tools/list", map[string]any{})
	tools := out["result"].(map[string]any)["tools"].([]any)
	for _, x := range tools {
		tool := x.(map[string]any)
		props := tool["inputSchema"].(map[string]any)["properties"].(map[string]any)
		_, has := props[gateway.RequestArg]
		if (tool["name"] == "issue_refund") != has {
			t.Errorf("%s lists %s: %v", tool["name"], gateway.RequestArg, has)
		}
	}
}

// relayRefundRig is refundRig with its tools on a relay grant, as the
// connection serves them, the case naming the processor's refund event.
func relayRefundRig(t *testing.T) (*refundRig, *http.ServeMux) {
	t.Helper()
	r := newRefundRig(t)
	const up = "https://relay.test/mcp/t1/grt_1"
	r.gw.Relays = []string{"https://relay.test"}
	var tools catalog
	for _, x := range r.gw.Catalog.(catalog) {
		x.Mode, x.UpstreamURL, x.Audience = gateway.ModeRelay, up, up
		if x.Case != nil {
			c := *x.Case
			c.RefundEvent = &gateway.CaseRefundEvent{Event: "PAYMENTS_REFUND_UPDATED", Object: "refund", Charge: "charge"}
			x.Case = &c
		}
		tools = append(tools, x)
	}
	r.gw.Catalog = tools
	mux := http.NewServeMux()
	r.gw.RegisterRefundEvents(mux, "synthetic-event-secret")
	return r, mux
}

func postRefundEvent(t *testing.T, r *refundRig, mux *http.ServeMux, id, grant string, refund map[string]any, secret string) int {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"v": "planque.event.v1", "id": id, "tenant": "t1", "agent": "coding-agent", "grant": grant,
		"event": "PAYMENTS_REFUND_UPDATED", "payload": map[string]any{"refund": refund}})
	ts := strconv.FormatInt(r.now.Unix(), 10)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(id + "." + ts + "."))
	mac.Write(body)
	req := httptest.NewRequest(http.MethodPost, gateway.RefundEventsPath, bytes.NewReader(body))
	req.Header.Set("Planque-Event-Id", id)
	req.Header.Set("Planque-Event-Timestamp", ts)
	req.Header.Set("Planque-Event-Signature", "v1,"+base64.StdEncoding.EncodeToString(mac.Sum(nil)))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec.Code
}

// Invariant: a refund's later state comes from the connection's signed event, and changes only what it says.
// Category: authority
// Attack: an unsigned or forged event says a refund KIFF sent failed, to get the Card's room back
// Refused: an event without the connection's signature is refused; a signed one records the refund's state once, sends nothing, and frees the request only when the money didn't move
func TestARefundEventRecordsTheRefundsLaterState(t *testing.T) {
	r, mux := relayRefundRig(t)
	if got := r.call(t, "agent-key", caseCall(200, "ticket-7", "op-1")); got.isError {
		t.Fatalf("refund: %+v", got)
	}
	a := r.adjustment(t, "op-1")
	if a.Execution != thune.ExecSent || a.Financial != thune.FinPending {
		t.Fatalf("after sending: %+v", a)
	}
	failed := map[string]any{"id": a.RefundID, "amount": 200, "status": "failed", "charge": "ch_1", "metadata": map[string]any{"kiff_op": a.Key}}
	// Unsigned, or signed with another secret: refused, nothing recorded.
	if st := postRefundEvent(t, r, mux, "evk_1", "grt_1", failed, "another-secret"); st != http.StatusUnauthorized {
		t.Fatalf("a forged event: %d", st)
	}
	if b := r.adjustment(t, "op-1"); b.Financial != thune.FinPending {
		t.Fatalf("a forged event changed the refund: %+v", b)
	}
	// Another connection's grant, or an event about a charge KIFF never
	// refunded: taken, nothing recorded.
	if st := postRefundEvent(t, r, mux, "evk_2", "grt_other", failed, "synthetic-event-secret"); st != http.StatusAccepted || r.adjustment(t, "op-1").Financial != thune.FinPending {
		t.Fatalf("another grant's event: %d", st)
	}
	elsewhere := map[string]any{"id": "re_x", "amount": 100, "status": "succeeded", "charge": "ch_never", "metadata": map[string]any{}}
	if st := postRefundEvent(t, r, mux, "evk_3", "grt_1", elsewhere, "synthetic-event-secret"); st != http.StatusAccepted {
		t.Fatalf("an unknown charge: %d", st)
	}
	// Signed, about our refund: its money failed, the request is free again.
	for i := 0; i < 2; i++ {
		if st := postRefundEvent(t, r, mux, "evk_4", "grt_1", failed, "synthetic-event-secret"); st != http.StatusAccepted {
			t.Fatalf("the event (%d): %d", i, st)
		}
	}
	if b := r.adjustment(t, "op-1"); b.Execution != thune.ExecSent || b.Financial != thune.FinFailed {
		t.Fatalf("after the event: %+v", b)
	}
	writes := r.proc.refundWrites()
	if got := r.call(t, "agent-key-2", caseCall(200, "ticket-7", "op-2")); got.isError {
		t.Fatalf("the request after its refund failed: %+v", got)
	}
	if r.proc.refundWrites() != writes+1 {
		t.Fatal("the request wasn't free after its refund failed")
	}
	// A failed refund never succeeds again.
	ok := failed
	ok["status"] = "succeeded"
	postRefundEvent(t, r, mux, "evk_5", "grt_1", ok, "synthetic-event-secret")
	if b := r.adjustment(t, "op-1"); b.Financial != thune.FinFailed {
		t.Fatalf("an ended refund moved: %+v", b)
	}
}
