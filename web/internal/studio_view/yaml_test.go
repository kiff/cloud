package studio_view_test

import (
	"reflect"
	"testing"

	studio "github.com/kiff/kiff-studio"

	"github.com/kiff/cloud/web/internal/studio_view"
)

// TestYAMLRoundTrip_StageFullPreservesEverything is the
// load-bearing parity test: a Blueprint emitted by studio.BuildYAML
// must round-trip through ParseYAML back to a structurally
// equivalent Blueprint. This is what makes the auto-save loop
// safe — every PUT decodes, mutates, and re-encodes; the
// invariant must hold or fields silently disappear between saves.
func TestYAMLRoundTrip_StageFullPreservesEverything(t *testing.T) {
	t.Parallel()

	original := studio.Blueprint{
		Domain: "refund-flow",
		Entity: "Order",
		Events: []string{"ORDER_REQUESTED", "ORDER_REFUNDED"},
		States: []string{"REQUESTED", "REFUNDED"},
		Transitions: []studio.Transition{
			{On: "ORDER_REQUESTED", From: "", To: "REQUESTED"},
			{On: "ORDER_REFUNDED", From: "REQUESTED", To: "REFUNDED"},
		},
		Actions: []studio.Action{{
			Name:               "ISSUE_REFUND",
			AllowedStates:      []string{"REQUESTED"},
			RequiredParameters: []string{"amount", "reason"},
			RequiredPermissions: []string{
				"refund-flow.issue_refund",
				"refund-flow.issue_refund.approve",
			},
			Risk:             "high",
			ApprovalRequired: true,
			Executor:         "cloud.proxy",
		}},
		Roles: map[string][]string{
			"tenant_owner": {
				"refund-flow.issue_refund",
				"refund-flow.issue_refund.approve",
			},
		},
	}

	yaml, err := studio.BuildYAML(original)
	if err != nil {
		t.Fatalf("studio.BuildYAML: %v", err)
	}

	parsed, err := studio_view.ParseYAML(yaml)
	if err != nil {
		t.Fatalf("ParseYAML: %v", err)
	}

	if !reflect.DeepEqual(original, parsed) {
		t.Fatalf("round-trip mismatch\n--- original ---\n%#v\n--- parsed ---\n%#v",
			original, parsed)
	}
}

// TestYAMLRoundTrip_StageScratchHandlesEmptyOptionalFields
// confirms a stage-1 Blueprint (no states, no transitions, no
// roles) round-trips without leaking spurious empty slices.
func TestYAMLRoundTrip_StageScratchHandlesEmptyOptionalFields(t *testing.T) {
	t.Parallel()

	original := studio.Blueprint{
		Domain: "test",
		Entity: "Thing",
		Actions: []studio.Action{{
			Name: "DO_IT",
			// Stage 1 — no allowed states, no permissions, no risk,
			// no executor. studio.BuildYAML would refuse this with
			// Validate; we feed the YAML through manually to test
			// the parser's empty-field handling.
		}},
	}
	// Hand-roll the YAML at the shape that emerges before
	// stage 2, since BuildYAML(<scratch>) would refuse.
	yaml := []byte(`domain: test
entity: Thing
events: []
states: []

transitions: []

actions:
  - name: DO_IT
    allowed_states: []
    required_parameters: []
    required_permissions: []
    risk: ""
    approval: never
    executor: ""

`)

	parsed, err := studio_view.ParseYAML(yaml)
	if err != nil {
		t.Fatalf("ParseYAML: %v", err)
	}

	if parsed.Domain != original.Domain || parsed.Entity != original.Entity {
		t.Errorf("domain/entity mismatch: got %q/%q, want %q/%q",
			parsed.Domain, parsed.Entity, original.Domain, original.Entity)
	}
	if len(parsed.Actions) != 1 || parsed.Actions[0].Name != "DO_IT" {
		t.Errorf("actions mismatch: %#v", parsed.Actions)
	}
	if parsed.Actions[0].ApprovalRequired {
		t.Errorf("approval=never should map to ApprovalRequired=false")
	}
}

// TestParseYAML_RejectsBadShape confirms that totally malformed
// YAML doesn't return a half-built Blueprint.
func TestParseYAML_RejectsBadShape(t *testing.T) {
	t.Parallel()

	// Tab indentation — yaml.v3 rejects it.
	bad := []byte("domain: test\n\tentity: bad\n")
	_, err := studio_view.ParseYAML(bad)
	if err == nil {
		t.Fatalf("expected parse error on bad YAML")
	}
}

