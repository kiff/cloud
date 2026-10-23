package domain_test

import (
	"errors"
	"strings"
	"testing"

	kiffaction "github.com/kiff/kiff/pkg/kiff/action"

	"github.com/kiff/cloud/api/internal/domain"
	"github.com/kiff/cloud/api/internal/executors"
)

const refundYAML = `
domain: refund
entity: Order

events:
  - ORDER_PLACED
  - ORDER_PAID
  - ORDER_REFUNDED

states:
  - CREATED
  - PAID
  - REFUNDED

transitions:
  - on: ORDER_PLACED
    from: ""
    to: CREATED
  - on: ORDER_PAID
    from: CREATED
    to: PAID
  - on: ORDER_REFUNDED
    from: PAID
    to: REFUNDED

actions:
  - name: MARK_PAID
    allowed_states: [CREATED]
    required_parameters: [payment_id]
    required_permissions: [orders.mark_paid]
    risk: low
    approval: never
    executor: refund.mark_paid

  - name: REFUND_ORDER
    allowed_states: [PAID]
    required_parameters: [amount, reason]
    required_permissions: [orders.refund]
    risk: high
    approval: required
    executor: refund.issue_refund

permissions:
  roles:
    ops_agent: [orders.mark_paid, orders.refund]
    ops_operator: [orders.mark_paid, orders.refund, refund.approve]
`

func TestParseAndBuildRefundDomain(t *testing.T) {
	t.Parallel()

	res, err := domain.ParseAndBuild([]byte(refundYAML), executors.DefaultRegistry())
	if err != nil {
		t.Fatalf("ParseAndBuild: %v", err)
	}

	if res.Definition.Name != "refund" {
		t.Errorf("name: got %q, want %q", res.Definition.Name, "refund")
	}
	if len(res.Definition.EntityTypes) != 1 || res.Definition.EntityTypes[0] != "Order" {
		t.Errorf("entity: got %v, want [Order]", res.Definition.EntityTypes)
	}
	if got := len(res.Definition.Actions.List()); got != 2 {
		t.Errorf("actions: got %d, want 2", got)
	}

	// Permission policy should answer questions about ops_agent +
	// orders.refund affirmatively.
	if res.Policy == nil {
		t.Fatal("nil policy")
	}
}

func TestParseRejectsEmptyYAML(t *testing.T) {
	t.Parallel()
	if _, err := domain.ParseAndBuild(nil, executors.DefaultRegistry()); err == nil {
		t.Fatal("expected error for empty yaml")
	}
}

func TestParseRejectsMalformedYAML(t *testing.T) {
	t.Parallel()
	bad := []byte("domain: refund\n  entity: Order\n  bogus: ::")
	if _, err := domain.ParseAndBuild(bad, executors.DefaultRegistry()); err == nil {
		t.Fatal("expected yaml decode error")
	}
}

func TestValidateCollectsAllErrors(t *testing.T) {
	t.Parallel()

	bad := `
domain: ""
entity: ""
events: []
states: []
transitions:
  - on: UNKNOWN_EVENT
    from: NOT_A_STATE
    to: ""
actions:
  - name: ""
    allowed_states: [WRONG_STATE]
    risk: critical
    approval: maybe
    executor: ""
`

	_, err := domain.ParseAndBuild([]byte(bad), executors.DefaultRegistry())
	if err == nil {
		t.Fatal("expected validation errors, got nil")
	}

	// errors.Join produces a wrapped multi-error; .Error() shows all
	// joined messages on separate lines. We assert each known
	// problem appears in the combined output.
	got := err.Error()
	for _, want := range []string{
		"domain: is required",
		"entity: is required",
		"events: must declare at least one event",
		"states: must declare at least one state",
		"transitions[0].on: unknown event",
		"transitions[0].to: is required",
		"actions[0].name: is required",
		"actions[0].allowed_states[0]: unknown state",
		"actions[0].risk: risk must be",
		"actions[0].approval: approval must be",
		"actions[0].executor: executor key is required",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing validation message %q in:\n%s", want, got)
		}
	}
}

