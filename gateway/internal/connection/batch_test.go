package connection_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/kiff/cloud/gateway/internal/connection"
	"github.com/kiff/cloud/gateway/internal/kiffapi"
	"github.com/kiff/cloud/gateway/internal/mcp"
)

// countingLister counts the listings of remote servers' tools.
type countingLister struct {
	next connection.Lister
	n    int
}

func (c *countingLister) ListTools(ctx context.Context, url, bearer string) ([]mcp.RemoteTool, error) {
	c.n++
	return c.next.ListTools(ctx, url, bearer)
}

// The owner's phone connects Coach's tools on the account's control server in
// one signed request (batch connections). Each is checked as one alone; they're
// made in one change, so a contract KIFF refuses connects none; the same
// batch again leaves them as they are; and every one is a tool that requires
// a Card, so until the owner signs Coach's Cards no call on them is allowed.
func TestPhoneConnectsCoachsToolsInOneBatch(t *testing.T) {
	r := newRig(t, dev)
	open := openToolServer(t)
	tool := func(name string, readOnly bool) map[string]any {
		return map[string]any{"name": name, "url": open.URL, "tool": "refund", "mode": "verify", "read_only": readOnly,
			"audience": "https://tafs.example/mcp", "idempotency_argument": "idem", "control": true}
	}
	batch := map[string]any{"connections": []any{tool("taf_task", false), tool("taf_read", true)}}
	post := func(key, sig string, b any) (int, string) {
		t.Helper()
		payload, _ := json.Marshal(b)
		req, _ := http.NewRequest(http.MethodPost, r.srv.URL+"/v1/connections/batch", bytes.NewReader(payload))
		req.Header.Set("Authorization", "Bearer "+key)
		if sig != "" {
			req.Header.Set(kiffapi.DeviceSignatureHeader, sig)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(resp.Body)
		return resp.StatusCode, buf.String()
	}
	// Attack: unsigned, or carrying a vendor's tool alongside.
	if status, body := post("phone-a", "", batch); status != http.StatusForbidden {
		t.Fatalf("unsigned: %d %s", status, body)
	}
	vendor := map[string]any{"connections": []any{tool("taf_task", false), map[string]any{"name": "issue_refund", "url": r.tool.URL, "tool": "refund", "credential": toolSecret}}}
	if status, body := post("phone-a", "t=ok", vendor); status != http.StatusBadRequest {
		t.Fatalf("a vendor's tool in the batch: %d %s", status, body)
	}
	// KIFF refuses the contract: neither tool is connected.
	r.api.mu.Lock()
	r.api.fail = true
	r.api.mu.Unlock()
	if status, body := post("owner-a", "", batch); status == http.StatusCreated {
		t.Fatalf("connected although the contract failed: %s", body)
	}
	r.api.mu.Lock()
	r.api.fail = false
	r.api.mu.Unlock()
	if conns, _ := r.store.List(context.Background(), "tenant-a"); len(conns) != 0 {
		t.Fatalf("a failed batch left %d tools", len(conns))
	}
	// The owner's phone, signed once: both are connected, a person's, and
	// their server is listed once for the batch.
	lists := &countingLister{next: r.h.Remote}
	r.h.Remote = lists
	status, body := post("phone-a", "t=ok", batch)
	if lists.n != 1 {
		t.Fatalf("the server was listed %d times for one batch, want once", lists.n)
	}
	if status != http.StatusCreated || !strings.Contains(body, `"connected":["taf_task","taf_read"]`) {
		t.Fatalf("signed batch: %d %s", status, body)
	}
	for _, name := range []string{"taf_task", "taf_read"} {
		conn, err := r.store.Get(context.Background(), "tenant-a", name)
		if err != nil || !conn.Control || conn.CreatedBy != "" {
			t.Fatalf("%s stored %+v %v", name, conn, err)
		}
	}
	// No Card yet: every tool in the contract requires one, so KIFF
	// refuses its calls until the owner signs Coach's Cards.
	contract := r.api.applied["phone-a"]
	if n := strings.Count(contract, "requires_authority: true"); n != 2 {
		t.Fatalf("%d tools require a Card, want 2:\n%s", n, contract)
	}
	// The same batch again: nothing changes.
	if status, body := post("owner-a", "", batch); status != http.StatusOK || !strings.Contains(body, `"already":["taf_task","taf_read"]`) {
		t.Fatalf("again: %d %s", status, body)
	}
	// A name already connected on other terms refuses the whole batch.
	other := tool("taf_task", true)
	if status, body := post("owner-a", "", map[string]any{"connections": []any{other, tool("taf_stop", false)}}); status != http.StatusConflict {
		t.Fatalf("other terms: %d %s", status, body)
	}
	if _, err := r.store.Get(context.Background(), "tenant-a", "taf_stop"); err == nil {
		t.Fatal("a refused batch connected taf_stop")
	}
	// Twice in one batch, or none.
	if status, _ := post("owner-a", "", map[string]any{"connections": []any{tool("taf_x", false), tool("taf_x", false)}}); status != http.StatusBadRequest {
		t.Fatalf("a name twice: %d", status)
	}
	if status, _ := post("owner-a", "", map[string]any{"connections": []any{}}); status != http.StatusBadRequest {
		t.Fatalf("an empty batch: %d", status)
	}
}

// A batch takes a name over with replace only from the account's own control
// server (tafs cloud's tools left at an address it no longer has): replaced
// in the same change as the rest. A vendor's tool is never replaced this way.
func TestBatchReplacesOnlyTheControlServersOwnTools(t *testing.T) {
	r := newRig(t, dev)
	oldServer, newServer := openToolServer(t), openToolServer(t)
	tool := func(url string, replace bool) map[string]any {
		return map[string]any{"name": "taf_task", "url": url, "tool": "refund", "mode": "verify",
			"audience": url + "/mcp", "idempotency_argument": "idem", "control": true, "replace": replace}
	}
	post := func(b any) (int, string) {
		t.Helper()
		payload, _ := json.Marshal(b)
		req, _ := http.NewRequest(http.MethodPost, r.srv.URL+"/v1/connections/batch", bytes.NewReader(payload))
		req.Header.Set("Authorization", "Bearer owner-a")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(resp.Body)
		return resp.StatusCode, buf.String()
	}
	one := func(c map[string]any) map[string]any { return map[string]any{"connections": []any{c}} }
	if status, body := post(one(tool(oldServer.URL, false))); status != http.StatusCreated {
		t.Fatalf("first: %d %s", status, body)
	}
	// At the new address without replace: refused, as before.
	if status, body := post(one(tool(newServer.URL, false))); status != http.StatusConflict {
		t.Fatalf("without replace: %d %s", status, body)
	}
	// With replace: taken over, at the new address.
	status, body := post(one(tool(newServer.URL, true)))
	if status != http.StatusCreated || !strings.Contains(body, `"replaced":["taf_task"]`) {
		t.Fatalf("replace: %d %s", status, body)
	}
	if conn, err := r.store.Get(context.Background(), "tenant-a", "taf_task"); err != nil || conn.URL != newServer.URL || !conn.Control {
		t.Fatalf("after replace: %+v %v", conn, err)
	}
	// A vendor's tool under one of the names: never replaced.
	vendor := map[string]any{"name": "issue_refund", "url": r.tool.URL, "tool": "refund", "credential": toolSecret}
	payload, _ := json.Marshal(vendor)
	req, _ := http.NewRequest(http.MethodPost, r.srv.URL+"/v1/connections", bytes.NewReader(payload))
	req.Header.Set("Authorization", "Bearer owner-a")
	if resp, err := http.DefaultClient.Do(req); err != nil || resp.StatusCode != http.StatusCreated {
		t.Fatalf("the vendor's tool: %v %v", resp, err)
	}
	takeover := tool(newServer.URL, true)
	takeover["name"] = "issue_refund"
	if status, body := post(one(takeover)); status != http.StatusConflict {
		t.Fatalf("replacing a vendor's tool: %d %s", status, body)
	}
	if conn, _ := r.store.Get(context.Background(), "tenant-a", "issue_refund"); conn.Control || conn.URL != r.tool.URL {
		t.Fatalf("the vendor's tool changed: %+v", conn)
	}
}
