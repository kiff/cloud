package gateway_test

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kiff/cloud/gateway/internal/gateway"
	"github.com/kiff/cloud/gateway/internal/mcp"
)

func (f *fakeAPI) SurchargeEffects(_ context.Context, _, proposal string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.surchargeFails > 0 {
		f.surchargeFails--
		return errors.New("kiffapi: 503: down")
	}
	f.surcharged = append(f.surcharged, proposal)
	return nil
}

type pauser struct {
	mu     sync.Mutex
	paused map[string]string
}

func (p *pauser) PauseOperation(_ context.Context, tenant, tool, reason string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.paused == nil {
		p.paused = map[string]string{}
	}
	p.paused[tool] = reason
	return nil
}

// Invariant: a write whose vendor answer doesn't match what KIFF decided is flagged and its operation paused.
// Category: authority
// Attack: a vendor refunds 700 where the Card decided 500, or a read-back that can't be made
// Refused: the call is recorded as sent and flagged; the operation is paused and nothing more is sent through it until the owner resumes it
func TestAVerifyReadThatDiffersPausesTheOperation(t *testing.T) {
	r := newRig(t)
	p := &pauser{}
	r.gw.Operations = p
	refund := refundTool
	refund.ResourceArg = "order"
	refund.Verify = &gateway.VerifyRead{Tool: "order_details", Args: map[string]string{"order": "$args.order"}, AmountPath: "amount"}
	r.gw.Catalog = catalog{refund, orderDetails}
	r.tool.amounts = map[string]any{"o-same": 500, "o-more": 700}

	if got := r.call(t, "agent-key", orderCall(500, "o-same", "op-same")); got.isError || strings.Contains(got.text, "didn't check out") || len(p.paused) != 0 {
		t.Fatalf("the vendor used what KIFF decided: %+v, paused %v", got, p.paused)
	}
	var result string
	if err := r.pool.QueryRow(context.Background(), `SELECT verify_evidence->>'result' FROM gateway.invocations WHERE operation_id = 'op-same'`).Scan(&result); err != nil || result != "match" {
		t.Fatalf("evidence kept: %q %v", result, err)
	}

	got := r.call(t, "agent-key", orderCall(500, "o-more", "op-more"))
	if !strings.Contains(got.text, "the vendor used 700, KIFF decided 500") || p.paused["issue_refund"] == "" || writes(r) != 2 {
		t.Fatalf("the vendor used more: %+v, paused %v, writes %d", got, p.paused, writes(r))
	}
	var vendor int64
	var key string
	if err := r.pool.QueryRow(context.Background(), `SELECT key, (verify_evidence->>'vendor_amount')::bigint FROM gateway.invocations WHERE operation_id = 'op-more'`).Scan(&key, &vendor); err != nil || vendor != 700 {
		t.Fatalf("vendor amount kept: %d %v", vendor, err)
	}
	// The vendor used more: the Card is asked to count the larger figure,
	// for that call only.
	if len(r.api.surcharged) != 1 || r.api.surcharged[0] != key {
		t.Fatalf("surcharged %v, want [%s]", r.api.surcharged, key)
	}

	// A read-back that can't be made pauses too.
	delete(p.paused, "issue_refund")
	if got := r.call(t, "agent-key", orderCall(500, "o-none", "op-none")); !strings.Contains(got.text, "doesn't have amount") || p.paused["issue_refund"] == "" {
		t.Fatalf("no amount in the read: %+v, paused %v", got, p.paused)
	}

	// Paused, nothing more is sent through it; its reads still work.
	refund.Paused = p.paused["issue_refund"]
	r.gw.Catalog = catalog{refund, orderDetails}
	sent := writes(r)
	if got := r.call(t, "agent-key", orderCall(500, "o-same", "op-after")); !got.isError || !strings.Contains(got.text, "is paused") || writes(r) != sent {
		t.Fatalf("a paused operation: %+v, writes %d", got, writes(r))
	}
}

