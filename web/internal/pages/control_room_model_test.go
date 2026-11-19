package pages

import (
	"testing"
	"time"

	"github.com/kiff/cloud/web/internal/apiclient"
)

// When the API returns a posture block with a recorded flip into
// enforce, the view model maps it and the section reports "recorded"
// with the became-enforce and last-changed labels populated.
func TestControlEvidenceFromAPI_MapsPostureHistory(t *testing.T) {
	became := time.Date(2026, time.May, 20, 9, 30, 0, 0, time.UTC)
	last := time.Date(2026, time.May, 22, 14, 0, 0, 0, time.UTC)
	resp := &apiclient.ControlDecisionsResponse{
		Control: apiclient.ControlDecisionsControl{
			ID: "ctl_1", Action: "MARK_PAID", Mode: "enforce",
			Posture: &apiclient.ControlDecisionsPosture{
				Current:         "enforce",
				BecameEnforceAt: &became,
				BecameEnforceBy: "gabo",
				LastChangedAt:   &last,
			},
		},
	}

	e := ControlEvidenceFromAPI(resp)
	if e.Posture == nil {
		t.Fatal("Posture: got nil, want mapped block")
	}
	if !controlRoomPostureRecorded(e) {
		t.Error("controlRoomPostureRecorded: got false, want true")
	}
	if got := controlRoomBecameEnforceLabel(e); got != "May 20, 2026 09:30 UTC by gabo" {
		t.Errorf("becameEnforceLabel: got %q", got)
	}
	if got := controlRoomLastChangedLabel(e); got != "May 22, 2026 14:00 UTC" {
		t.Errorf("lastChangedLabel: got %q", got)
	}
}

// A shadow-only tenant: the API returns a posture block with current
// mode but no flip recorded. The section stays on its honest
// empty-state and the became-enforce label is empty.
func TestControlEvidenceFromAPI_PostureShadowOnly(t *testing.T) {
	resp := &apiclient.ControlDecisionsResponse{
		Control: apiclient.ControlDecisionsControl{
			ID: "ctl_2", Action: "REFUND", Mode: "shadow",
			Posture: &apiclient.ControlDecisionsPosture{Current: "shadow"},
		},
	}

	e := ControlEvidenceFromAPI(resp)
	if controlRoomPostureRecorded(e) {
		t.Error("controlRoomPostureRecorded: got true, want false (no flip recorded)")
	}
	if got := controlRoomBecameEnforceLabel(e); got != "" {
		t.Errorf("becameEnforceLabel: got %q, want empty", got)
	}
}

// The flip timestamp is buyer-facing tamper-evidence copy, so it must
// render with year + an explicit UTC marker regardless of the parsed
// location (a review note 1).
func TestControlRoomBecameEnforceLabel_ForcesUTCWithYear(t *testing.T) {
	loc := time.FixedZone("PST", -8*3600)
	became := time.Date(2026, time.May, 20, 1, 30, 0, 0, loc) // 09:30 UTC
	e := &ControlEvidence{Posture: &ControlEvidencePosture{
		Current: "enforce", BecameEnforceAt: became, BecameEnforceBy: "gabo",
	}}
	if got := controlRoomBecameEnforceLabel(e); got != "May 20, 2026 09:30 UTC by gabo" {
		t.Errorf("becameEnforceLabel: got %q, want UTC-normalized with year", got)
	}
}

// No posture block at all (reader unwired): Posture is nil and the
// section falls back to the empty-state without panicking.
func TestControlEvidenceFromAPI_NoPostureBlock(t *testing.T) {
	resp := &apiclient.ControlDecisionsResponse{
		Control: apiclient.ControlDecisionsControl{ID: "ctl_3", Action: "SHIP", Mode: "enforce"},
	}

	e := ControlEvidenceFromAPI(resp)
	if e.Posture != nil {
		t.Errorf("Posture: got %+v, want nil", e.Posture)
	}
	if controlRoomPostureRecorded(e) {
		t.Error("controlRoomPostureRecorded: got true, want false")
	}
}

