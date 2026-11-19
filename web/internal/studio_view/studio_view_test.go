package studio_view_test

import (
	"errors"
	"testing"
	"time"

	studio "github.com/kiff/kiff-studio"

	"github.com/kiff/cloud/web/internal/studio_view"
)

// scratchBlueprint is the minimum-shape Blueprint that satisfies
// StageScratch but no higher.
func scratchBlueprint() studio.Blueprint {
	return studio.Blueprint{
		Domain: "refund-flow",
		Entity: "Order",
		Actions: []studio.Action{{
			Name: "ISSUE_REFUND",
		}},
	}
}

// shapeBlueprint adds states + transitions to scratchBlueprint.
func shapeBlueprint() studio.Blueprint {
	bp := scratchBlueprint()
	bp.States = []string{"REQUESTED", "REFUNDED"}
	bp.Events = []string{"ORDER_REQUESTED", "ORDER_REFUNDED"}
	bp.Transitions = []studio.Transition{
		{On: "ORDER_REQUESTED", From: "", To: "REQUESTED"},
	}
	bp.Actions = []studio.Action{{
		Name:          "ISSUE_REFUND",
		AllowedStates: []string{"REQUESTED"},
	}}
	return bp
}

// rulesBlueprint adds risk + approval + permissions + roles.
// Uses the canonical solo-signup shape (one role holding both
// proposing and .approve).
func rulesBlueprint() studio.Blueprint {
	bp := shapeBlueprint()
	bp.Actions[0].Risk = "high"
	bp.Actions[0].ApprovalRequired = true
	bp.Actions[0].RequiredPermissions = []string{
		"refund-flow.issue_refund",
		"refund-flow.issue_refund.approve",
	}
	bp.Roles = map[string][]string{
		"tenant_owner": {
			"refund-flow.issue_refund",
			"refund-flow.issue_refund.approve",
		},
	}
	return bp
}

// rulesBlueprintSplit returns the multi-user role split (no
// single role holds both proposing + .approve for the same
// action).
func rulesBlueprintSplit() studio.Blueprint {
	bp := rulesBlueprint()
	bp.Roles = map[string][]string{
		"operator":   {"refund-flow.issue_refund"},
		"supervisor": {"refund-flow.issue_refund.approve"},
	}
	return bp
}