// The first three sent calls of a money operation no test call proved are
// marked for the owner to confirm, and no more (connection profiles: probation); a proven one
// marks none.
func TestTheFirstThreeCallsOfAnUntriedOperationAreOnProbation(t *testing.T) {
	r := newRig(t)
	rev := int64(1)
	tool := refundTool
	tool.Probation, tool.ProfileRevision = true, &rev
	r.gw.Catalog = catalog{tool}
	for i := 0; i < 5; i++ {
		if got := r.call(t, "agent-key", amount(100+i, "op-p"+strconv.Itoa(i))); got.isError {
			t.Fatalf("call %d: %+v", i, got)
		}
	}
	var n int
	if err := r.pool.QueryRow(context.Background(), `SELECT count(*) FROM gateway.invocations WHERE probation`).Scan(&n); err != nil || n != 3 {
		t.Fatalf("marked %d %v, want 3", n, err)
	}
	tool.Probation = false
	r.gw.Catalog = catalog{tool}
	if got := r.call(t, "agent-key", amount(200, "op-proven")); got.isError {
		t.Fatal(got)
	}
	if err := r.pool.QueryRow(context.Background(), `SELECT count(*) FROM gateway.invocations WHERE probation`).Scan(&n); err != nil || n != 3 {
		t.Fatalf("a proven operation marked a call: %d %v", n, err)
	}
}

// A surcharge that failed when the vendor used more is asked again each
// time the call is collected, until KIFF has it; the tool isn't called again.
func TestASurchargeThatFailedIsCountedOnCollection(t *testing.T) {
	r := newRig(t)
	r.gw.Operations = &pauser{}
	refund := refundTool
	refund.ResourceArg = "order"
	refund.Verify = &gateway.VerifyRead{Tool: "order_details", Args: map[string]string{"order": "$args.order"}, AmountPath: "amount"}
	r.gw.Catalog = catalog{refund, orderDetails}
	r.tool.amounts = map[string]any{"o-more": 700}
	r.api.surchargeFails = 1
	r.call(t, "agent-key", orderCall(500, "o-more", "op-more"))
	if len(r.api.surcharged) != 0 || writes(r) != 1 {
		t.Fatalf("the first ask failed: %v, writes %d", r.api.surcharged, writes(r))
	}
	r.call(t, "agent-key", orderCall(500, "o-more", "op-more"))
	if len(r.api.surcharged) != 1 || writes(r) != 1 {
		t.Fatalf("collected: surcharged %v, writes %d", r.api.surcharged, writes(r))
	}
}

// A check that didn't hold, or hasn't finished, pauses its operation from
// what the call keeps, even when keeping the pause failed or the check
// crashed: no write is sent through it until the owner resumes it.
func TestAVerifyReadThatDidntHoldPausesFromItsRecord(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	if _, err := r.pool.Exec(ctx, `DELETE FROM gateway.tools WHERE tenant_id = 'tenant-v';
		INSERT INTO gateway.tools (tenant_id, name, input_schema, upstream_url, upstream_tool) VALUES
		('tenant-v', 'refund', '{}', 'http://t/mcp', 'refund'), ('tenant-v', 'fresh', '{}', 'http://t/mcp', 'refund'),
		('tenant-v', 'overdue', '{}', 'http://t/mcp', 'refund')`); err != nil {
		t.Fatal(err)
	}
	add := func(key, tool, result string, at time.Time) {
		t.Helper()
		if _, err := r.pool.Exec(ctx, `INSERT INTO gateway.invocations (key, tenant_id, credential_id, agent_id, tool, operation_id, args_hash, arguments, state, created_at, updated_at, verify_evidence)
			VALUES ($1, 'tenant-v', 'c', 'a', $2, '', 'h', '{}', 'forwarded', $3, $3, jsonb_build_object('result', $4::text))`, key, tool, at, result); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()
	add("v-mismatch", "refund", "mismatch", now.Add(-time.Minute))
	add("v-fresh", "fresh", "pending", now)
	add("v-overdue", "overdue", "pending", now.Add(-5*time.Minute))
	paused := func() map[string]string {
		tools, err := gateway.PostgresCatalog{Pool: r.pool}.Tools(ctx, "tenant-v")
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]string{}
		for _, tl := range tools {
			out[tl.Name] = tl.Paused
		}
		return out
	}
	if p := paused(); p["refund"] == "" || p["fresh"] != "the check of an earlier call hasn't finished" || p["overdue"] == "" {
		t.Fatalf("paused %q", p)
	}
	if _, err := r.pool.Exec(ctx, `UPDATE gateway.tools SET resumed_at = now() WHERE tenant_id = 'tenant-v' AND name = 'refund'`); err != nil {
		t.Fatal(err)
	}
	if p := paused(); p["refund"] != "" {
		t.Fatalf("resumed, still paused: %q", p)
	}
}

// refundsVendor makes a refund for each write, marked with the call's
// operation id as planque marks it (metadata.kiff_op), and lists a
// payment's refunds newest first. dashboard adds a refund made in the
// vendor's own dashboard, unmarked, after each write.
type refundsVendor struct {
	mu        sync.Mutex
	refunds   []map[string]any
	dashboard int
	writes    int
}