// controlRoomStateContextLabel renders the ✓/✗ hint that tells a reader
// whether the reconstructed state was in the action's allowed set. This
// is user-facing decision language, so lock the mapping.
func TestControlRoomStateContextLabel(t *testing.T) {
	cases := []struct {
		name string
		row  apiclient.ProposalDecision
		want string
	}{
		{
			name: "state in allowed set",
			row:  apiclient.ProposalDecision{StateAtDecision: "PENDING", AllowedStates: []string{"PENDING", "AUTHORIZED"}},
			want: "✓ allowed from here",
		},
		{
			name: "state not in allowed set",
			row:  apiclient.ProposalDecision{StateAtDecision: "PAID", AllowedStates: []string{"PENDING", "AUTHORIZED"}},
			want: "✗ not in allowed states",
		},
		{
			name: "no state recorded",
			row:  apiclient.ProposalDecision{AllowedStates: []string{"PENDING"}},
			want: "",
		},
		{
			name: "no allowed states recorded",
			row:  apiclient.ProposalDecision{StateAtDecision: "PAID"},
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := controlRoomStateContextLabel(tc.row); got != tc.want {
				t.Errorf("controlRoomStateContextLabel: got %q, want %q", got, tc.want)
			}
		})
	}
}

// controlRoomAllowedStatesLabel joins the allowed-state set for the State
// cell tooltip; empty when none recorded.
func TestControlRoomAllowedStatesLabel(t *testing.T) {
	if got := controlRoomAllowedStatesLabel(apiclient.ProposalDecision{AllowedStates: []string{"A", "B"}}); got != "A, B" {
		t.Errorf("allowedStatesLabel: got %q, want %q", got, "A, B")
	}
	if got := controlRoomAllowedStatesLabel(apiclient.ProposalDecision{}); got != "" {
		t.Errorf("allowedStatesLabel empty: got %q, want empty", got)
	}
}

// receiptOutcomeLabel / receiptOutcomePillClass map the decision outcome
// to the receipt head's verdict word and pill colour. Unknown values
// pass through as the label and a muted pill.
func TestReceiptOutcomeLabelAndPill(t *testing.T) {
	cases := []struct {
		outcome   string
		wantLabel string
		wantPill  string
	}{
		{"allowed", "allowed", "receipt-outcome-allowed"},
		{"blocked", "refused", "receipt-outcome-blocked"},
		{"approval_required", "held for approval", "receipt-outcome-held"},
		{"limit_exceeded", "rate-limited", "receipt-outcome-muted"},
		{"invalid", "invalid", "receipt-outcome-blocked"},
		{"surprise", "surprise", "receipt-outcome-muted"},
	}
	for _, tc := range cases {
		if got := receiptOutcomeLabel(tc.outcome); got != tc.wantLabel {
			t.Errorf("receiptOutcomeLabel(%q): got %q, want %q", tc.outcome, got, tc.wantLabel)
		}
		if got := receiptOutcomePillClass(tc.outcome); got != tc.wantPill {
			t.Errorf("receiptOutcomePillClass(%q): got %q, want %q", tc.outcome, got, tc.wantPill)
		}
	}
}

// A mandate refusal is the one reason on the decisions table a reader
// can act on somewhere else, so it leads to what the agent holds.
func TestMandateRefusalLeadsToTheHoldersCards(t *testing.T) {
	// Real codes: the gate emits "mandate_" + the decision outcome
	// (limit_reached, expired), plus no_mandate_issued and
	// mandate_unavailable.
	for _, reason := range []string{"no_mandate_issued", "mandate_limit_reached", "mandate_expired", "mandate_unavailable"} {
		href, ok := cardsForRefusalHref(apiclient.ProposalDecision{
			ActorID: "refund-agent-eu", Reasons: []string{reason},
		})
		if !ok {
			t.Fatalf("%s: no link offered", reason)
		}
		if href != "/cards?holder=refund-agent-eu" {
			t.Errorf("%s: href = %q", reason, href)
		}
	}
}

// A block decided before the card was consulted must not send anyone to
// the cards: it would teach them the wrong thing about where the
// decision came from.
func TestRefusalsBeforeTheCardDoNotLeadToIt(t *testing.T) {
	for _, reason := range []string{"state_not_allowed", "permission_denied", "unknown_action"} {
		if _, ok := cardsForRefusalHref(apiclient.ProposalDecision{
			ActorID: "refund-agent-eu", Reasons: []string{reason},
		}); ok {
			t.Errorf("%s: offered a card link for a decision no card took part in", reason)
		}
	}
	// No actor, nothing to filter by.
	if _, ok := cardsForRefusalHref(apiclient.ProposalDecision{Reasons: []string{"no_mandate_issued"}}); ok {
		t.Error("offered a holder filter with no holder")
	}
}
