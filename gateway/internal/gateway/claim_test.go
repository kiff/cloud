package gateway_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/gateway/internal/gateway"
	"github.com/kiff/cloud/gateway/internal/thune"
)

// claimedTool answers a dispute: a write marked dispute_answer, on the
// order its call names.
func claimedTool() gateway.Tool {
	t := refundTool
	t.ResourceArg, t.Claim = "order", "dispute_answer"
	return t
}

func (r *rig) callAs(t *testing.T, key string, args map[string]any) result {
	t.Helper()
	status, out := r.rpc(t, key, "tools/call", map[string]any{"name": "issue_refund", "arguments": args})
	if status != 200 || out["result"] == nil {
		t.Fatalf("tools/call: %d %v", status, out)
	}
	res := out["result"].(map[string]any)
	var texts []string
	for _, c := range res["content"].([]any) {
		texts = append(texts, c.(map[string]any)["text"].(string))
	}
	meta, _ := res["_meta"].(map[string]any)
	call, _ := meta[gateway.MetaKey].(map[string]any)
	isErr, _ := res["isError"].(bool)
	return result{text: strings.Join(texts, "\n"), isError: isErr, call: call}
}

func (f *fakeAPI) decisions() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.params)
}

// Invariant: while one agent's call of a claimed kind is being decided, held or sent on an object, another agent's call of that kind on that object is refused before KIFF decides it.
// Category: integrity
// Attack: two tafs answer the same dispute at once, or one answers while the other's answer waits for the owner
// Refused: the second agent's call is refused with the holder and its state; nothing is decided, drawn or sent for it
func TestASecondAgentOnAClaimedObjectIsRefusedWhileTheFirstHoldsIt(t *testing.T) {
	r := newRig(t)
	r.gw.Catalog = catalog{claimedTool()}

	// The first agent's answer waits for the owner (over the Card's €1,000).
	held := r.callAs(t, "agent-key", orderCall(5000, "o-dsp", "op-first"))
	if held.call["state"] != "held" {
		t.Fatalf("first: %+v", held)
	}
	decided := r.api.decisions()

	got := r.callAs(t, "chat-key", orderCall(200, "o-dsp", "op-second"))
	if got.call["state"] != "refused" || !strings.Contains(got.text, "coding-agent") || !strings.Contains(got.text, "waiting for the owner's approval") {
		t.Fatalf("second agent: %+v", got)
	}
	if reasons, _ := got.call["reasons"].([]any); len(reasons) != 1 || reasons[0] != gateway.ReasonObjectClaimed {
		t.Fatalf("reasons: %v", got.call["reasons"])
	}
	if r.api.decisions() != decided || r.tool.count() != 0 {
		t.Fatalf("decided %d (was %d), sent %d: the second call must be neither", r.api.decisions(), decided, r.tool.count())
	}

	// The same agent's own next call on the object goes through.
	if own := r.callAs(t, "agent-key", orderCall(200, "o-dsp", "op-own")); own.call["state"] != "forwarded" {
		t.Fatalf("the holder's own call: %+v", own)
	}
	// And doesn't hand the object away: the first call still waits for the owner.
	if again := r.callAs(t, "chat-key", orderCall(200, "o-dsp", "op-after-own")); again.call["state"] != "refused" {
		t.Fatalf("the second agent after the holder's own call: %+v", again)
	}
	// Another object, or another kind, isn't claimed by it.
	if other := r.callAs(t, "chat-key", orderCall(200, "o-other", "op-other-object")); other.call["state"] != "forwarded" {
		t.Fatalf("another object: %+v", other)
	}
	reminder := claimedTool()
	reminder.Claim = "reminder"
	r.gw.Catalog = catalog{reminder}
	if kind := r.callAs(t, "chat-key", orderCall(200, "o-dsp", "op-other-kind")); kind.call["state"] != "forwarded" {
		t.Fatalf("another kind: %+v", kind)
	}
}

// Each of the holder's calls holds the object while it counts: an earlier
// call that lapses doesn't free it while a later one still waits.
func TestAClaimHoldsWhileAnyOfItsAgentsCallsCounts(t *testing.T) {
	r := newRig(t)
	r.gw.Catalog = catalog{claimedTool()}
	// The first waits for the owner with no expiry: it holds for ClaimLease.
	if first := r.callAs(t, "agent-key", orderCall(5000, "o-dsp", "op-held")); first.call["state"] != "held" {
		t.Fatalf("first: %+v", first)
	}
	// Ten minutes on, the same agent's second waits too, for an hour.
	*r.now = r.now.Add(10 * time.Minute)
	expiry := r.now.Add(time.Hour)
	r.api.holdExpiry = &expiry
	if second := r.callAs(t, "agent-key", orderCall(5000, "o-dsp", "op-held-hour")); second.call["state"] != "held" {
		t.Fatalf("second: %+v", second)
	}
	// Past the first's lease, the second still waits: another agent is refused.
	*r.now = r.now.Add(gateway.ClaimLease)
	if got := r.callAs(t, "chat-key", orderCall(200, "o-dsp", "op-other")); got.call["state"] != "refused" || !strings.Contains(got.text, "coding-agent") {
		t.Fatalf("while the second waits: %+v", got)
	}
	// Past the second's hold, nothing counts and the other agent takes it.
	*r.now = expiry.Add(time.Minute)
	if got := r.callAs(t, "chat-key", orderCall(200, "o-dsp", "op-other-late")); got.call["state"] != "forwarded" {
		t.Fatalf("past both: %+v", got)
	}
}

