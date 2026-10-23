package gateway_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/kiff/cloud/gateway/internal/gateway"
)

// trialCall is r.call for a trial: the request carries KIFF-Trial.
func (r *rig) trialCall(t *testing.T, key, draft, tool string, args map[string]any) result {
	t.Helper()
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": tool, "arguments": args}})
	req, _ := http.NewRequest(http.MethodPost, r.srv.URL, bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set(gateway.TrialHeader, draft)
	if r.trialRun != "" {
		req.Header.Set(gateway.TrialRunHeader, r.trialRun)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	res, ok := out["result"].(map[string]any)
	if resp.StatusCode != http.StatusOK || !ok {
		t.Fatalf("tools/call: %d %v", resp.StatusCode, out)
	}
	var texts []string
	for _, c := range res["content"].([]any) {
		texts = append(texts, c.(map[string]any)["text"].(string))
	}
	isErr, _ := res["isError"].(bool)
	meta, _ := res["_meta"].(map[string]any)
	call, _ := meta[gateway.MetaKey].(map[string]any)
	return result{text: strings.Join(texts, " | "), isError: isErr, call: call}
}

func (r *rig) invocations(t *testing.T) int {
	t.Helper()
	var n int
	if err := r.pool.QueryRow(context.Background(), `SELECT count(*) FROM gateway.invocations`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func trialOf(t *testing.T, got result) map[string]any {
	t.Helper()
	tr, _ := got.call["trial"].(map[string]any)
	if tr == nil || got.call["sent"] != "no" {
		t.Fatalf("no trial decision in %+v", got)
	}
	return tr
}

// Invariant: a trial's call is never sent, reads included. A call made with
// KIFF-Trial is decided against the agent's Cards and answered with that
// decision; it creates no invocation, records no request, draws nothing and
// never reaches the tool.
// Category: authority
// Attack: run a trial of a new version that issues refunds inside and outside its Card through the gateway
// Refused: each refund comes back with KIFF's decision (allowed, held) and "Nothing was sent"; the tool receives no call and nothing is recorded or drawn
func TestTrialWriteIsDecidedAndNeverSent(t *testing.T) {
	r := newRig(t)
	r.trialRun = "run-1"
	small := r.trialCall(t, "agent-key", "live", "issue_refund", amount(500, "op-trial-1"))
	if small.isError || !strings.Contains(small.text, "would allow") || !strings.Contains(small.text, "Nothing was sent") {
		t.Fatalf("€500 in a trial: %+v", small)
	}
	if tr := trialOf(t, small); tr["decision"] != "allowed" {
		t.Fatalf("€500 trial decision: %v", tr)
	}
	big := r.trialCall(t, "agent-key", "live", "issue_refund", amount(5000, ""))
	if tr := trialOf(t, big); tr["decision"] != "held" || !strings.Contains(big.text, "owner's approval") || big.isError {
		t.Fatalf("€5,000 in a trial: %+v", big)
	}
	if r.tool.count() != 0 {
		t.Fatalf("a trial reached the tool %d times", r.tool.count())
	}
	if n, draws := r.api.totalDrawn(); n != 0 || draws != 0 || len(r.api.ingested) != 0 {
		t.Fatalf("a trial was recorded or drawn: drawn %d (%d), requested %v", n, draws, r.api.ingested)
	}
	if n := r.invocations(t); n != 0 {
		t.Fatalf("a trial created %d invocations", n)
	}
	// The trial names its draft, the tool and the Card's parameters,
	// never the agent's operation id.
	if len(r.api.trials) != 2 || r.api.trials[0].Draft != "live" || r.api.trials[0].Run != "run-1" || r.api.trials[0].ActionName != "issue_refund" ||
		r.api.trials[0].Parameters["amount_eur"] != int64(500) {
		t.Fatalf("trials asked: %+v", r.api.trials)
	}
}

// When KIFF can't be asked, a trial's write counts as refused, and is
// still not sent.
func TestTrialWriteKIFFDownSendsNothing(t *testing.T) {
	r := newRig(t)
	r.trialRun = "run-1"
	r.api.mu.Lock()
	r.api.trialFails = true
	r.api.mu.Unlock()
	got := r.trialCall(t, "agent-key", "live", "issue_refund", amount(500, ""))
	if tr := trialOf(t, got); tr["decision"] != "refused" || !strings.Contains(got.text, "couldn't decide") {
		t.Fatalf("trial with KIFF down: %+v", got)
	}
	if r.tool.count() != 0 || r.invocations(t) != 0 {
		t.Fatalf("sent %d, invocations %d", r.tool.count(), r.invocations(t))
	}
}

// A trial's read isn't sent either (kiff/agents#139): its read-only flag can
// be a model's claim, and a "read" that writes must not run in a trial.
func TestTrialReadIsNotSentEither(t *testing.T) {
	r := newRig(t)
	r.trialRun = "run-1"
	read := refundTool
	read.Name, read.UpstreamTool, read.ReadOnly = "list_orders", "orders", true
	r.gw.Catalog = catalog{refundTool, read}
	got := r.trialCall(t, "agent-key", "live", "list_orders", amount(1, ""))
	if tr := trialOf(t, got); tr["decision"] != "allowed" || !strings.Contains(got.text, "reads included") {
		t.Fatalf("a read in a trial: %+v", got)
	}
	if r.tool.count() != 0 || r.invocations(t) != 0 || len(r.api.trials) != 1 {
		t.Fatalf("a trial's read was sent: tool calls %d, invocations %d, trials %d", r.tool.count(), r.invocations(t), len(r.api.trials))
	}
}

// The gateway says it decides trials, so a client sends one only here.
func TestTrialCapabilityIsDeclared(t *testing.T) {
	r := newRig(t)
	_, out := r.rpc(t, "agent-key", "initialize", map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{}})
	res, _ := out["result"].(map[string]any)
	caps, _ := res["capabilities"].(map[string]any)
	exp, _ := caps["experimental"].(map[string]any)
	if exp[gateway.TrialCapability] != true || exp[gateway.TrialSendsCapability] != true {
		t.Fatalf("capabilities = %v", caps)
	}
}

// A trial write that names no run is refused without asking KIFF: its
// earlier writes couldn't be counted. It is not sent either.
func TestTrialWriteWithoutItsRunIsRefused(t *testing.T) {
	r := newRig(t)
	got := r.trialCall(t, "agent-key", "live", "issue_refund", amount(500, ""))
	if tr := trialOf(t, got); tr["decision"] != "refused" || !strings.Contains(got.text, gateway.TrialRunHeader) {
		t.Fatalf("trial without its run: %+v", got)
	}
	if r.tool.count() != 0 || r.invocations(t) != 0 || len(r.api.trials) != 0 {
		t.Fatalf("sent %d, invocations %d, asked %d", r.tool.count(), r.invocations(t), len(r.api.trials))
	}
}

// In a trial, the tools are described with the Cards its calls are decided
// on (kiff/tafs#107): the gateway reads the agent's Card for the trial's
// draft, so a tool the draft adds isn't listed as uncovered. Outside a trial,
// the Cards in force.
func TestTrialListsToolsWithItsDraftsCard(t *testing.T) {
	r := newRig(t)
	list := func(trial string) {
		t.Helper()
		b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list", "params": map[string]any{}})
		req, _ := http.NewRequest(http.MethodPost, r.srv.URL, bytes.NewReader(b))
		req.Header.Set("Authorization", "Bearer agent-key")
		if trial != "" {
			req.Header.Set(gateway.TrialHeader, trial)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("tools/list: %d", resp.StatusCode)
		}
	}
	list("drf_v2")
	list("")
	r.api.mu.Lock()
	got := append([]string(nil), r.api.cardTrials...)
	r.api.mu.Unlock()
	if len(got) != 2 || got[0] != "drf_v2" || got[1] != "" {
		t.Fatalf("the Card was read for trials %q, want [drf_v2, \"\"]", got)
	}
}

// Invariant: a trial its owner started sends its calls (kiff/tafs#173).
// With KIFF-Trial naming a draft KIFF says is being tried, a call goes the
// normal path: recorded, decided with the draft named (so KIFF decides it
// on the trial's own Cards), sent when allowed, held when not, and its
// result names the draft. KIFF-Trial-Run isn't needed, and is harmless.
// Category: authority
// Attack: a version being tried issues one refund inside its trial Card and one outside it
// Refused: the one outside is held for the owner and not sent; both are decided with the trial's draft, never as the live version's calls
func TestTrialBeingTriedIsSentOnItsDraft(t *testing.T) {
	r := newRig(t)
	r.api.mu.Lock()
	r.api.trying["drf_try"] = true
	r.api.mu.Unlock()
	small := r.trialCall(t, "agent-key", "drf_try", "issue_refund", amount(500, "op-try-1"))
	if small.isError || r.tool.count() != 1 || small.call["trial_draft"] != "drf_try" || small.call["trial"] != nil {
		t.Fatalf("€500 while trying: %+v, tool calls %d", small, r.tool.count())
	}
	r.trialRun = "run-1" // harmless on a trial being tried
	big := r.trialCall(t, "agent-key", "drf_try", "issue_refund", amount(5000, "op-try-2"))
	if big.call["state"] != "held" || big.call["sent"] != "no" || big.call["trial_draft"] != "drf_try" || r.tool.count() != 1 {
		t.Fatalf("€5,000 while trying: %+v, tool calls %d", big, r.tool.count())
	}
	r.api.mu.Lock()
	defer r.api.mu.Unlock()
	if len(r.api.trials) != 0 {
		t.Fatalf("a trial being tried was only decided: %+v", r.api.trials)
	}
	if len(r.api.trialDrafts) != 2 {
		t.Fatalf("decided %d calls, want 2", len(r.api.trialDrafts))
	}
	for op, d := range r.api.trialDrafts {
		if d != "drf_try" {
			t.Fatalf("call %s decided with trial_draft %q", op, d)
		}
	}
}

// A draft that is only waiting is a trial that sends nothing, as before:
// Setup's preview of a version (kiff/tafs#107).
func TestTrialOfAWaitingDraftIsOnlyDecided(t *testing.T) {
	r := newRig(t)
	r.trialRun = "run-1"
	got := r.trialCall(t, "agent-key", "drf_waiting", "issue_refund", amount(500, ""))
	if tr := trialOf(t, got); tr["decision"] != "allowed" {
		t.Fatalf("a waiting draft's trial: %+v", got)
	}
	if r.tool.count() != 0 || r.invocations(t) != 0 || got.call["trial_draft"] != nil {
		t.Fatalf("a waiting draft's trial was sent: tool calls %d, %+v", r.tool.count(), got)
	}
}

// When KIFF can't say whether the draft is being tried, the call is neither
// sent nor answered as a decision it isn't.
func TestTrialOpenUnknownSendsNothing(t *testing.T) {
	r := newRig(t)
	r.api.mu.Lock()
	r.api.trialOpenFails = true
	r.api.mu.Unlock()
	got := r.trialCall(t, "agent-key", "drf_try", "issue_refund", amount(500, ""))
	if got.call["sent"] != "no" || got.call["retry"] != "same_call" || got.call["trial"] != nil || !got.isError {
		t.Fatalf("trial with KIFF down: %+v", got)
	}
	if r.tool.count() != 0 || r.invocations(t) != 0 || len(r.api.trials) != 0 {
		t.Fatalf("sent %d, invocations %d, trials %d", r.tool.count(), r.invocations(t), len(r.api.trials))
	}
}