func TestValidateRejectsUnknownExecutorKey(t *testing.T) {
	t.Parallel()

	bad := `
domain: refund
entity: Order
events: [ORDER_PLACED]
states: [CREATED]
transitions:
  - on: ORDER_PLACED
    from: ""
    to: CREATED
actions:
  - name: MARK_PAID
    allowed_states: [CREATED]
    executor: refund.does_not_exist
`

	_, err := domain.ParseAndBuild([]byte(bad), executors.DefaultRegistry())
	if err == nil {
		t.Fatal("expected unknown-executor error")
	}
	if !strings.Contains(err.Error(), "unknown executor key refund.does_not_exist") {
		t.Errorf("error did not mention unknown executor: %s", err)
	}
}

func TestValidateRejectsDuplicateActionName(t *testing.T) {
	t.Parallel()

	bad := `
domain: refund
entity: Order
events: [ORDER_PLACED]
states: [CREATED]
transitions:
  - on: ORDER_PLACED
    from: ""
    to: CREATED
actions:
  - name: MARK_PAID
    allowed_states: [CREATED]
    executor: refund.mark_paid
  - name: MARK_PAID
    allowed_states: [CREATED]
    executor: refund.mark_paid
`

	_, err := domain.ParseAndBuild([]byte(bad), executors.DefaultRegistry())
	if err == nil {
		t.Fatal("expected duplicate-action error")
	}
	if !strings.Contains(err.Error(), "duplicate action name MARK_PAID") {
		t.Errorf("error did not mention duplicate: %s", err)
	}
}

func TestParsedDefinitionMatchesGoBuilder(t *testing.T) {
	t.Parallel()

	res, err := domain.ParseAndBuild([]byte(refundYAML), executors.DefaultRegistry())
	if err != nil {
		t.Fatalf("ParseAndBuild: %v", err)
	}

	// The MARK_PAID action must surface with the right risk and
	// approval requirement; the framework's contract type is the
	// canonical shape.
	contract, ok := res.Definition.Actions.Get("MARK_PAID")
	if !ok {
		t.Fatal("MARK_PAID missing from definition")
	}
	if contract.Risk != kiffaction.RiskLow {
		t.Errorf("MARK_PAID risk: got %q, want low", contract.Risk)
	}
	if contract.ApprovalRequirement != kiffaction.ApprovalNever {
		t.Errorf("MARK_PAID approval: got %q, want never", contract.ApprovalRequirement)
	}
	if contract.Executor == nil {
		t.Error("MARK_PAID executor is nil")
	}

	refund, ok := res.Definition.Actions.Get("REFUND_ORDER")
	if !ok {
		t.Fatal("REFUND_ORDER missing from definition")
	}
	if refund.Risk != kiffaction.RiskHigh {
		t.Errorf("REFUND_ORDER risk: got %q, want high", refund.Risk)
	}
	if refund.ApprovalRequirement != kiffaction.ApprovalRequired {
		t.Errorf("REFUND_ORDER approval: got %q, want required", refund.ApprovalRequirement)
	}
}

// errorJoinUnwraps confirms our errors.Join shape preserves
// individual errors so callers can errors.As / errors.Is on them.
// Future code may use this to highlight each ValidationError in a
// dashboard.
func TestParseErrorsAreJoinable(t *testing.T) {
	t.Parallel()
	_, err := domain.ParseAndBuild([]byte("\ndomain: \"\"\n"), executors.DefaultRegistry())
	if err == nil {
		t.Fatal("expected error")
	}

	var ve domain.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("expected joined ValidationError, got %T: %v", err, err)
	}
}

// A run-context disposition outside the vocabulary is refused
// at parse time rather than ignored. Silently dropping it would leave an
// action whose author believes it consults run context with no rule at
// all — a control that reads as present and is not, which is the worst
// of the three states the design distinguishes.
func TestValidateRejectsUnknownRunContextDisposition(t *testing.T) {
	t.Parallel()

	bad := `
domain: refund
entity: Order
events: [ORDER_PLACED]
states: [CREATED]
transitions:
  - on: ORDER_PLACED
    from: ""
    to: CREATED
actions:
  - name: MARK_PAID
    allowed_states: [CREATED]
    executor: refund.auto_refund
    run_context:
      on_untrusted_input: hold-maybe
`

	_, err := domain.ParseAndBuild([]byte(bad), executors.DefaultRegistry())
	if err == nil {
		t.Fatal("expected an unknown-disposition error")
	}
	if !strings.Contains(err.Error(), "on_untrusted_input must be approval|block") {
		t.Errorf("error did not name the valid dispositions: %s", err)
	}
}

