package connection_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

// A first start signs Coach's tools batch and its draft together, so the
// draft's Cards are worded before the tools are connected. The gateway checks
// each connection as the batch would (its server probed), builds the contract
// the batch would apply, and has KIFF word the draft against it: the same
// contract the batch then applies. A connection the batch would refuse gets no
// words at all, and nothing is stored, connected or applied.
func TestABatchPreviewWordsTheDraftOverTheContractTheBatchApplies(t *testing.T) {
	r := newRig(t, dev)
	open := openToolServer(t)
	tool := func(name string, readOnly bool) map[string]any {
		return map[string]any{"name": name, "title": "Coach " + name, "url": open.URL, "tool": "refund", "mode": "verify", "read_only": readOnly,
			"audience": "https://tafs.example/mcp", "idempotency_argument": "idem", "control": true}
	}
	conns := []any{tool("taf_task", false), tool("taf_read", true)}
	draft := map[string]any{"agent_id": "setup", "label": "Coach", "cards": []any{map[string]any{"id": "coach", "subject": "setup", "domain": "gateway"}}}
	const path = "/v1/connections/batch/preview"

	status, body := r.do(t, "owner-a", http.MethodPost, path, map[string]any{"connections": conns, "draft": draft})
	if status != http.StatusOK || !strings.Contains(body, `"contract_digest":"d1"`) {
		t.Fatalf("preview: %d %s", status, body)
	}
	r.api.mu.Lock()
	previews := append([]fakePreview(nil), r.api.previews...)
	applied := len(r.api.applied)
	r.api.mu.Unlock()
	if len(previews) != 1 {
		t.Fatalf("KIFF was asked %d times, want once", len(previews))
	}
	p := previews[0]
	if p.key != "owner-a" || p.draft["agent_id"] != "setup" || p.titles["taf_task"] != "Coach taf_task" ||
		!strings.Contains(p.contract, "name: taf_task") || !strings.Contains(p.contract, "name: taf_read") {
		t.Fatalf("asked KIFF with %+v", p)
	}
	// Nothing stored, connected or applied.
	if got, _ := r.store.List(context.Background(), "tenant-a"); len(got) != 0 || applied != 0 {
		t.Fatalf("a preview left %d tools and %d contracts", len(got), applied)
	}

	// The batch itself then applies exactly the contract the draft was worded over.
	if status, body := r.do(t, "owner-a", http.MethodPost, "/v1/connections/batch", map[string]any{"connections": conns}); status != http.StatusCreated {
		t.Fatalf("batch: %d %s", status, body)
	}
	r.api.mu.Lock()
	got := r.api.applied["owner-a"]
	r.api.mu.Unlock()
	if got != p.contract {
		t.Fatalf("the batch applied another contract than the one worded:\napplied:\n%s\nworded:\n%s", got, p.contract)
	}

	// A connection the batch would refuse: refused in its words, and nothing is worded.
	for name, c := range map[string][]any{
		"a vendor's tool":         {tool("taf_x", false), map[string]any{"name": "issue_refund", "url": r.tool.URL, "tool": "refund", "credential": toolSecret}},
		"a tool its server lacks": {map[string]any{"name": "taf_y", "url": open.URL, "tool": "no_such_tool", "mode": "verify", "audience": "https://tafs.example/mcp", "idempotency_argument": "idem", "control": true}},
		"a name twice":            {tool("taf_z", false), tool("taf_z", false)},
		"none":                    {},
	} {
		if status, body := r.do(t, "owner-a", http.MethodPost, path, map[string]any{"connections": c, "draft": draft}); status < 400 || status == http.StatusServiceUnavailable {
			t.Errorf("%s: %d %s", name, status, body)
		}
	}
	if status, body := r.do(t, "owner-a", http.MethodPost, path, map[string]any{"connections": conns, "draft": []any{}}); status != http.StatusBadRequest {
		t.Errorf("a draft that isn't one: %d %s", status, body)
	}
	r.api.mu.Lock()
	n := len(r.api.previews)
	r.api.previewDown = true
	r.api.mu.Unlock()
	if n != 1 {
		t.Fatalf("refused previews reached KIFF: %d asks", n)
	}
	// KIFF unreachable: said so, never words of the gateway's own.
	if status, body := r.do(t, "owner-a", http.MethodPost, path, map[string]any{"connections": conns, "draft": draft}); status != http.StatusServiceUnavailable {
		t.Errorf("KIFF down: %d %s", status, body)
	}
}
