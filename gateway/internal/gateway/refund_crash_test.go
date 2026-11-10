package gateway_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
	"time"

	"github.com/kiff/cloud/gateway/internal/gateway"
	"github.com/kiff/cloud/gateway/internal/invocation"
	"github.com/kiff/cloud/gateway/internal/thune"
)

type fixedReader struct{ f thune.Facts }

func (r fixedReader) ReadCharge(context.Context, string, string, string) (thune.Facts, error) {
	return r.f, nil
}

// died begins a governed refund as agent-key and runs thune's Before for
// it, as refundFirst does, then stops: the gateway died there.
func died(t *testing.T, r *refundRig, op string) string {
	t.Helper()
	ctx := context.Background()
	sum := sha256.Sum256([]byte("agent-key"))
	in, created, err := r.store.Begin(ctx, invocation.Call{TenantID: "t1",
		CredentialID: hex.EncodeToString(sum[:16]), AgentID: "coding-agent", Tool: "issue_refund",
		OperationID: op, Arguments: json.RawMessage(`{"amount_eur":200,"charge":"ch_1","kiff_request_id":"ticket-7"}`)})
	if err != nil || !created {
		t.Fatalf("begin: %v %v", created, err)
	}
	id := thune.Identity{TenantID: "t1", Account: "acct_test_1", Object: "ch_1", Kind: thune.KindRefund, Request: "ticket-7"}
	g := &thune.Gate{Store: r.thune, Reader: fixedReader{thune.Facts{Account: "acct_test_1", Charge: "ch_1", Currency: "eur", Captured: 10000, ReadAt: *r.now}}}
	b, err := g.Before(ctx, id, thune.Ask{Amount: 200}, in.Key)
	if err != nil || !b.Claim.Won || b.Case.Verdict != thune.VerdictAllow {
		t.Fatalf("before: %+v %v", b, err)
	}
	return in.Key
}

// Invariant: a refund call the gateway died in is settled in thune when the sweep ends it.
// Category: failure-modes
// Attack: the gateway dies in the middle of a refund call, before or after sending it
// Refused: the sweep's end reaches thune: not sent releases the request, unknown is watched and found by its marker, never resent
//
// The gateway dies after thune's Before and before the decision. The
// sweep ends the call as failed; thune records it not sent and releases
// the request, so another taf can take it.
func TestAGatewayCrashBeforeSendingReleasesTheClaim(t *testing.T) {
	r := newRefundRig(t)
	ctx := context.Background()
	key := died(t, r, "op-crash")
	*r.now = r.now.Add(gateway.DefaultLease + time.Second)
	if _, err := r.gw.SweepStale(ctx); err != nil {
		t.Fatal(err)
	}
	if s := r.state(t, "op-crash"); s != string(invocation.StateFailed) {
		t.Fatalf("invocation is %s, want failed", s)
	}
	a, _ := r.thune.Get(ctx, key)
	if a.Execution != thune.ExecNotSent {
		t.Errorf("thune execution = %s, want not_sent", a.Execution)
	}
	got := r.call(t, "agent-key-2", caseCall(200, "ticket-7", "op-2"))
	if got.isError || r.proc.refundWrites() != 1 {
		t.Errorf("second taf, same request: isError=%v text=%q refund writes=%d; want one refund", got.isError, got.text, r.proc.refundWrites())
	}
}

// A stale retry settles the call it ends at once: the second taf can take
// the request without waiting for the sweep.
func TestAStaleRetrySettlesTheRefundItEnds(t *testing.T) {
	r := newRefundRig(t)
	ctx := context.Background()
	key := died(t, r, "op-crash")
	// The adjustment is written after the call's record (the charge read
	// sits between them): when the call turns stale, it is still younger
	// than a lease.
	if _, err := r.pool.Exec(ctx, `UPDATE gateway.thune_adjustments SET updated_at = updated_at + interval '5 seconds' WHERE key=$1`, key); err != nil {
		t.Fatal(err)
	}
	*r.now = r.now.Add(gateway.DefaultLease + time.Second)
	// The same agent retries the call that died, with its operation id.
	r.call(t, "agent-key", map[string]any{"amount_eur": 200, "charge": "ch_1", gateway.RequestArg: "ticket-7", gateway.OperationArg: "op-crash"})
	if s := r.state(t, "op-crash"); s != string(invocation.StateFailed) {
		t.Fatalf("invocation is %s, want failed", s)
	}
	if a, _ := r.thune.Get(ctx, key); a.Execution != thune.ExecNotSent {
		t.Fatalf("thune execution = %s right after the stale retry, want not_sent", a.Execution)
	}
	got := r.call(t, "agent-key-2", caseCall(200, "ticket-7", "op-2"))
	if got.isError || r.proc.refundWrites() != 1 {
		t.Errorf("second taf, same request, no sweep: isError=%v text=%q writes=%d", got.isError, got.text, r.proc.refundWrites())
	}
}