// The two valid dispositions parse and reach the public projection, so
// the decide path can read them back.
func TestRunContextDispositionsParseIntoTheSpec(t *testing.T) {
	t.Parallel()

	for _, disposition := range []string{domain.RunContextApproval, domain.RunContextBlock} {
		yaml := `
domain: refund
entity: Order
events: [ORDER_PLACED]
states: [CREATED]
transitions:
  - on: ORDER_PLACED
    from: ""
    to: CREATED
actions:
  - name: MARK_PAID
    allowed_states: [CREATED]
    executor: refund.auto_refund
    run_context:
      on_untrusted_input: ` + disposition + `
`
		res, err := domain.ParseAndBuild([]byte(yaml), executors.DefaultRegistry())
		if err != nil {
			t.Fatalf("%s: parse: %v", disposition, err)
		}
		got := res.Spec.Actions[0].RunContext
		if got == nil {
			t.Fatalf("%s: RunContext did not reach the spec", disposition)
		}
		if got.OnUntrustedInput != disposition {
			t.Errorf("OnUntrustedInput = %q, want %q", got.OnUntrustedInput, disposition)
		}
	}
}

// An action that declares no run_context block projects nil, so a domain
// authored before run contexts is unchanged and its evidence stays
// byte-identical.
func TestActionWithoutRunContextProjectsNil(t *testing.T) {
	t.Parallel()

	res, err := domain.ParseAndBuild([]byte(refundYAML), executors.DefaultRegistry())
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	for _, a := range res.Spec.Actions {
		if a.RunContext != nil {
			t.Errorf("action %s declared no run_context but projected %+v", a.Name, a.RunContext)
		}
	}
}

// For an action whose approval is required, the `.approve`
// permission is the reviewer's. The runtime contract does not ask the
// proposer to hold it (else any key able to propose could approve);
// the spec keeps it for the review gate. An action that runs without
// approval keeps it as a proposer requirement, so nothing becomes
// reachable that was not.
func TestReviewerPermission_IsNotAProposerRequirement(t *testing.T) {
	const yaml = `
domain: refunds
entity: Order
events: [ORDER_PAID]
states: [PAID]
transitions:
  - on: ORDER_PAID
    from: ""
    to: PAID
actions:
  - name: REFUND_ORDER
    allowed_states: [PAID]
    required_permissions: [orders.refund, orders.approve]
    approval: required
    executor: cloud.proxy
  - name: FORCE_REFUND
    allowed_states: [PAID]
    required_permissions: [orders.refund, orders.force.approve]
    approval: never
    executor: cloud.proxy
permissions:
  roles:
    agent: [orders.refund]
    admin: [orders.refund, orders.approve, orders.force.approve]
`
	parsed, err := domain.ParseAndBuild([]byte(yaml), executors.DefaultRegistry())
	if err != nil {
		t.Fatalf("ParseAndBuild: %v", err)
	}
	perms := func(action string) []string {
		c, ok := parsed.Definition.Actions.Get(action)
		if !ok {
			t.Fatalf("no contract %s", action)
		}
		var out []string
		for _, p := range c.RequiredPermissions {
			out = append(out, string(p))
		}
		return out
	}
	if got := perms("REFUND_ORDER"); len(got) != 1 || got[0] != "orders.refund" {
		t.Errorf("REFUND_ORDER proposer permissions = %v, want [orders.refund]", got)
	}
	if got := perms("FORCE_REFUND"); len(got) != 2 {
		t.Errorf("FORCE_REFUND (no approval) must keep its .approve permission for the proposer: %v", got)
	}
	for _, a := range parsed.Spec.Actions {
		if a.Name == "REFUND_ORDER" && a.ReviewerPermission() != "orders.approve" {
			t.Errorf("spec reviewer permission = %q, want orders.approve", a.ReviewerPermission())
		}
	}
}
