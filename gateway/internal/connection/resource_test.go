package connection_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/kiff/cloud/gateway/internal/kiffapi"
	"github.com/kiff/cloud/gateway/internal/mcp"
)

// A connection can name the string argument that says what a call
// is on. It is stored, declared in the contract, and bound in the permit.
func TestConnectWithAResourceArgument(t *testing.T) {
	r := newRig(t, dev)
	req := r.refund(toolSecret)
	req["resource_argument"] = "order"
	status, body := r.do(t, "owner-a", http.MethodPost, "/v1/connections", req)
	if status != http.StatusCreated || !strings.Contains(body, `"resource_argument":"order"`) {
		t.Fatalf("create: %d %s", status, body)
	}
	if y := r.api.applied["owner-a"]; !strings.Contains(y, "required_parameters: [amount_eur, order]") || !strings.Contains(y, "type: string") {
		t.Fatalf("contract without the resource argument: %q", y)
	}
	if conns, _ := r.store.List(context.Background(), "tenant-a"); len(conns) != 1 || conns[0].ResourceArg != "order" {
		t.Fatalf("stored: %+v", conns)
	}

	open := openToolServer(t)
	if status, body := r.do(t, "owner-a", http.MethodPut, "/v1/connections/issue_refund/mode",
		map[string]string{"mode": "verify", "audience": "https://refunds.example.com/mcp", "url": open.URL}); status != http.StatusOK {
		t.Fatalf("to verify: %d %s", status, body)
	}
	if got := r.api.targets["owner-a"]; len(got) != 1 || got[0] != (kiffapi.PermitTarget{Domain: "gateway", Action: "issue_refund", Tool: "refund",
		Audience: "https://refunds.example.com/mcp", AmountArg: "amount_eur", ResourceArg: "order"}) {
		t.Fatalf("permit targets: %+v", got)
	}

	for _, tc := range []struct{ name, arg, want string }{
		{"not in the schema", "customer", "must be a string argument"},
		{"an integer", "amount_eur", "must be a string argument"},
		{"the idempotency argument", "idem", "must differ"},
		{"not a name", "order id", "not a valid argument name"},
		{"the gateway's operation argument", "kiff_operation_id", "KIFF uses for its own operation id"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := r.refund(toolSecret)
			req["name"], req["resource_argument"] = "other_tool", tc.arg
			if tc.arg == "kiff_operation_id" {
				// A tool whose schema really declares it as a string.
				req["url"], req["tool"] = opToolServer(t).URL, "refund_op"
				delete(req, "idempotency_argument")
			}
			if status, body := r.do(t, "owner-a", http.MethodPost, "/v1/connections", req); status != http.StatusBadRequest || !strings.Contains(body, tc.want) {
				t.Fatalf("%d %s", status, body)
			}
		})
	}
}

// opRefunds lists a tool whose schema declares the gateway's own operation
// argument as a string property.
type opRefunds struct{}

func (opRefunds) ListTools(context.Context) ([]mcp.Tool, error) {
	return []mcp.Tool{{Name: "refund_op", InputSchema: json.RawMessage(
		`{"type":"object","properties":{"kiff_operation_id":{"type":"string"},"amount_eur":{"type":"integer"}}}`)}}, nil
}

func (opRefunds) CallTool(context.Context, string, json.RawMessage) (json.RawMessage, error) {
	return mcp.TextResult("ok", false), nil
}

func opToolServer(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(&mcp.Server{Name: "tool", Version: "1", Auth: func(*http.Request) (mcp.Handler, error) {
		return opRefunds{}, nil
	}})
	t.Cleanup(srv.Close)
	return srv
}

// A connected tool's settings change without reconnecting. The
// resource argument and the no-effect opt-in are stored, declared in the
// contract and bound in the permit target in the same change; a field left
// out keeps its value, and the same checks as connecting apply.
func TestSettingsChangeWithoutReconnecting(t *testing.T) {
	r := newRig(t, dev)
	if status, body := r.do(t, "owner-a", http.MethodPost, "/v1/connections", r.refund(toolSecret)); status != http.StatusCreated {
		t.Fatalf("create: %d %s", status, body)
	}
	open := openToolServer(t)
	if status, body := r.do(t, "owner-a", http.MethodPut, "/v1/connections/issue_refund/mode",
		map[string]string{"mode": "verify", "audience": "https://refunds.example.com/mcp", "url": open.URL}); status != http.StatusOK {
		t.Fatalf("to verify: %d %s", status, body)
	}

	status, body := r.do(t, "owner-a", http.MethodPut, "/v1/connections/issue_refund/settings",
		map[string]string{"resource_argument": "order", "outcome_contract": "terminal_no_effect_v1"})
	if status != http.StatusOK || !strings.Contains(body, `"resource_argument":"order"`) || !strings.Contains(body, `"outcome_contract":"terminal_no_effect_v1"`) {
		t.Fatalf("settings: %d %s", status, body)
	}
	if y := r.api.applied["owner-a"]; !strings.Contains(y, "required_parameters: [amount_eur, order]") {
		t.Fatalf("contract without the resource argument: %q", y)
	}
	if got := r.api.targets["owner-a"]; len(got) != 1 || got[0].ResourceArg != "order" {
		t.Fatalf("permit targets: %+v", got)
	}

	// A field left out keeps its value.
	if status, body := r.do(t, "owner-a", http.MethodPut, "/v1/connections/issue_refund/settings",
		map[string]string{"resource_argument": ""}); status != http.StatusOK || strings.Contains(body, `"resource_argument"`) ||
		!strings.Contains(body, `"outcome_contract":"terminal_no_effect_v1"`) {
		t.Fatalf("clear resource: %d %s", status, body)
	}
	if got := r.api.targets["owner-a"]; len(got) != 1 || got[0].ResourceArg != "" {
		t.Fatalf("permit target kept the old resource argument: %+v", got)
	}
	if status, body := r.do(t, "owner-a", http.MethodPut, "/v1/connections/issue_refund/settings",
		map[string]string{"outcome_contract": ""}); status != http.StatusOK || strings.Contains(body, `"outcome_contract"`) {
		t.Fatalf("opt out: %d %s", status, body)
	}

	for _, tc := range []struct {
		name string
		body map[string]string
		want string
	}{
		{"nothing to change", map[string]string{}, "nothing to change"},
		{"not in the schema", map[string]string{"resource_argument": "customer"}, "must be a string argument"},
		{"the idempotency argument", map[string]string{"resource_argument": "idem"}, "must differ"},
		{"another contract", map[string]string{"outcome_contract": "trust_me"}, "terminal_no_effect_v1"},
	} {
		if status, body := r.do(t, "owner-a", http.MethodPut, "/v1/connections/issue_refund/settings", tc.body); status != http.StatusBadRequest ||
			!strings.Contains(body, tc.want) {
			t.Errorf("%s: %d %s", tc.name, status, body)
		}
	}
	if status, _ := r.do(t, "owner-b", http.MethodPut, "/v1/connections/issue_refund/settings", map[string]string{"resource_argument": "order"}); status != http.StatusNotFound {
		t.Fatalf("another account changed this tool: %d", status)
	}
}