// The gateway dies after it recorded sending and the refund left. The
// sweep ends the call unknown; thune records it unknown and watches it;
// the next governed call on the charge finds the refund by its marker,
// and both records say sent, never resent.
func TestAGatewayCrashWhileSendingIsSettledByItsMarker(t *testing.T) {
	r := newRefundRig(t)
	ctx := context.Background()
	key := died(t, r, "op-crash")
	if ok, err := r.store.Transition(ctx, key, []invocation.State{invocation.StateDeciding}, invocation.StateForwarding, invocation.Update{}); !ok || err != nil {
		t.Fatal(ok, err)
	}
	if ok, err := r.thune.SetExecution(ctx, key, []string{thune.ExecAllowed, thune.ExecHeld}, thune.ExecSending); !ok || err != nil {
		t.Fatal(ok, err)
	}
	r.proc.mu.Lock()
	r.proc.refunds = append(r.proc.refunds, procRefund{ID: "re_1", Charge: "ch_1", Status: "pending", Amount: 200, Marker: key})
	r.proc.mu.Unlock()
	*r.now = r.now.Add(gateway.DefaultLease + time.Second)
	if _, err := r.gw.SweepStale(ctx); err != nil {
		t.Fatal(err)
	}
	a, _ := r.thune.Get(ctx, key)
	var watched int
	_ = r.pool.QueryRow(ctx, `SELECT count(*) FROM gateway.thune_watch_work WHERE key=$1`, key).Scan(&watched)
	if a.Execution != thune.ExecUnknown || watched != 1 {
		t.Fatalf("after the sweep: execution=%s watch rows=%d, want unknown and watched", a.Execution, watched)
	}
	r.call(t, "agent-key", caseCall(100, "ticket-9", "op-3"))
	a, _ = r.thune.Get(ctx, key)
	if a.Execution != thune.ExecSent || a.RefundID != "re_1" || r.state(t, "op-crash") != string(invocation.StateForwarded) {
		t.Errorf("crashed call: thune execution=%s refund=%q invocation=%s; want sent, re_1, forwarded",
			a.Execution, a.RefundID, r.state(t, "op-crash"))
	}
	// The next call is a second refund on the charge, so the owner is
	// asked (the fake API doesn't hold on the ask, so it is sent); the
	// crashed call itself is never sent again.
	if n := r.proc.refundWrites(); n != 1 {
		t.Errorf("refund writes = %d, want 1 (the next call's): the crashed call was resent", n)
	}
	var next string
	_ = r.pool.QueryRow(ctx, `SELECT key FROM gateway.invocations WHERE operation_id='op-3'`).Scan(&next)
	r.api.mu.Lock()
	ask, _ := r.api.params[next][gateway.OwnerAskParam].(string)
	r.api.mu.Unlock()
	if ask != gateway.OwnerAskSecondRefund {
		t.Errorf("next call's owner ask = %q, want %q", ask, gateway.OwnerAskSecondRefund)
	}
}

// A charge read that carries no dispute flag is not read as undisputed:
// the refund is never sent, as with a missing currency or captured amount.
func TestAChargeReadWithNoDisputeFlagSendsNoRefund(t *testing.T) {
	r := newRefundRig(t)
	r.proc.mu.Lock()
	r.proc.noDisputeFlag = true
	r.proc.mu.Unlock()
	got := r.call(t, "agent-key", caseCall(200, "ticket-7", "op-1"))
	if !got.isError || r.proc.refundWrites() != 0 {
		t.Fatalf("a charge with no dispute flag: %+v, writes %d", got, r.proc.refundWrites())
	}
}