func (v *refundsVendor) CallTool(_ context.Context, _, _, tool string, args json.RawMessage) (json.RawMessage, error) {
	var a map[string]any
	_ = json.Unmarshal(args, &a)
	v.mu.Lock()
	defer v.mu.Unlock()
	if tool == "list_refunds" {
		list := make([]map[string]any, 0, len(v.refunds))
		for i := len(v.refunds) - 1; i >= 0; i-- {
			list = append(list, v.refunds[i])
		}
		b, _ := json.Marshal(map[string]any{"data": list})
		return mcp.TextResult(string(b), false), nil
	}
	v.writes++
	mine := map[string]any{"id": "re_" + strconv.Itoa(len(v.refunds)), "amount": a["amount_eur"],
		"metadata": map[string]any{"kiff_op": a["idempotency_key"]}}
	v.refunds = append(v.refunds, mine)
	if v.dashboard > 0 {
		v.refunds = append(v.refunds, map[string]any{"id": "re_dash", "amount": v.dashboard, "metadata": map[string]any{}})
	}
	b, _ := json.Marshal(mine)
	return mcp.TextResult(string(b), false), nil
}

// Invariant: a refund's read-back compares the refund this call made, found by its marker, never the newest refund on the payment.
// Category: authority
// Attack: the owner refunds 2,000 in the vendor's dashboard between a 500 refund and KIFF's read-back
// Refused: the read-back finds the call's own refund by the operation id the connection wrote on it, and matches; a read with no refund marked with the call is flagged and pauses the operation
func TestARefundsReadBackFindsItsOwnRefundByItsMarker(t *testing.T) {
	r := newRig(t)
	p := &pauser{}
	r.gw.Operations = p
	vendor := &refundsVendor{dashboard: 2000}
	r.gw.Forward = vendor
	refund := refundTool
	refund.Mode, refund.Audience = gateway.ModeRelay, "https://relay.example.com/mcp"
	refund.Verify = &gateway.VerifyRead{Tool: "list_refunds", Args: map[string]string{"order": "$args.order"},
		AmountPath: "data[0].amount", Marker: "metadata.kiff_op"}
	list := gateway.Tool{Name: "list_refunds", Description: "List an order's refunds",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"order":{"type":"string"}},"required":["order"]}`),
		UpstreamURL: refund.UpstreamURL, UpstreamTool: "list_refunds", ReadOnly: true, Mode: refund.Mode, Audience: refund.Audience}
	r.gw.Catalog = catalog{refund, list}

	// The dashboard's 2,000 is the newest refund on the payment; the
	// read-back still finds this call's 500 and matches.
	got := r.call(t, "agent-key", orderCall(500, "o-1", "op-own"))
	if got.isError || strings.Contains(got.text, "didn't check out") || len(p.paused) != 0 {
		t.Fatalf("a dashboard refund made the call's read-back fail: %+v, paused %v", got, p.paused)
	}
	var result string
	var decided, vendorAmount int64
	if err := r.pool.QueryRow(context.Background(), `SELECT verify_evidence->>'result', (verify_evidence->>'decided_amount')::bigint,
		(verify_evidence->>'vendor_amount')::bigint FROM gateway.invocations WHERE operation_id = 'op-own'`).Scan(&result, &decided, &vendorAmount); err != nil ||
		result != "match" || decided != 500 || vendorAmount != 500 {
		t.Fatalf("evidence: %q %d %d %v", result, decided, vendorAmount, err)
	}

	// A read with no refund marked with this call can't vouch for it: the
	// call is flagged and the operation paused, as for any read that
	// doesn't check out.
	vendor.mu.Lock()
	vendor.refunds = nil
	vendor.mu.Unlock()
	r.gw.Forward = &unmarkedRefunds{refundsVendor: vendor}
	got = r.call(t, "agent-key", orderCall(500, "o-1", "op-unmarked"))
	if !strings.Contains(got.text, "no record marked with this call") || p.paused["issue_refund"] == "" {
		t.Fatalf("an unmarked refund: %+v, paused %v", got, p.paused)
	}
}

// unmarkedRefunds makes refunds without the call's marker, as a vendor
// that dropped it would.
type unmarkedRefunds struct{ *refundsVendor }

func (u *unmarkedRefunds) CallTool(ctx context.Context, url, bearer, tool string, args json.RawMessage) (json.RawMessage, error) {
	if tool != "list_refunds" {
		var a map[string]any
		_ = json.Unmarshal(args, &a)
		delete(a, "idempotency_key")
		args, _ = json.Marshal(a)
	}
	return u.refundsVendor.CallTool(ctx, url, bearer, tool, args)
}
