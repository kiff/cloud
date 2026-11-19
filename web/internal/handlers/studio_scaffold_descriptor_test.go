package handlers

import (
	"encoding/json"
	"reflect"
	"testing"

	studio "github.com/kiff/kiff-studio"
)

// orderRefundBlueprint mirrors the canonical order/refund domain the
// framework's kiff scaffold golden test uses, so the cloud emitter is
// pinned against the same shape the CLI consumes.
func orderRefundBlueprint() studio.Blueprint {
	return studio.Blueprint{
		Domain: "refund",
		Entity: "Order",
		Events: []string{"ORDER_PLACED", "ORDER_PAID", "ORDER_REFUNDED"},
		States: []string{"CREATED", "PAID", "REFUNDED"},
		Transitions: []studio.Transition{
			{On: "ORDER_PLACED", From: "", To: "CREATED"},
			{On: "ORDER_PAID", From: "CREATED", To: "PAID"},
			{On: "ORDER_REFUNDED", From: "PAID", To: "REFUNDED"},
		},
		Actions: []studio.Action{
			{
				Name:                "MARK_PAID",
				AllowedStates:       []string{"CREATED"},
				RequiredParameters:  []string{"payment_id"},
				RequiredPermissions: []string{"refund.mark_paid"},
				Risk:                "low",
				ApprovalRequired:    false,
			},
			{
				Name:                "REFUND_ORDER",
				AllowedStates:       []string{"PAID"},
				RequiredParameters:  []string{"amount", "reason"},
				RequiredPermissions: []string{"refund.execute"},
				Risk:                "high",
				ApprovalRequired:    true,
			},
		},
		Roles: map[string][]string{
			"agent":    {"refund.mark_paid", "refund.execute"},
			"operator": {"refund.approve"},
		},
	}
}

func TestScaffoldDescriptor_MapsBlueprintAndDerivesFollowUps(t *testing.T) {
	t.Parallel()
	d := scaffoldDescriptorFromBlueprint(orderRefundBlueprint())

	if d.Domain != "refund" || d.Entity != "Order" {
		t.Fatalf("domain/entity = %q/%q, want refund/Order", d.Domain, d.Entity)
	}
	// Adapter defaults to the domain so the generated AdapterConst is sane.
	if d.Adapter != "refund" {
		t.Errorf("adapter = %q, want refund (defaulted from domain)", d.Adapter)
	}
	if len(d.Actions) != 2 {
		t.Fatalf("actions = %d, want 2", len(d.Actions))
	}

	markPaid, refund := d.Actions[0], d.Actions[1]

	// ApprovalRequired bool maps to the framework's never|required enum.
	if markPaid.Approval != "never" {
		t.Errorf("MARK_PAID approval = %q, want never", markPaid.Approval)
	}
	if refund.Approval != "required" {
		t.Errorf("REFUND_ORDER approval = %q, want required", refund.Approval)
	}

	// follow_up_events are derived from the transitions leaving each
	// action's allowed states — so the generated happy-path test advances
	// the entity. MARK_PAID (from CREATED) → ORDER_PAID; REFUND_ORDER
	// (from PAID) → ORDER_REFUNDED.
	if !reflect.DeepEqual(markPaid.FollowUpEvents, []string{"ORDER_PAID"}) {
		t.Errorf("MARK_PAID follow_up_events = %v, want [ORDER_PAID]", markPaid.FollowUpEvents)
	}
	if !reflect.DeepEqual(refund.FollowUpEvents, []string{"ORDER_REFUNDED"}) {
		t.Errorf("REFUND_ORDER follow_up_events = %v, want [ORDER_REFUNDED]", refund.FollowUpEvents)
	}
}

func TestScaffoldDescriptor_JSONShapeMatchesFrameworkFields(t *testing.T) {
	t.Parallel()
	out, err := scaffoldDescriptorJSON(orderRefundBlueprint())
	if err != nil {
		t.Fatalf("scaffoldDescriptorJSON: %v", err)
	}
	// Decode into a generic map and assert the exact top-level + action
	// field names the framework's DisallowUnknownFields decoder expects.
	// Drift here would be a hard parse error in `kiff scaffold`.
	var top map[string]json.RawMessage
	if err := json.Unmarshal(out, &top); err != nil {
		t.Fatalf("emitted descriptor is not valid JSON: %v", err)
	}
	for _, k := range []string{"domain", "entity", "adapter", "events", "states", "transitions", "actions"} {
		if _, ok := top[k]; !ok {
			t.Errorf("descriptor missing top-level field %q", k)
		}
	}
	var actions []map[string]json.RawMessage
	if err := json.Unmarshal(top["actions"], &actions); err != nil {
		t.Fatalf("actions not an array: %v", err)
	}
	for _, k := range []string{"name", "allowed_states", "required_parameters", "required_permissions", "risk", "approval", "follow_up_events"} {
		if _, ok := actions[0][k]; !ok {
			t.Errorf("action missing field %q", k)
		}
	}
}

// TestScaffoldDescriptor_EmptySlicesNotNull guards the JSON shape: empty
// parameter/permission lists must serialize as [] (the framework iterates
// them), never null-by-omission that could trip a stricter consumer.
func TestScaffoldDescriptor_EmptySlicesNotNull(t *testing.T) {
	t.Parallel()
	bp := orderRefundBlueprint()
	bp.Actions[0].RequiredParameters = nil
	bp.Actions[0].RequiredPermissions = nil
	d := scaffoldDescriptorFromBlueprint(bp)
	if d.Actions[0].RequiredParameters == nil || d.Actions[0].RequiredPermissions == nil {
		t.Error("empty action slices should be non-nil ([]), not nil")
	}
}
