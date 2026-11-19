package studio_view_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	studio "github.com/kiff/kiff-studio"

	"github.com/kiff/cloud/web/internal/studio_view"
)

// approvalBlueprint is a complete Studio blueprint whose approval action
// declares only its proposing permission, held by the agent role: what
// Studio produced before.
func approvalBlueprint() studio.Blueprint {
	bp := shapeBlueprint()
	bp.Actions[0].Risk = "high"
	bp.Actions[0].ApprovalRequired = true
	bp.Actions[0].RequiredPermissions = []string{"refund-flow.issue_refund"}
	bp.Actions = append(bp.Actions, studio.Action{
		Name: "NOTE_ORDER", AllowedStates: []string{"REQUESTED"}, Risk: "low",
		RequiredPermissions: []string{"refund-flow.note_order"},
	})
	bp.Roles = map[string][]string{"support_agent": {"refund-flow.issue_refund", "refund-flow.note_order"}}
	return bp
}

// The property the API's catalog test pins for templates, on Studio's
// output. Every approval-required action in the YAML Studio emits names a
// reviewer permission, some role holds it, and no role that proposes the
// action holds it.
func TestYAMLPreview_ApprovalActionsDeclareASeparateReviewer(t *testing.T) {
	for name, bp := range map[string]studio.Blueprint{
		"complete":   approvalBlueprint(),
		"incomplete": func() studio.Blueprint { b := approvalBlueprint(); b.States = nil; return b }(),
	} {
		t.Run(name, func(t *testing.T) {
			out, err := studio_view.ParseYAML([]byte(studio_view.StudioDraft{Blueprint: bp}.YAMLPreview()))
			if err != nil {
				t.Fatalf("parse preview: %v", err)
			}
			for _, a := range out.Actions {
				reviewer := ""
				for _, p := range a.RequiredPermissions {
					if strings.HasSuffix(p, ".approve") {
						reviewer = p
					}
				}
				if !a.ApprovalRequired {
					if reviewer != "" {
						t.Errorf("%s needs no approval but got reviewer %s", a.Name, reviewer)
					}
					continue
				}
				if reviewer != "refund-flow.issue_refund.approve" {
					t.Fatalf("%s: reviewer = %q, want refund-flow.issue_refund.approve", a.Name, reviewer)
				}
				if !slices.Contains(out.Roles[studio_view.ReviewerRole], reviewer) {
					t.Errorf("%s: %s does not hold %s", a.Name, studio_view.ReviewerRole, reviewer)
				}
				for role, perms := range out.Roles {
					if slices.Contains(perms, "refund-flow.issue_refund") && slices.Contains(perms, reviewer) {
						t.Errorf("%s: role %s both proposes and reviews", a.Name, role)
					}
				}
			}
		})
	}
}

func TestWithReviewerPermissions_KeepsDeclaredReviewerAndInput(t *testing.T) {
	bp := rulesBlueprint() // already declares refund-flow.issue_refund.approve
	out := studio_view.WithReviewerPermissions(bp)
	if got := out.Actions[0].RequiredPermissions; len(got) != 2 {
		t.Errorf("a declared reviewer must be kept as is, got %v", got)
	}

	in := approvalBlueprint()
	_ = studio_view.WithReviewerPermissions(in)
	if len(in.Actions[0].RequiredPermissions) != 1 || len(in.Roles) != 1 {
		t.Errorf("the input blueprint was modified: %v %v", in.Actions[0].RequiredPermissions, in.Roles)
	}

	// No proposing permission: named after the domain and the action.
	bare := approvalBlueprint()
	bare.Actions[0].RequiredPermissions = nil
	if got := studio_view.WithReviewerPermissions(bare).Actions[0].RequiredPermissions; !slices.Equal(got, []string{"refund-flow.issue_refund.approve"}) {
		t.Errorf("reviewer without a proposing permission = %v", got)
	}
}

// a review: the result shares no slice or map with the input, both
// when a reviewer is added and when there is nothing to add.
func TestWithReviewerPermissions_ResultIsIndependentOfInput(t *testing.T) {
	for name, in := range map[string]studio.Blueprint{
		"reviewer added": approvalBlueprint(),
		"nothing to add": rulesBlueprint(),
	} {
		t.Run(name, func(t *testing.T) {
			before := studio_view.StudioDraft{Blueprint: in}.YAMLPreview()
			snapshot := fmt.Sprintf("%#v", in)

			out := studio_view.WithReviewerPermissions(in)
			out.Events[0] = "MUTATED"
			out.States[0] = "MUTATED"
			out.Transitions[0].To = "MUTATED"
			out.Actions[0].Name = "MUTATED"
			out.Actions[0].AllowedStates[0] = "MUTATED"
			out.Actions[0].RequiredPermissions[0] = "MUTATED"
			for role := range out.Roles {
				out.Roles[role][0] = "MUTATED"
			}
			out.Roles["new_role"] = []string{"MUTATED"}

			if got := fmt.Sprintf("%#v", in); got != snapshot {
				t.Errorf("mutating the result changed the input:\nbefore %s\nafter  %s", snapshot, got)
			}
			if after := (studio_view.StudioDraft{Blueprint: in}).YAMLPreview(); after != before {
				t.Error("mutating the result changed the input's rendered YAML")
			}
		})
	}
}