// Once the holder's call has ended, the next agent takes the object.
func TestAClaimEndsWithItsHoldersCall(t *testing.T) {
	r := newRig(t)
	r.gw.Catalog = catalog{claimedTool()}
	if first := r.callAs(t, "agent-key", orderCall(200, "o-dsp", "op-sent")); first.call["state"] != "forwarded" {
		t.Fatalf("first: %+v", first)
	}
	if second := r.callAs(t, "chat-key", orderCall(200, "o-dsp", "op-after")); second.call["state"] != "forwarded" {
		t.Fatalf("after the holder's call was sent: %+v", second)
	}
}

// A held call without an expiry holds the object for ClaimLease, no
// longer; one with an expiry, until it.
func TestAClaimLapsesWithItsLeaseOrItsHold(t *testing.T) {
	r := newRig(t)
	r.gw.Catalog = catalog{claimedTool()}
	r.callAs(t, "agent-key", orderCall(5000, "o-dsp", "op-held"))
	*r.now = r.now.Add(gateway.ClaimLease + time.Minute)
	if got := r.callAs(t, "chat-key", orderCall(200, "o-dsp", "op-late")); got.call["state"] != "forwarded" {
		t.Fatalf("past the lease: %+v", got)
	}

	r2 := newRig(t)
	r2.gw.Catalog = catalog{claimedTool()}
	expiry := r2.now.Add(time.Hour)
	r2.api.holdExpiry = &expiry
	r2.callAs(t, "agent-key", orderCall(5000, "o-dsp", "op-held-hour"))
	*r2.now = r2.now.Add(gateway.ClaimLease + time.Minute)
	if got := r2.callAs(t, "chat-key", orderCall(200, "o-dsp", "op-in-hold")); got.call["state"] != "refused" {
		t.Fatalf("within the hold's hour: %+v", got)
	}
	*r2.now = expiry.Add(time.Minute)
	if got := r2.callAs(t, "chat-key", orderCall(200, "o-dsp", "op-past-hold")); got.call["state"] != "forwarded" {
		t.Fatalf("past the hold: %+v", got)
	}
}

// Without a claim on the operation, or for a read, nothing is claimed.
func TestOnlyAClaimedWriteClaimsItsObject(t *testing.T) {
	for name, edit := range map[string]func(*gateway.Tool){
		"no claim": func(t *gateway.Tool) { t.Claim = "" },
		"a read":   func(t *gateway.Tool) { t.ReadOnly = true },
	} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t)
			tool := claimedTool()
			edit(&tool)
			r.gw.Catalog = catalog{tool}
			r.callAs(t, "agent-key", orderCall(5000, "o-dsp", "op-a"))
			if got := r.callAs(t, "chat-key", orderCall(200, "o-dsp", "op-b")); got.call["state"] == "refused" {
				t.Fatalf("claimed without a claim: %+v", got)
			}
			var n int
			if err := r.pool.QueryRow(context.Background(), `SELECT count(*) FROM gateway.object_claims`).Scan(&n); err != nil || n != 0 {
				t.Fatalf("claims %d %v", n, err)
			}
		})
	}
}

// Invariant: an agent answering a dispute is shown the refunds already made on the dispute's charge, KIFF's and outside KIFF's, and its answer is still sent.
// Category: integrity
// Attack: one taf refunds the order while another contests its dispute, each unaware of the other
// Refused: nothing is refused; the dispute answer carries the refunds already made on its charge
func TestADisputeAnswerIsShownTheRefundsAlreadyMadeOnItsCharge(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	if err := thune.ApplySchema(ctx, r.pool); err != nil {
		t.Fatal(err)
	}
	if _, err := r.pool.Exec(ctx, `TRUNCATE gateway.thune_claims, gateway.thune_charge_locks, gateway.thune_adjustments, gateway.thune_refunds, gateway.thune_events, gateway.thune_watch_work`); err != nil {
		t.Fatal(err)
	}
	r.gw.Thune = thune.NewStore(r.pool)
	dispute := disputeAnswer
	dispute.Claim = "dispute_answer"
	// The amount lookup's read names the charge: here the order's id.
	dispute.ClaimRelated = &gateway.ClaimRelated{Kind: gateway.RelatedRefund, Path: "id"}
	r.gw.Catalog = catalog{dispute, orderDetails}
	r.tool.amounts = map[string]any{"o-dsp": 300, "o-clean": 300}

	clean := r.callAs(t, "agent-key", disputeCall("o-clean", "op-clean"))
	if clean.call["state"] != "forwarded" || strings.Contains(clean.text, "Refunds already made") {
		t.Fatalf("no refund on its charge: %+v", clean)
	}

	if _, err := r.pool.Exec(ctx, `INSERT INTO gateway.thune_refunds (tenant_id, account, refund_id, object, amount, status, marker, origin, seen_at, updated_at)
		VALUES ('t1', 'acct_1', 're_out', 'o-dsp', 4500, 'succeeded', '', 'outside', now(), now()),
		       ('t1', 'acct_1', 're_failed', 'o-dsp', 999, 'failed', '', 'outside', now(), now()),
		       ('t2', 'acct_2', 're_other_tenant', 'o-dsp', 777, 'succeeded', '', 'outside', now(), now())`); err != nil {
		t.Fatal(err)
	}
	got := r.callAs(t, "agent-key", disputeCall("o-dsp", "op-dsp"))
	if got.call["state"] != "forwarded" || !strings.Contains(got.text, "Refunds already made on o-dsp: 4500, outside KIFF, succeeded") {
		t.Fatalf("the refund made on its charge: %+v", got)
	}
	if strings.Contains(got.text, "999") || strings.Contains(got.text, "777") {
		t.Fatalf("a failed refund, or another account's, was shown: %s", got.text)
	}
	if answers(r) != 2 {
		t.Fatalf("both answers are sent: %d", answers(r))
	}
}