// TestStageOf_TablesEveryTransition walks the four canonical
// shapes and asserts StageOf returns the expected stage.
func TestStageOf_TablesEveryTransition(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		bp   studio.Blueprint
		want studio_view.Stage
	}{
		{"empty entity → scratch", studio.Blueprint{}, studio_view.StageScratch},
		{"scratch", scratchBlueprint(), studio_view.StageScratch},
		{"shape", shapeBlueprint(), studio_view.StageShape},
		{"rules solo-signup", rulesBlueprint(), studio_view.StageRules},
		{"rules split", rulesBlueprintSplit(), studio_view.StageRules},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := studio_view.StageOf(tc.bp); got != tc.want {
				t.Fatalf("StageOf(%s) = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}

// TestStageOf_BackwardsCriteriaFail asserts that a Blueprint
// can't shortcut to a higher stage by populating only the later
// primitives. Stages are nested.
func TestStageOf_BackwardsCriteriaFail(t *testing.T) {
	t.Parallel()
	// Has roles but no state machine — should drop to Scratch
	// (not Rules) because the StageShape gate fails.
	bp := scratchBlueprint()
	bp.Roles = map[string][]string{
		"tenant_owner": {"refund-flow.issue_refund"},
	}
	if got := studio_view.StageOf(bp); got != studio_view.StageScratch {
		t.Fatalf("StageOf(scratch+roles, no state machine) = %v, want StageScratch", got)
	}
}

// TestAnalyzeTrustBoundary_Holds confirms the multi-user split
// lands on TrustBoundaryHolds.
func TestAnalyzeTrustBoundary_Holds(t *testing.T) {
	t.Parallel()
	got := studio_view.AnalyzeTrustBoundary(rulesBlueprintSplit())
	if got != studio_view.TrustBoundaryHolds {
		t.Fatalf("got %v, want TrustBoundaryHolds", got)
	}
}

// TestAnalyzeTrustBoundary_SoloSignup confirms the canonical
// solo-signup shape is named explicitly. Two distinct roles
// happens later; this is the launch default.
func TestAnalyzeTrustBoundary_SoloSignup(t *testing.T) {
	t.Parallel()
	got := studio_view.AnalyzeTrustBoundary(rulesBlueprint())
	if got != studio_view.TrustBoundarySoloSignup {
		t.Fatalf("got %v, want TrustBoundarySoloSignup", got)
	}
}

// TestAnalyzeTrustBoundary_UnknownBeforeRules confirms the
// templ surface doesn't render the line at stage 1 / 2.
func TestAnalyzeTrustBoundary_UnknownBeforeRules(t *testing.T) {
	t.Parallel()
	got := studio_view.AnalyzeTrustBoundary(shapeBlueprint())
	if got != studio_view.TrustBoundaryUnknown {
		t.Fatalf("got %v, want TrustBoundaryUnknown at StageShape", got)
	}
}

// TestStudioDraft_StagePromotesOnAdapters confirms that adding
// a sidecar adapter at StageRules promotes to StageFull.
func TestStudioDraft_StagePromotesOnAdapters(t *testing.T) {
	t.Parallel()
	d := studio_view.StudioDraft{Blueprint: rulesBlueprint()}
	if got := d.Stage(); got != studio_view.StageRules {
		t.Fatalf("StageRules expected before adapters; got %v", got)
	}

	d.Adapters = []studio_view.AdapterConfig{{Name: "x", Source: "y", Kind: "webhook"}}
	if got := d.Stage(); got != studio_view.StageFull {
		t.Fatalf("StageFull expected after adapter; got %v", got)
	}
}

// TestStudioDraft_AutoSaveLabel confirms the relative-time copy
// matches the spec's auto-save indicator semantics.
func TestStudioDraft_AutoSaveLabel(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 5, 28, 23, 30, 0, 0, time.UTC)
	cases := []struct {
		name      string
		updatedAt int64
		want      string
	}{
		{"never saved", 0, "ready"},
		{"just now", now.Unix(), "auto-saved · just now"},
		{"4 seconds ago", now.Unix() - 4, "auto-saved · just now"},
		{"14 seconds ago", now.Unix() - 14, "auto-saved · 14s ago"},
		{"4 minutes ago", now.Unix() - 4*60, "auto-saved · 4m ago"},
		{"2 hours ago", now.Unix() - 2*3600, "auto-saved · 2h ago"},
		{"3 days ago", now.Unix() - 3*86400, "auto-saved · 3d ago"},
		{"clock skew (future)", now.Unix() + 10, "auto-saved · just now"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := studio_view.StudioDraft{UpdatedAt: tc.updatedAt}
			if got := d.AutoSaveLabel(now); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// TestStudioDraft_GatesScaffoldAndActivate confirms the studio
// bar's two action buttons enable at the right stages.
func TestStudioDraft_GatesScaffoldAndActivate(t *testing.T) {
	t.Parallel()
	d := studio_view.StudioDraft{Blueprint: scratchBlueprint()}

	if d.ScaffoldEnabled() {
		t.Errorf("scaffold should be disabled at StageScratch")
	}
	if d.ActivateEnabled() {
		t.Errorf("activate should be disabled at StageScratch")
	}

	d = studio_view.StudioDraft{Blueprint: shapeBlueprint()}
	if d.ScaffoldEnabled() {
		t.Errorf("scaffold should be disabled before StageFull")
	}
	if !d.ActivateEnabled() {
		t.Errorf("activate should enable at StageShape")
	}

	d = studio_view.StudioDraft{
		Blueprint: rulesBlueprint(),
		Adapters:  []studio_view.AdapterConfig{{Name: "x", Source: "y", Kind: "webhook"}},
	}
	if !d.ScaffoldEnabled() {
		t.Errorf("scaffold should enable at StageFull")
	}
}

// TestStudioDraft_IndexErrors round-trips Issues into the
// FieldErrors map, then confirms FieldError lookups work.
func TestStudioDraft_IndexErrors(t *testing.T) {
	t.Parallel()
	d := studio_view.StudioDraft{
		Issues: []studio_view.FieldError{
			{BlockID: "entity", Field: "domain", Message: "must be lowercase"},
			{BlockID: "action-0", Field: "risk", Message: "must be low/medium/high"},
		},
	}
	d.IndexErrors()

	if got := d.FieldError("entity", "domain"); got != "must be lowercase" {
		t.Errorf("entity/domain: got %q", got)
	}
	if got := d.FieldError("action-0", "risk"); got != "must be low/medium/high" {
		t.Errorf("action-0/risk: got %q", got)
	}
	if got := d.FieldError("entity", "missing"); got != "" {
		t.Errorf("missing field should return empty; got %q", got)
	}
	if got := d.FieldError("nonexistent", "x"); got != "" {
		t.Errorf("nonexistent block should return empty; got %q", got)
	}
}

// TestParseValidationError_MapsKnownPatterns walks the error
// patterns the spec §6 names and asserts each lands on the right
// (block, field).
func TestParseValidationError_MapsKnownPatterns(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		err   string
		want  studio_view.FieldError
		strip bool // true = the message is stripped of "blueprint: " prefix
	}{
		{
			name: "domain bad",
			err:  `blueprint: domain "RefundFlow" must be lowercase letters, digits, and hyphens (e.g. refund-flow)`,
			want: studio_view.FieldError{BlockID: "entity", Field: "domain"},
		},
		{
			name: "entity bad",
			err:  `blueprint: entity "order" must be PascalCase (e.g. Order)`,
			want: studio_view.FieldError{BlockID: "entity", Field: "entity"},
		},
		{
			name: "too few states",
			err:  `blueprint: at least two states are required (a before-state and an after-state)`,
			want: studio_view.FieldError{BlockID: "state-machine", Field: "states"},
		},
		{
			name: "too few actions",
			err:  `blueprint: at least one action is required`,
			want: studio_view.FieldError{BlockID: "entity", Field: "actions"},
		},
		{
			name: "bad state name",
			err:  `blueprint: state "ready" must be UPPER_SNAKE_CASE`,
			want: studio_view.FieldError{BlockID: "state-machine", Field: "ready"},
		},
		{
			name: "bad event name",
			err:  `blueprint: event "ready" must be UPPER_SNAKE_CASE`,
			want: studio_view.FieldError{BlockID: "state-machine", Field: "ready"},
		},
		{
			name: "transition.on unknown",
			err:  `blueprint: transition.on "FOO" is not declared in events`,
			want: studio_view.FieldError{BlockID: "state-machine", Field: "transitions"},
		},
		{
			name: "action[0] field",
			err:  `blueprint: action[0] "ISSUE_REFUND": risk "x" must be one of low, medium, high, critical`,
			want: studio_view.FieldError{BlockID: "action-0", Field: "risk"},
		},
		{
			name: "action[2] required_permission",
			err:  `blueprint: action[2] "FOO": required_permission "Hermes" must be dotted.lowercase`,
			want: studio_view.FieldError{BlockID: "action-2", Field: "required_permissions"},
		},
		{
			name: "role permission bad",
			err:  `blueprint: role "ops" permission "Hermes.execute" must be dotted.lowercase`,
			want: studio_view.FieldError{BlockID: "role-ops", Field: "permissions"},
		},
		{
			name: "role name bad",
			err:  `blueprint: role "Ops" must be snake_case`,
			want: studio_view.FieldError{BlockID: "role-Ops", Field: "name"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := studio_view.ParseValidationError(errors.New(tc.err))
			if len(got) != 1 {
				t.Fatalf("expected 1 FieldError, got %d: %#v", len(got), got)
			}
			if got[0].BlockID != tc.want.BlockID {
				t.Errorf("BlockID: got %q, want %q", got[0].BlockID, tc.want.BlockID)
			}
			if got[0].Field != tc.want.Field {
				t.Errorf("Field: got %q, want %q", got[0].Field, tc.want.Field)
			}
			if got[0].Message == "" {
				t.Errorf("Message must be non-empty")
			}
		})
	}
}

// TestParseValidationError_UnknownErrorCollapses confirms the
// fallback path: unknown shapes land on the entity block as a
// "_unknown" field. The user still sees the error.
func TestParseValidationError_UnknownErrorCollapses(t *testing.T) {
	t.Parallel()
	got := studio_view.ParseValidationError(errors.New("something exploded"))
	if len(got) != 1 {
		t.Fatalf("expected 1 FieldError, got %d", len(got))
	}
	if got[0].BlockID != "entity" || got[0].Field != "_unknown" {
		t.Errorf("got %#v", got[0])
	}
}

// TestParseValidationError_NilReturnsNil is the defence-in-depth
// behaviour.
func TestParseValidationError_NilReturnsNil(t *testing.T) {
	t.Parallel()
	if got := studio_view.ParseValidationError(nil); got != nil {
		t.Fatalf("nil err should return nil; got %v", got)
	}
}

// TestParseValidationError_JoinedErrors confirms that an
// errors.Join'd error unwraps and accumulates FieldErrors per
// joined entry.
func TestParseValidationError_JoinedErrors(t *testing.T) {
	t.Parallel()
	joined := errors.Join(
		errors.New(`blueprint: domain "BAD" must be lowercase letters, digits, and hyphens`),
		errors.New(`blueprint: at least two states are required`),
	)
	got := studio_view.ParseValidationError(joined)
	if len(got) != 2 {
		t.Fatalf("expected 2 FieldErrors from joined error, got %d: %#v", len(got), got)
	}
}