// TestPartialYAML_RoundTrip_PreservesAllPopulatedFields is the
// regression guard for the auto-save data-loss path found in an
// earlier change. Every populated field on the
// Blueprint must round-trip through renderPartialYAML →
// ParseYAML identically. Without this, a tenant whose Blueprint
// drops below the previous stage's criteria (e.g. blanking the
// entity name) hits the fallback path; if the fallback YAML
// omits a field the user populated, the next reload silently
// drops their work.
//
// The test exercises every field on Action including the ones
// the original buggy implementation skipped:
// RequiredParameters, RequiredPermissions, Executor.
func TestPartialYAML_RoundTrip_PreservesAllPopulatedFields(t *testing.T) {
	t.Parallel()

	original := studio.Blueprint{
		Domain: "refund-flow",
		Entity: "Order",
		Events: []string{"ORDER_REQUESTED", "ORDER_REFUNDED"},
		States: []string{"REQUESTED", "REFUNDED"},
		Transitions: []studio.Transition{
			{On: "ORDER_REQUESTED", From: "", To: "REQUESTED"},
			{On: "ORDER_REFUNDED", From: "REQUESTED", To: "REFUNDED"},
		},
		Actions: []studio.Action{{
			Name:               "ISSUE_REFUND",
			AllowedStates:      []string{"REQUESTED"},
			RequiredParameters: []string{"amount", "reason"},
			RequiredPermissions: []string{
				"refund-flow.issue_refund",
				"refund-flow.issue_refund.approve",
			},
			Risk:             "high",
			ApprovalRequired: true,
			Executor:         "cloud.proxy",
		}},
		Roles: map[string][]string{
			"tenant_owner": {
				"refund-flow.issue_refund",
				"refund-flow.issue_refund.approve",
			},
		},
	}

	// Render through the partial path (the fallback that runs
	// when Blueprint.Validate fails). We can't trigger the
	// fallback by validation failure here without hand-rolling
	// invalid input, so we test the partial renderer directly
	// via the YAMLPreview escape hatch on the wrapper.
	d := studio_view.StudioDraft{Blueprint: original}
	yaml := d.YAMLPreview()

	parsed, err := studio_view.ParseYAML([]byte(yaml))
	if err != nil {
		t.Fatalf("ParseYAML: %v\n--- yaml ---\n%s", err, yaml)
	}

	if !reflect.DeepEqual(original, parsed) {
		t.Fatalf("partial-YAML round-trip lost fields\n--- original ---\n%#v\n--- parsed ---\n%#v\n--- yaml ---\n%s",
			original, parsed, yaml)
	}
}

// TestPartialYAML_RoundTrip_BelowValidatePreservesPermissions is
// the explicit reproduction of that data-loss bug. A Blueprint
// that has permissions populated but fails Validate (because Domain
// is empty — fails the "lowercase hyphenated" check) must still
// round-trip those permissions through the fallback path.
func TestPartialYAML_RoundTrip_BelowValidatePreservesPermissions(t *testing.T) {
	t.Parallel()

	// Domain empty → studio.BuildYAML rejects → YAMLPreview
	// uses the fallback path.
	original := studio.Blueprint{
		Domain: "", // intentionally empty
		Entity: "Order",
		States: []string{"REQUESTED"},
		Actions: []studio.Action{{
			Name:                "ISSUE_REFUND",
			AllowedStates:       []string{"REQUESTED"},
			RequiredParameters:  []string{"amount"},
			RequiredPermissions: []string{"refund-flow.issue_refund"},
			Risk:                "high",
			ApprovalRequired:    true,
			Executor:            "cloud.proxy",
		}},
	}

	d := studio_view.StudioDraft{Blueprint: original}
	yaml := d.YAMLPreview()

	// The partial path uses "<pick a name>" as a placeholder
	// when Domain is empty, so the parsed output's Domain won't
	// match. But every other field MUST round-trip — that's the
	// bug.
	parsed, err := studio_view.ParseYAML([]byte(yaml))
	if err != nil {
		t.Fatalf("ParseYAML: %v\n--- yaml ---\n%s", err, yaml)
	}

	// Fields the user typed, all of which the buggy version
	// dropped:
	if len(parsed.Actions) != 1 {
		t.Fatalf("Actions: got %d, want 1", len(parsed.Actions))
	}
	got := parsed.Actions[0]
	// Everything typed survives. The approval action also gains its
	// reviewer permission, which the preview adds on output; the draft
	// itself is never rewritten from the preview.
	wantPerms := []string{"refund-flow.issue_refund", "refund-flow.issue_refund.approve"}
	if !reflect.DeepEqual(got.RequiredPermissions, wantPerms) {
		t.Errorf("RequiredPermissions lost: got %v, want %v", got.RequiredPermissions, wantPerms)
	}
	wantParams := []string{"amount"}
	if !reflect.DeepEqual(got.RequiredParameters, wantParams) {
		t.Errorf("RequiredParameters lost: got %v, want %v", got.RequiredParameters, wantParams)
	}
	if got.Executor != "cloud.proxy" {
		t.Errorf("Executor lost: got %q, want cloud.proxy", got.Executor)
	}
	if got.Risk != "high" {
		t.Errorf("Risk lost: got %q, want high", got.Risk)
	}
	if !got.ApprovalRequired {
		t.Errorf("ApprovalRequired lost: got false, want true")
	}
}
