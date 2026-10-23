package gateway_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/gateway/internal/gateway"
	"github.com/kiff/cloud/gateway/internal/kiffapi"
)

// want checks the fields of a result's call state.
func want(t *testing.T, what string, got result, fields map[string]any) {
	t.Helper()
	if got.call == nil {
		t.Fatalf("%s: no %s in _meta: %q", what, gateway.MetaKey, got.text)
	}
	for k, v := range fields {
		g, _ := json.Marshal(got.call[k])
		w, _ := json.Marshal(v)
		if string(g) != string(w) {
			t.Errorf("%s: %s = %s, want %s (call %v)", what, k, g, w, got.call)
		}
	}
}

// A held call says where the owner answers and that the same call is
// the retry, in the text and in the call state.
func TestHeldResultCarriesCallState(t *testing.T) {
	r := newRig(t)
	got := r.call(t, "agent-key", amount(5000, "op-held"))
	if !got.isError || !strings.Contains(got.text, "Waiting for the owner's approval") || !strings.Contains(got.text, "30 seconds") {
		t.Fatalf("held: %+v", got)
	}
	want(t, "held", got, map[string]any{"state": "held", "sent": "no", "retry": "same_call", "retry_after_s": 30,
		"operation_id": "op-held", "review_url": got.call["review_url"]})
	if inv, _ := got.call["invocation"].(string); !strings.HasPrefix(inv, "tc-") {
		t.Fatalf("invocation: %v", got.call)
	}
	if url, _ := got.call["review_url"].(string); !strings.Contains(got.text, url) || !strings.HasPrefix(url, "https://app.kiff.dev/needs-you/") {
		t.Fatalf("review_url %q not the link in the text: %q", url, got.text)
	}
}

// A refusal gives the next step for its reason, keeps the codes and
// apps/api's message, and replays the same reasons on a retry.
func TestRefusalHasNextStepAndReplaysReasons(t *testing.T) {
	r := newRig(t)
	r.api.refuse[7] = kiffapi.Decision{Outcome: "blocked", Reasons: []string{"mandate_per_action_exceeded"}, Message: "over the per-action limit"}
	first := r.call(t, "agent-key", amount(7, "op-over"))
	for _, s := range []string{"Refused by KIFF (blocked: mandate_per_action_exceeded)", "Nothing was sent to the tool.",
		"Next: This call is above the Card's per-call limit", "KIFF said: over the per-action limit"} {
		if !strings.Contains(first.text, s) {
			t.Errorf("text lacks %q: %q", s, first.text)
		}
	}
	fields := map[string]any{"state": "refused", "sent": "no", "retry": "new_call", "reasons": []string{"mandate_per_action_exceeded"}}
	want(t, "refusal", first, fields)
	again := r.call(t, "agent-key", amount(7, "op-over"))
	if again.text != first.text {
		t.Fatalf("replayed text differs: %q", again.text)
	}
	want(t, "replayed refusal", again, fields)
	if r.tool.count() != 0 {
		t.Fatalf("tool calls = %d", r.tool.count())
	}
}

// The owner's rejection says not to retry, a withdrawn call may be made
// again; a reason the gateway does not
// know keeps the text as it was and is still a new_call.
func TestRefusalRetryHints(t *testing.T) {
	r := newRig(t)
	r.api.refuse[8] = kiffapi.Decision{Outcome: "blocked", Reasons: []string{"mandate_owner_rejected"}}
	r.api.refuse[9] = kiffapi.Decision{Outcome: "invalid", Reasons: []string{"something_new"}}
	rejected := r.call(t, "agent-key", amount(8, "op-rej"))
	if !strings.Contains(rejected.text, "Do not retry it.") {
		t.Fatalf("rejected: %q", rejected.text)
	}
	want(t, "rejected", rejected, map[string]any{"retry": "none", "reasons": []string{"mandate_owner_rejected"}})
	// A withdrawn call is the agent's own run stopping, not an
	// answer, so it may be made again under a new id.
	r.api.refuse[10] = kiffapi.Decision{Outcome: "blocked", Reasons: []string{"mandate_withdrawn"}}
	withdrawn := r.call(t, "agent-key", amount(10, "op-wd"))
	if strings.Contains(withdrawn.text, "Do not retry") || !strings.Contains(withdrawn.text, "new "+gateway.OperationArg) {
		t.Fatalf("withdrawn: %q", withdrawn.text)
	}
	want(t, "withdrawn", withdrawn, map[string]any{"retry": "new_call", "reasons": []string{"mandate_withdrawn"}})
	unknown := r.call(t, "agent-key", amount(9, "op-new"))
	if strings.Contains(unknown.text, "Next:") || !strings.Contains(unknown.text, "invalid: something_new") {
		t.Fatalf("unknown reason: %q", unknown.text)
	}
	want(t, "unknown reason", unknown, map[string]any{"state": "refused", "retry": "new_call", "reasons": []string{"something_new"}})
}

func TestOperationIDReusedCallState(t *testing.T) {
	r := newRig(t)
	r.call(t, "agent-key", amount(500, "op-1"))
	got := r.call(t, "agent-key", amount(600, "op-1"))
	if !strings.Contains(got.text, "already used") {
		t.Fatalf("reused: %q", got.text)
	}
	want(t, "reused", got, map[string]any{"state": "refused", "sent": "no", "retry": "new_call",
		"operation_id": "op-1", "reasons": []string{"operation_id_reused"}})
}

