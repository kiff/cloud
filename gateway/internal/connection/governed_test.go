package connection_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kiff/cloud/gateway/internal/mcp"
)

// A connection fixes the operation of a generic vendor tool and
// reads the amount and the resource at a path, as Stripe's
// stripe_api_write needs.

type stripeLike struct{}

func (stripeLike) ListTools(context.Context) ([]mcp.Tool, error) {
	return []mcp.Tool{{Name: "stripe_api_write", InputSchema: json.RawMessage(`{"type":"object","properties":{` +
		`"stripe_api_operation_id":{"type":"string"},"parameters":{"type":"object"},"human_confirmation":{"type":"object"},"limit":{"type":"integer"},` +
		`"idempotency_key":{"type":"string"}},` +
		`"required":["stripe_api_operation_id"]}`)}}, nil
}

func (stripeLike) CallTool(context.Context, string, json.RawMessage) (json.RawMessage, error) {
	return mcp.TextResult("ok", false), nil
}

func stripeLikeServer(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(&mcp.Server{Name: "stripe", Version: "1", Auth: func(*http.Request) (mcp.Handler, error) {
		return stripeLike{}, nil
	}})
	t.Cleanup(srv.Close)
	return srv
}

func stripeRefundRequest(url string) map[string]any {
	return map[string]any{"name": "stripe_refund", "url": url, "tool": "stripe_api_write", "credential": "rk_test_agent",
		"amount_path": "parameters.amount", "amount_unit": "EUR", "amount_scale": 2,
		"resource_path":   "parameters.charge",
		"fixed_arguments": map[string]any{"stripe_api_operation_id": "PostRefunds"}}
}

func TestConnectAGenericToolOperation(t *testing.T) {
	r := newRig(t, dev)
	srv := stripeLikeServer(t)
	status, body := r.do(t, "owner-a", http.MethodPost, "/v1/connections", stripeRefundRequest(srv.URL))
	if status != http.StatusCreated {
		t.Fatalf("create: %d %s", status, body)
	}
	for _, want := range []string{`"amount_argument":"amount"`, `"amount_path":"parameters.amount"`,
		`"resource_argument":"charge"`, `"resource_path":"parameters.charge"`, `"fixed_arguments":{"stripe_api_operation_id":"PostRefunds"}`} {
		if !strings.Contains(body, want) {
			t.Fatalf("response lacks %s: %s", want, body)
		}
	}
	// The contract names the Card's parameters, not the paths.
	if y := r.api.applied["owner-a"]; !strings.Contains(y, "required_parameters: [amount, charge]") {
		t.Fatalf("contract: %q", y)
	}
	conns, _ := r.store.List(context.Background(), "tenant-a")
	var fixed map[string]string
	if len(conns) == 1 {
		_ = json.Unmarshal(conns[0].FixedArgs, &fixed)
	}
	if len(conns) != 1 || conns[0].AmountPath != "parameters.amount" || conns[0].ResourcePath != "parameters.charge" ||
		len(fixed) != 1 || fixed["stripe_api_operation_id"] != "PostRefunds" {
		t.Fatalf("stored: %+v", conns)
	}

	// A path is for sealed connections: apps/api binds verify and relay
	// permits to top-level arguments. (With an idempotency argument, so the
	// path rule is what refuses the move.)
	idem := stripeRefundRequest(srv.URL)
	idem["name"], idem["idempotency_argument"] = "stripe_refund_idem", "idempotency_key"
	if status, body := r.do(t, "owner-a", http.MethodPost, "/v1/connections", idem); status != http.StatusCreated {
		t.Fatalf("create with idempotency: %d %s", status, body)
	}
	open := openToolServer(t)
	if status, body := r.do(t, "owner-a", http.MethodPut, "/v1/connections/stripe_refund_idem/mode",
		map[string]string{"mode": "verify", "audience": "https://refunds.example.com/mcp", "url": open.URL}); status != http.StatusBadRequest || !strings.Contains(body, "only for sealed") {
		t.Fatalf("to verify: %d %s", status, body)
	}
	// The settings sheet changes top-level resource arguments only.
	if status, body := r.do(t, "owner-a", http.MethodPut, "/v1/connections/stripe_refund/settings",
		map[string]any{"resource_argument": "limit"}); status != http.StatusBadRequest || !strings.Contains(body, "reconnect") {
		t.Fatalf("settings: %d %s", status, body)
	}
}

// A one-part path is the argument itself, so a top-level tool can use
// either field.
func TestOnePartPathIsTheArgument(t *testing.T) {
	r := newRig(t, dev)
	req := r.refund(toolSecret)
	delete(req, "amount_argument")
	req["amount_path"] = "amount_eur"
	status, body := r.do(t, "owner-a", http.MethodPost, "/v1/connections", req)
	if status != http.StatusCreated || !strings.Contains(body, `"amount_argument":"amount_eur"`) || strings.Contains(body, `"amount_path"`) {
		t.Fatalf("create: %d %s", status, body)
	}
}

func TestGenericToolChecks(t *testing.T) {
	r := newRig(t, dev)
	srv := stripeLikeServer(t)
	for _, tc := range []struct {
		name   string
		change func(map[string]any)
		want   string
	}{
		{"path not starting at an object", func(m map[string]any) { m["amount_path"] = "limit.amount" }, "must start at an object argument"},
		{"path starting at an unknown argument", func(m map[string]any) { m["amount_path"] = "body.amount" }, "must start at an object argument"},
		{"path with a bad part", func(m map[string]any) { m["amount_path"] = "parameters.amount value" }, "argument names separated by dots"},
		{"path too deep", func(m map[string]any) { m["amount_path"] = "parameters.a.b.c.d" }, "argument names separated by dots"},
		{"same name for amount and resource", func(m map[string]any) { m["resource_path"] = "parameters.x.amount" }, "must differ"},
		{"fixed argument not in the schema", func(m map[string]any) { m["fixed_arguments"] = map[string]any{"operation": "PostRefunds"} }, "not an argument of the tool"},
		{"fixed argument where the amount is read", func(m map[string]any) {
			m["fixed_arguments"] = map[string]any{"parameters": map[string]any{"amount": 1}}
		}, "cannot be fixed"},
		{"fixed operation id", func(m map[string]any) { m["fixed_arguments"] = map[string]any{"kiff_operation_id": "x"} }, "not an argument of the tool"},
		{"fixed arguments not an object", func(m map[string]any) { m["fixed_arguments"] = []string{"PostRefunds"} }, "must be a JSON object"},
		{"a path in verify mode", func(m map[string]any) {
			m["mode"], m["audience"], m["idempotency_argument"] = "verify", "https://refunds.example.com/mcp", "idempotency_key"
			delete(m, "credential")
		}, "only for sealed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := stripeRefundRequest(srv.URL)
			tc.change(req)
			status, body := r.do(t, "owner-a", http.MethodPost, "/v1/connections", req)
			if status != http.StatusBadRequest || !strings.Contains(body, tc.want) {
				t.Fatalf("%d %s", status, body)
			}
		})
	}
	if conns, _ := r.store.List(context.Background(), "tenant-a"); len(conns) != 0 {
		t.Fatalf("a refused connection was stored: %+v", conns)
	}
}
