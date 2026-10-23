package gateway_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/gateway/internal/gateway"
	"github.com/kiff/cloud/gateway/internal/kiffapi"
)

// chatCall is a 2026-07-28 tools/call from a client named name at
// version, declaring URL elicitation when url is set, carrying the
// client's answers to earlier input requests when responses is set.
func (r *rig) chatCall(t *testing.T, key, name, version string, url bool, args map[string]any, responses map[string]string) map[string]any {
	t.Helper()
	caps := map[string]any{"elicitation": map[string]any{"form": map[string]any{}}}
	if url {
		caps["elicitation"].(map[string]any)["url"] = map[string]any{}
	}
	params := map[string]any{"name": "issue_refund", "arguments": args, "_meta": map[string]any{
		"io.modelcontextprotocol/protocolVersion":    "2026-07-28",
		"io.modelcontextprotocol/clientInfo":         map[string]any{"name": name, "version": version},
		"io.modelcontextprotocol/clientCapabilities": caps,
	}}
	if responses != nil {
		in := map[string]any{}
		for id, action := range responses {
			in[id] = map[string]any{"action": action}
		}
		params["inputResponses"] = in
	}
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": params})
	req, _ := http.NewRequest(http.MethodPost, r.srv.URL, bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("MCP-Protocol-Version", "2026-07-28")
	req.Header.Set("Mcp-Method", "tools/call")
	req.Header.Set("Mcp-Name", "issue_refund")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	res, _ := out["result"].(map[string]any)
	if res == nil {
		t.Fatalf("tools/call: %v", out)
	}
	return res
}

// prompted returns the elicited URL and its id, or "" when the result
// is not a URL elicitation.
func prompted(res map[string]any) (url, id string) {
	if res["resultType"] != "input_required" {
		return "", ""
	}
	for k, v := range res["inputRequests"].(map[string]any) {
		p := v.(map[string]any)["params"].(map[string]any)
		if p["mode"] == "url" {
			return p["url"].(string), k
		}
	}
	return "", ""
}

func isHeld(res map[string]any) bool {
	meta, _ := res["_meta"].(map[string]any)
	call, _ := meta["dev.kiff/call"].(map[string]any)
	return res["resultType"] == "complete" && call["state"] == "held" && call["sent"] == "no"
}

// A verified client is asked once to open the hold's
// Needs you page; its answer, even "accept", approves nothing; the
// owner's answer on Needs you does.
func TestChatApprovalOffersNeedsYouOnce(t *testing.T) {
	r := newRig(t)
	res := r.chatCall(t, "chat-key", "claude-code", "2.1.287", true, amount(5000, "op-chat"), nil)
	link, id := prompted(res)
	if !strings.HasPrefix(link, "https://app.kiff.dev/needs-you/exc-") || !strings.HasPrefix(id, "kiff-exc-") {
		t.Fatalf("not offered: %v", res)
	}
	for _, action := range []string{"accept", "decline", "cancel"} {
		res = r.chatCall(t, "chat-key", "claude-code", "2.1.287", true, amount(5000, "op-chat"), map[string]string{id: action})
		if !isHeld(res) {
			t.Fatalf("%s: %v", action, res)
		}
	}
	// A later retry of the same hold is not offered again.
	*r.now = r.now.Add(5 * time.Minute)
	if res = r.chatCall(t, "chat-key", "claude-code", "2.1.287", true, amount(5000, "op-chat"), nil); !isHeld(res) {
		t.Fatalf("offered twice: %v", res)
	}
	if r.tool.count() != 0 {
		t.Fatalf("the client's answer reached the tool: %d", r.tool.count())
	}
	r.api.approveAll()
	if res = r.chatCall(t, "chat-key", "claude-code", "2.1.287", true, amount(5000, "op-chat"), nil); res["resultType"] != "complete" || r.tool.count() != 1 {
		t.Fatalf("approved on Needs you: %v (tool %d)", res, r.tool.count())
	}
}

// Every other client, and an account with chat approvals off, gets the
// held result exactly as before.
func TestChatApprovalOnlyForVerifiedClients(t *testing.T) {
	r := newRig(t)
	for i, c := range []struct {
		key, name, version string
		url                bool
	}{
		{"chat-key", "claude-code", "2.1.286", true},  // older than verified
		{"chat-key", "claude-code", "2.1.287", false}, // no URL mode
		{"chat-key", "some-client", "9.9.9", true},    // not verified
		{"agent-key", "claude-code", "2.1.287", true}, // account has it off
	} {
		*r.now = r.now.Add(2 * time.Minute)
		res := r.chatCall(t, c.key, c.name, c.version, c.url, amount(5000, "op-other-"+string(rune('a'+i))), nil)
		if !isHeld(res) {
			t.Errorf("%+v: %v", c, res)
		}
	}
}

// Only a hold the owner can still answer is offered: never a refusal,
// an expired hold, or an answered one. Nobody answering the prompt does
// not extend the hold.
func TestChatApprovalOnlyForAnswerableHolds(t *testing.T) {
	r := newRig(t)
	r.api.refuse[4000] = kiffapi.Decision{Outcome: "blocked", Reasons: []string{"no_mandate"}}
	if res := r.chatCall(t, "chat-key", "claude-code", "2.1.287", true, amount(4000, "op-refused"), nil); res["resultType"] != "complete" {
		t.Errorf("refusal offered: %v", res)
	}

	past := r.now.Add(-time.Minute)
	r.api.holdExpiry = &past
	*r.now = r.now.Add(2 * time.Minute)
	if res := r.chatCall(t, "chat-key", "claude-code", "2.1.287", true, amount(5000, "op-expired"), nil); !isHeld(res) {
		t.Errorf("expired hold offered: %v", res)
	}

	expires := r.now.Add(10 * time.Minute)
	r.api.holdExpiry = &expires
	*r.now = r.now.Add(2 * time.Minute)
	if link, _ := prompted(r.chatCall(t, "chat-key-2", "claude-code", "2.1.287", true, amount(6000, "op-live"), nil)); link == "" {
		t.Fatal("a live hold was not offered")
	}
	r.api.refuse[6000] = kiffapi.Decision{Outcome: "blocked", Reasons: []string{"mandate_owner_expired"}}
	*r.now = expires.Add(time.Minute)
	if res := r.chatCall(t, "chat-key-2", "claude-code", "2.1.287", true, amount(6000, "op-live"), nil); res["resultType"] != "complete" || r.tool.count() != 0 {
		t.Errorf("unanswered prompt extended the hold: %v", res)
	}
}

// One prompt per agent per ChatPromptGap: the next hold in a batch is
// held without one, and offered again once the gap has passed.
func TestChatApprovalOnePromptPerAgentPerMinute(t *testing.T) {
	r := newRig(t)
	if link, _ := prompted(r.chatCall(t, "chat-key", "claude-code", "2.1.287", true, amount(5000, "op-b1"), nil)); link == "" {
		t.Fatal("first hold not offered")
	}
	*r.now = r.now.Add(20 * time.Second)
	if res := r.chatCall(t, "chat-key", "claude-code", "2.1.287", true, amount(5001, "op-b2"), nil); !isHeld(res) {
		t.Fatalf("second hold in the same minute was offered: %v", res)
	}
	if link, _ := prompted(r.chatCall(t, "chat-key-2", "claude-code", "2.1.287", true, amount(5002, "op-b3"), nil)); link == "" {
		t.Fatal("another agent's hold was not offered")
	}
	*r.now = r.now.Add(time.Minute)
	if link, _ := prompted(r.chatCall(t, "chat-key", "claude-code", "2.1.287", true, amount(5003, "op-b4"), nil)); link == "" {
		t.Fatal("a hold after the gap was not offered")
	}
}

// Each agent's MCP client is recorded: name, version, whether it
// declares URL mode, and when it was last seen. Nothing else.
func TestAgentClientsAreRecorded(t *testing.T) {
	r := newRig(t)
	r.chatCall(t, "chat-key", "claude-code", "2.1.287", true, amount(500, "op-rec"), nil)
	r.chatCall(t, "chat-key", "claude-code", "2.1.287", true, amount(501, "op-rec-2"), nil)
	var n int
	var version string
	var url bool
	if err := r.pool.QueryRow(context.Background(), `SELECT count(*), max(client_version), bool_and(url_elicitation)
		FROM gateway.agent_clients WHERE tenant_id = 't1' AND agent_id = 'chat-agent-chat-key' AND client_name = 'claude-code'`).Scan(&n, &version, &url); err != nil {
		t.Fatal(err)
	}
	if n != 1 || version != "2.1.287" || !url {
		t.Fatalf("recorded: %d %q %v", n, version, url)
	}
	r.call(t, "agent-key", amount(500, "op-legacy")) // legacy: no client info per request
	if err := r.pool.QueryRow(context.Background(), `SELECT count(*) FROM gateway.agent_clients`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("rows: %d %v", n, err)
	}
}

// Only this app's own Needs you page is ever offered: a hold whose link
// points anywhere else is held without a prompt.
func TestChatApprovalOnlyOffersOwnNeedsYou(t *testing.T) {
	r := newRig(t)
	r.api.reviewBase = "https://evil.example"
	if res := r.chatCall(t, "chat-key", "claude-code", "2.1.287", true, amount(5000, "op-foreign"), nil); !isHeld(res) {
		t.Fatalf("a foreign link was offered: %v", res)
	}
}

// One agent cannot add more than maxClientsPerAgent client names to
// gateway.agent_clients, however it varies them.
func TestAgentClientsAreCappedPerAgent(t *testing.T) {
	r := newRig(t)
	p := gateway.PostgresClients{Pool: r.pool}
	for i := 0; i < 15; i++ {
		if err := p.SeeClient(context.Background(), gateway.ClientSeen{TenantID: "t1", AgentID: "a", Name: fmt.Sprint("client-", i), Version: "1", At: *r.now}); err != nil {
			t.Fatal(err)
		}
	}
	if err := p.SeeClient(context.Background(), gateway.ClientSeen{TenantID: "t1", AgentID: "a", Name: "client-0", Version: "2", At: *r.now}); err != nil {
		t.Fatal(err)
	}
	var n int
	var v string
	_ = r.pool.QueryRow(context.Background(), `SELECT count(*) FROM gateway.agent_clients WHERE agent_id = 'a'`).Scan(&n)
	_ = r.pool.QueryRow(context.Background(), `SELECT client_version FROM gateway.agent_clients WHERE agent_id = 'a' AND client_name = 'client-0'`).Scan(&v)
	if n != 10 || v != "2" {
		t.Fatalf("rows %d, client-0 version %q", n, v)
	}
}