// A lost response is never re-sent: sent unknown, no retry.
func TestUnknownResultCallState(t *testing.T) {
	r := newRig(t)
	r.tool.hang = true
	got := r.call(t, "agent-key", amount(500, "op-hang"))
	want(t, "unknown", got, map[string]any{"state": "unknown", "sent": "unknown", "retry": "none",
		"reasons": []string{"response_lost"}})
}

// The tool's own result is passed through untouched, with the call state
// naming KIFF's key for it (kiff/tafs#167); a repeat without an operation
// id is marked in the call state as well as the text.
func TestForwardedResultUntouchedRepeatMarked(t *testing.T) {
	r := newRig(t)
	first := r.call(t, "agent-key", amount(500, ""))
	if first.isError || first.text != "refund 1 issued by refund" {
		t.Fatalf("forwarded result changed: %+v", first)
	}
	want(t, "forwarded", first, map[string]any{"state": "forwarded", "sent": "yes", "retry": "none"})
	if inv, _ := first.call["invocation"].(string); !strings.HasPrefix(inv, "tc-") {
		t.Fatalf("invocation: %v", first.call)
	}
	*r.now = t0.Add(time.Minute)
	again := r.call(t, "agent-key", amount(500, ""))
	if !strings.Contains(again.text, "treated this as a repeat") {
		t.Fatalf("repeat: %q", again.text)
	}
	want(t, "repeat", again, map[string]any{"state": "forwarded", "sent": "yes", "repeat": true, "retry": "none"})
	if r.tool.count() != 1 {
		t.Fatalf("tool calls = %d", r.tool.count())
	}
}

// The 2026-07-28 path carries the call state too, next to resultType.
func TestModernResultCarriesCallState(t *testing.T) {
	r := newRig(t)
	params := map[string]any{"name": "issue_refund", "arguments": amount(5000, "op-modern"),
		"_meta": map[string]any{"io.modelcontextprotocol/protocolVersion": "2026-07-28"}}
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": params})
	req, _ := http.NewRequest(http.MethodPost, r.srv.URL, bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer agent-key")
	req.Header.Set("MCP-Protocol-Version", "2026-07-28")
	req.Header.Set("Mcp-Method", "tools/call")
	req.Header.Set("Mcp-Name", "issue_refund")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out struct {
		Result struct {
			ResultType string                    `json:"resultType"`
			Meta       map[string]map[string]any `json:"_meta"`
		} `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.Result.ResultType != "complete" || out.Result.Meta[gateway.MetaKey]["state"] != "held" {
		t.Fatalf("modern held result: %+v", out.Result)
	}
}

// A held call tells the agent when it stops waiting, and once
// apps/api reports the hold expired, the call is refused with a next step
// and is never sent.
func TestHeldCallExpires(t *testing.T) {
	r := newRig(t)
	expires := t0.Add(10 * time.Minute)
	r.api.holdExpiry = &expires
	held := r.call(t, "agent-key", amount(5000, "op-exp"))
	if !strings.Contains(held.text, "If the owner has not answered by 2026-10-01T12:10:00Z") {
		t.Fatalf("held text: %q", held.text)
	}
	want(t, "held", held, map[string]any{"state": "held", "hold_expires_at": "2026-10-01T12:10:00Z"})

	r.api.refuse[5000] = kiffapi.Decision{Outcome: "blocked", Reasons: []string{"mandate_owner_expired"}}
	*r.now = t0.Add(11 * time.Minute)
	got := r.call(t, "agent-key", amount(5000, "op-exp"))
	if !strings.Contains(got.text, "Next: The account owner did not answer before the hold expired") {
		t.Fatalf("expired text: %q", got.text)
	}
	want(t, "expired", got, map[string]any{"state": "refused", "sent": "no", "retry": "new_call",
		"reasons": []string{"mandate_owner_expired"}})
	if r.tool.count() != 0 {
		t.Fatalf("an expired hold reached the tool: %d", r.tool.count())
	}
}

// After the result's retention, a retry is told the call was sent and is
// still not sent again.
func TestRetryAfterResultPurged(t *testing.T) {
	r := newRig(t)
	if first := r.call(t, "agent-key", amount(500, "op-purge")); first.isError {
		t.Fatalf("first: %+v", first)
	}
	*r.now = t0.Add(gateway.DefaultResultRetention - time.Minute)
	if n, err := r.gw.SweepResults(context.Background()); n != 0 || err != nil {
		t.Fatalf("purged inside retention: %d %v", n, err)
	}
	*r.now = t0.Add(gateway.DefaultResultRetention + time.Minute)
	if n, err := r.gw.SweepResults(context.Background()); n != 1 || err != nil {
		t.Fatalf("sweep: %d %v", n, err)
	}
	again := r.call(t, "agent-key", amount(500, "op-purge"))
	if !strings.Contains(again.text, "no longer keeps the tool's result") {
		t.Fatalf("retry: %q", again.text)
	}
	want(t, "purged", again, map[string]any{"state": "forwarded", "sent": "yes", "retry": "none"})
	if r.tool.count() != 1 {
		t.Fatalf("tool calls = %d", r.tool.count())
	}
}
