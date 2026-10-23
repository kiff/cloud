package executors_test

import (
	"context"
	"testing"

	"github.com/kiff/kiff/pkg/kiff/action"

	"github.com/kiff/cloud/api/internal/executors"
)

func noop(_ context.Context, _ action.ActionContext) (action.ActionResult, error) {
	return action.ActionResult{}, nil
}

func TestRegisterAndGet(t *testing.T) {
	t.Parallel()

	r := executors.NewRegistry()
	if err := r.Register("custom.x", noop); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if !r.Has("custom.x") {
		t.Fatal("Has(custom.x) returned false")
	}
	fn, ok := r.Get("custom.x")
	if !ok || fn == nil {
		t.Fatal("Get(custom.x) returned no fn")
	}
}

func TestRegisterRejectsEmpty(t *testing.T) {
	t.Parallel()

	r := executors.NewRegistry()
	if err := r.Register("", noop); err == nil {
		t.Fatal("Register(empty key) should fail")
	}
	if err := r.Register("custom.y", nil); err == nil {
		t.Fatal("Register(nil fn) should fail")
	}
}

func TestRegisterRejectsDuplicate(t *testing.T) {
	t.Parallel()

	r := executors.NewRegistry()
	if err := r.Register("custom.dup", noop); err != nil {
		t.Fatalf("first Register: %v", err)
	}
	if err := r.Register("custom.dup", noop); err == nil {
		t.Fatal("duplicate Register should fail")
	}
}

func TestDefaultRegistryHasV01Executors(t *testing.T) {
	t.Parallel()

	r := executors.DefaultRegistry()
	want := []string{
		"refund.mark_paid",
		"refund.issue_refund",
		"refund.auto_refund",
		"support.escalate",
		"support.close_ticket",
		"support.send_outreach",
		"support.waive_fee",
		"cafe.start_shift",
		"cafe.auto_order_inventory",
		"cafe.order_inventory",
		"cafe.request_specialty",
		"cafe.send_staff_message",
		"cafe.escalate_supplier",
		"cloud.proxy",
	}
	for _, k := range want {
		if !r.Has(k) {
			t.Errorf("DefaultRegistry missing %q", k)
		}
	}
	if got := len(r.Keys()); got != len(want) {
		t.Errorf("Keys count: got %d, want %d", got, len(want))
	}
}

func TestExecutorsReturnSucceededOnHappyPath(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		fn     executors.ExecutorFunc
		params map[string]any
	}{
		{"MarkPaid", executors.MarkPaid, map[string]any{"payment_id": "pay-1"}},
		{"IssueRefund", executors.IssueRefund, map[string]any{"amount": 49.5, "reason": "duplicate charge"}},
		{"AutoRefund", executors.AutoRefund, map[string]any{"amount": 1.99}},
		{"Escalate", executors.Escalate, map[string]any{"reason": "VIP customer"}},
		{"CloseTicket", executors.CloseTicket, map[string]any{"resolution": "answered"}},
		{"SendOutreach", executors.SendOutreach, map[string]any{"channel": "email", "body": "hi"}},
		{"WaiveFee", executors.WaiveFee, map[string]any{"amount": 5.0, "reason": "first late payment"}},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			ctx := action.ActionContext{
				ActionName: c.name,
				EntityID:   "ent-1",
				Parameters: c.params,
			}
			res, err := c.fn(context.Background(), ctx)
			if err != nil {
				t.Fatalf("%s err: %v", c.name, err)
			}
			if res.Status != action.ExecutionSucceeded {
				t.Fatalf("%s status: got %q, want succeeded", c.name, res.Status)
			}
			if !res.Executed {
				t.Fatalf("%s Executed flag: false", c.name)
			}
		})
	}
}

func TestExecutorsRejectMissingParameters(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		fn     executors.ExecutorFunc
		params map[string]any
	}{
		{"MarkPaid", executors.MarkPaid, map[string]any{}},
		{"IssueRefund", executors.IssueRefund, map[string]any{"amount": 0.0, "reason": ""}},
		{"AutoRefund", executors.AutoRefund, map[string]any{}},
		{"SendOutreach", executors.SendOutreach, map[string]any{"channel": "email"}}, // missing body
		{"WaiveFee", executors.WaiveFee, map[string]any{"amount": 0.0}},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			ctx := action.ActionContext{
				ActionName: c.name,
				EntityID:   "ent-1",
				Parameters: c.params,
			}
			_, err := c.fn(context.Background(), ctx)
			if err == nil {
				t.Fatalf("%s expected error for missing params", c.name)
			}
		})
	}
}

// ParseAndBuild documents a nil registry as "parse without checking
// executor keys", and the mandate validator takes it at its word: it
// wants the shape of a domain's actions, not the functions behind them.
// The validator honoured nil; the builder dereferenced it, so issuing a
// mandate against any domain whose actions name an executor panicked on
// a live endpoint.
//
// A nil registry has nothing registered. That is the answer at every
// door, not a crash at one of them.
func TestNilRegistryHasNothingRatherThanPanicking(t *testing.T) {
	var r *executors.Registry

	if r.Has("cafe.start_shift") {
		t.Error("a nil registry reported a key as registered")
	}
	fn, ok := r.Get("cafe.start_shift")
	if ok || fn != nil {
		t.Errorf("Get on a nil registry = (%v, %v), want (nil, false)", fn, ok)
	}
	if keys := r.Keys(); len(keys) != 0 {
		t.Errorf("Keys on a nil registry = %v, want none", keys)
	}
}