// If apps/api does not take the contract, the settings do not change.
func TestSettingsNeedTheContractApplied(t *testing.T) {
	r := newRig(t, dev)
	if status, body := r.do(t, "owner-a", http.MethodPost, "/v1/connections", r.refund(toolSecret)); status != http.StatusCreated {
		t.Fatalf("create: %d %s", status, body)
	}
	r.api.mu.Lock()
	r.api.fail = true
	r.api.mu.Unlock()
	if status, _ := r.do(t, "owner-a", http.MethodPut, "/v1/connections/issue_refund/settings", map[string]string{"resource_argument": "order"}); status != http.StatusUnprocessableEntity {
		t.Fatalf("settings changed without the contract: %d", status)
	}
	if c, _ := r.store.Get(context.Background(), "tenant-a", "issue_refund"); c.ResourceArg != "" {
		t.Fatalf("connection changed: %+v", c)
	}
}

// Two partial changes at once each keep the other's field: the connection
// is read and merged under the account's lock, so neither writes back the
// value it read before the other committed.
func TestConcurrentPartialSettingsKeepBothChanges(t *testing.T) {
	r := newRig(t, dev)
	if status, body := r.do(t, "owner-a", http.MethodPost, "/v1/connections", r.refund(toolSecret)); status != http.StatusCreated {
		t.Fatalf("create: %d %s", status, body)
	}
	for i := 0; i < 10; i++ {
		if status, body := r.do(t, "owner-a", http.MethodPut, "/v1/connections/issue_refund/settings",
			map[string]string{"resource_argument": "", "outcome_contract": ""}); status != http.StatusOK {
			t.Fatalf("reset: %d %s", status, body)
		}
		var wg sync.WaitGroup
		for _, body := range []map[string]string{{"resource_argument": "order"}, {"outcome_contract": "terminal_no_effect_v1"}} {
			wg.Add(1)
			go func(body map[string]string) {
				defer wg.Done()
				if status, got := r.do(t, "owner-a", http.MethodPut, "/v1/connections/issue_refund/settings", body); status != http.StatusOK {
					t.Errorf("%v: %d %s", body, status, got)
				}
			}(body)
		}
		wg.Wait()
		c, err := r.store.Get(context.Background(), "tenant-a", "issue_refund")
		if err != nil || c.ResourceArg != "order" || c.OutcomeContract != "terminal_no_effect_v1" {
			t.Fatalf("round %d lost a change: %+v %v", i, c, err)
		}
	}
}

// A connection can name the read that says which currency a call is
// in. It is stored with the tool, and refused without an amount unit and a
// resource argument for the read to look up.
func TestConnectWithAUnitLookup(t *testing.T) {
	r := newRig(t, dev)
	lookup := map[string]any{"tool": "order_details", "argument": "order", "path": "currency"}
	req := r.refund(toolSecret)
	req["amount_unit"], req["amount_scale"], req["unit_lookup"] = "EUR", 2, lookup
	if status, body := r.do(t, "owner-a", http.MethodPost, "/v1/connections", req); status != http.StatusBadRequest || !strings.Contains(body, "resource argument") {
		t.Fatalf("without a resource argument: %d %s", status, body)
	}
	req["resource_argument"] = "order"
	status, body := r.do(t, "owner-a", http.MethodPost, "/v1/connections", req)
	if status != http.StatusCreated || !strings.Contains(body, `"unit_lookup":{"tool":"order_details","argument":"order","path":"currency"}`) {
		t.Fatalf("create: %d %s", status, body)
	}
	if conns, _ := r.store.List(context.Background(), "tenant-a"); len(conns) != 1 || conns[0].UnitLookup == nil || conns[0].UnitLookup.Path != "currency" {
		t.Fatalf("stored: %+v", conns)
	}
}
