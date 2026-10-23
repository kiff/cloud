package gateway_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/kiff/cloud/gateway/internal/kiffapi"
)

// modern sends a 2026-07-28 request from a client named claude-code that
// declares URL elicitation, and returns the result or the error.
func (r *rig) modern(t *testing.T, key, method string, params map[string]any) (map[string]any, map[string]any) {
	t.Helper()
	params["_meta"] = map[string]any{
		"io.modelcontextprotocol/protocolVersion":    "2026-07-28",
		"io.modelcontextprotocol/clientInfo":         map[string]any{"name": "claude-code", "version": "2.1.287"},
		"io.modelcontextprotocol/clientCapabilities": map[string]any{"elicitation": map[string]any{"url": map[string]any{}}},
	}
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	req, _ := http.NewRequest(http.MethodPost, r.srv.URL, bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("MCP-Protocol-Version", "2026-07-28")
	req.Header.Set("Mcp-Method", method)
	if n, ok := params["name"].(string); ok {
		req.Header.Set("Mcp-Name", n)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	res, _ := out["result"].(map[string]any)
	e, _ := out["error"].(map[string]any)
	return res, e
}

func (r *rig) taskCall(t *testing.T, key string, args map[string]any) map[string]any {
	t.Helper()
	res, e := r.modern(t, key, "tools/call", map[string]any{"name": "issue_refund", "arguments": args, "task": map[string]any{"ttl": 60000}})
	if e != nil {
		t.Fatalf("tools/call: %v", e)
	}
	task, _ := res["task"].(map[string]any)
	if task == nil {
		t.Fatalf("no task in %v", res)
	}
	return task
}

func (r *rig) taskGet(t *testing.T, key, id string) map[string]any {
	t.Helper()
	res, e := r.modern(t, key, "tasks/get", map[string]any{"taskId": id})
	if e != nil {
		t.Fatalf("tasks/get: %v", e)
	}
	return res
}

// A held call run as a task stays working while it waits, and
// completes with the forwarded result once the owner approves — the client
// never has to retry, and the tool receives the call once.
func TestHeldCallRunsAsTask(t *testing.T) {
	r := newRig(t)
	task := r.taskCall(t, "chat-key", amount(5000, "op-task"))
	id, _ := task["taskId"].(string)
	if task["status"] != "working" || id == "" || task["pollInterval"] == nil {
		t.Fatalf("held task: %v", task)
	}
	if got := r.taskGet(t, "chat-key", id); got["status"] != "working" {
		t.Fatalf("before approval: %v", got)
	}
	if _, e := r.modern(t, "chat-key", "tasks/result", map[string]any{"taskId": id}); e == nil {
		t.Fatal("tasks/result answered a working task")
	}
	if r.tool.count() != 0 {
		t.Fatalf("a held task reached the tool: %d", r.tool.count())
	}
	r.api.approveAll()
	if got := r.taskGet(t, "chat-key", id); got["status"] != "completed" {
		t.Fatalf("after approval: %v", got)
	}
	r.taskGet(t, "chat-key", id) // polling again changes nothing
	res, e := r.modern(t, "chat-key", "tasks/result", map[string]any{"taskId": id})
	if e != nil || res["resultType"] != "complete" || res["content"] == nil {
		t.Fatalf("tasks/result: %v %v", res, e)
	}
	if r.tool.count() != 1 {
		t.Fatalf("tool received %d calls, want 1", r.tool.count())
	}
}

// A call inside the Card is a task that is complete at once.
func TestAllowedCallIsACompletedTask(t *testing.T) {
	r := newRig(t)
	task := r.taskCall(t, "chat-key", amount(500, "op-task-ok"))
	if task["status"] != "completed" || r.tool.count() != 1 {
		t.Fatalf("task %v, tool %d", task, r.tool.count())
	}
	res, e := r.modern(t, "chat-key", "tasks/result", map[string]any{"taskId": task["taskId"]})
	if e != nil || res["content"] == nil {
		t.Fatalf("tasks/result: %v %v", res, e)
	}
}

// Only the key that made the call can read its task.
func TestTaskBelongsToItsKey(t *testing.T) {
	r := newRig(t)
	id := r.taskCall(t, "chat-key", amount(5000, "op-task-own"))["taskId"]
	for _, m := range []string{"tasks/get", "tasks/result"} {
		if _, e := r.modern(t, "chat-key-2", m, map[string]any{"taskId": id}); e == nil || e["message"] != "unknown task" {
			t.Errorf("%s by another key: %v", m, e)
		}
	}
	if _, e := r.modern(t, "chat-key", "tasks/get", map[string]any{"taskId": "tc-nope"}); e == nil {
		t.Error("an unknown task was answered")
	}
}

// An unanswered hold expires: the task completes, refused, and nothing is
// sent. A task never extends a hold.
func TestExpiredHoldCompletesTheTaskRefused(t *testing.T) {
	r := newRig(t)
	expires := r.now.Add(10 * time.Minute)
	r.api.holdExpiry = &expires
	id := r.taskCall(t, "chat-key", amount(5000, "op-task-exp"))["taskId"].(string)
	r.api.refuse[5000] = kiffapi.Decision{Outcome: "blocked", Reasons: []string{"mandate_owner_expired"}}
	*r.now = expires.Add(time.Minute)
	if got := r.taskGet(t, "chat-key", id); got["status"] != "completed" {
		t.Fatalf("expired: %v", got)
	}
	res, _ := r.modern(t, "chat-key", "tasks/result", map[string]any{"taskId": id})
	if res["isError"] != true || r.tool.count() != 0 {
		t.Fatalf("expired result %v, tool %d", res, r.tool.count())
	}
}

// A client that runs the call as a task waits on it; it is not also asked
// to open the Needs you link.
func TestTaskSupersedesChatPrompt(t *testing.T) {
	r := newRig(t)
	res, _ := r.modern(t, "chat-key", "tools/call", map[string]any{"name": "issue_refund", "arguments": amount(5000, "op-task-chat"), "task": map[string]any{}})
	if res["inputRequests"] != nil || res["task"] == nil {
		t.Fatalf("got %v", res)
	}
}

// The capability and each tool's task support are advertised.
func TestTasksAreAdvertised(t *testing.T) {
	r := newRig(t)
	res, _ := r.modern(t, "chat-key", "server/discover", map[string]any{})
	caps, _ := res["capabilities"].(map[string]any)
	if caps["tasks"] == nil {
		t.Errorf("no tasks capability: %v", caps)
	}
	res, _ = r.modern(t, "chat-key", "tools/list", map[string]any{})
	tools, _ := res["tools"].([]any)
	if len(tools) == 0 {
		t.Fatal("no tools")
	}
	ex, _ := tools[0].(map[string]any)["execution"].(map[string]any)
	if ex["taskSupport"] != "optional" {
		t.Errorf("execution: %v", ex)
	}
}
