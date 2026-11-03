package connection_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

// A profile is applied only by the connector key that connected its tools,
// only with the owner's approval as apps/api confirms it, and each apply
// is a new revision; the earlier one stays (connection profiles: a key alone never applies one, and a call keeps the one it was decided under).
func TestProfileAppliesOnlyWithTheOwnersApproval(t *testing.T) {
	r := newRig(t, dev)
	ctx := context.Background()
	if _, err := r.store.Pool.Exec(ctx, `TRUNCATE gateway.profiles`); err != nil {
		t.Fatal(err)
	}
	open := openToolServer(t)
	for _, name := range []string{"create_refund", "list_refunds"} {
		c := map[string]any{"name": name, "url": open.URL, "tool": "refund", "mode": "verify", "audience": "https://planque.example/v1/relay", "idempotency_argument": "idem"}
		if status, body := r.do(t, "connector-a", http.MethodPost, "/v1/connections", c); status != http.StatusCreated {
			t.Fatalf("connect %s: %d %s", name, status, body)
		}
	}
	if status, body := r.do(t, "owner-a", http.MethodPost, "/v1/connections", r.refund(toolSecret)); status != http.StatusCreated {
		t.Fatalf("owner connects: %d %s", status, body)
	}
	profile := func(tools ...string) map[string]any {
		ops := []map[string]string{}
		for _, t := range tools {
			ops = append(ops, map[string]string{"tool": t})
		}
		return map[string]any{"v": "kiff.profile.v1", "operations": ops}
	}
	apply := func(key string, p map[string]any, approval any) (int, string) {
		return r.do(t, key, http.MethodPost, "/v1/profiles", map[string]any{"profile": p, "approval": approval})
	}
	signed := map[string]bool{"signed": true}

	for name, c := range map[string]struct {
		key      string
		profile  map[string]any
		approval any
		status   int
		says     string
	}{
		"without the owner's approval":         {"connector-a", profile("create_refund"), map[string]bool{"signed": false}, http.StatusForbidden, "profile_not_approved"},
		"a profile KIFF refuses":               {"connector-a", map[string]any{"v": "bad"}, signed, http.StatusUnprocessableEntity, "kiff.profile.v1"},
		"the owner's own tool":                 {"connector-a", profile("create_refund", "issue_refund"), signed, http.StatusForbidden, "didn't connect"},
		"another connector's tools":            {"connector-a2", profile("create_refund"), signed, http.StatusForbidden, "didn't connect"},
		"the owner's session, not a connector": {"owner-a", profile("create_refund"), signed, http.StatusForbidden, "connector key"},
		"an agent's key":                       {"agent-a", profile("create_refund"), signed, http.StatusForbidden, ""},
	} {
		if status, body := apply(c.key, c.profile, c.approval); status != c.status || !strings.Contains(body, c.says) {
			t.Errorf("%s: %d %s", name, status, body)
		}
	}
	var n int
	if err := r.store.Pool.QueryRow(ctx, `SELECT count(*) FROM gateway.profiles`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("a refused apply kept a profile: %d %v", n, err)
	}

	status, body := apply("connector-a", profile("create_refund", "list_refunds"), signed)
	if status != http.StatusOK || !strings.Contains(body, `"revision":1`) || !strings.Contains(body, `"words_sha256":"w256"`) {
		t.Fatalf("apply: %d %s", status, body)
	}
	if status, body := apply("connector-a", profile("create_refund"), signed); status != http.StatusOK || !strings.Contains(body, `"revision":2`) {
		t.Fatalf("second apply: %d %s", status, body)
	}
	refund, _ := r.store.Get(ctx, "tenant-a", "create_refund")
	list, _ := r.store.Get(ctx, "tenant-a", "list_refunds")
	owners, _ := r.store.Get(ctx, "tenant-a", "issue_refund")
	if refund.ProfileRevision == nil || *refund.ProfileRevision != 2 || list.ProfileRevision == nil || *list.ProfileRevision != 1 || owners.ProfileRevision != nil {
		t.Fatalf("revisions: %v %v %v", refund.ProfileRevision, list.ProfileRevision, owners.ProfileRevision)
	}
	// A profile describes how a connection is counted; it never re-points
	// what the Card limits. A decimal amount is taken from it.
	invoice := map[string]any{"name": "record_invoice", "url": open.URL, "tool": "refund", "mode": "verify", "audience": "https://planque.example/v1/relay",
		"idempotency_argument": "idem", "amount_argument": "total", "amount_unit": "EUR", "amount_scale": 2, "amount_format": "decimal_string"}
	if status, body := r.do(t, "connector-a", http.MethodPost, "/v1/connections", invoice); status != http.StatusCreated {
		t.Fatalf("connect record_invoice: %d %s", status, body)
	}
	op := func(scale int) map[string]any {
		return map[string]any{"v": "kiff.profile.v1", "operations": []map[string]any{{"tool": "record_invoice", "kind": "write",
			"amount": map[string]any{"from": "argument", "arg": "total", "format": "decimal_string", "scale": scale, "unit": "EUR"}}}}
	}
	if status, body := apply("connector-a", op(0), signed); status != http.StatusConflict || !strings.Contains(body, "scale") {
		t.Fatalf("a profile that disagrees with its connection: %d %s", status, body)
	}
	if status, body := apply("connector-a", profile("record_invoice"), signed); status != http.StatusConflict || !strings.Contains(body, "doesn't name") {
		t.Fatalf("a profile that leaves out the connection's amount: %d %s", status, body)
	}
	if status, body := apply("connector-a", op(2), signed); status != http.StatusOK || !strings.Contains(body, `"revision":3`) {
		t.Fatalf("apply the invoice profile: %d %s", status, body)
	}
	var format string
	if err := r.store.Pool.QueryRow(ctx, `SELECT amount_format FROM gateway.tools WHERE tenant_id='tenant-a' AND name='record_invoice'`).Scan(&format); err != nil || format != "decimal_string" {
		t.Fatalf("amount_format = %q %v", format, err)
	}

	first, err := r.store.ProfileAt(ctx, "tenant-a", 1)
	if err != nil || first.DeviceID != "dev_1" || first.CreatedBy != "key-connector-a" || !strings.Contains(string(first.Raw), "list_refunds") {
		t.Fatalf("revision 1 is kept as approved: %+v %v", first, err)
	}
}
