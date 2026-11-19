package pages

import (
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/kiff/cloud/web/internal/apiclient"
)

var controlRoomOutcomes = []string{"allowed", "blocked", "approval_required", "limit_exceeded", "invalid"}

// ControlEvidence is the web view model for the Control Room. It is
// intentionally close to the response so the on-screen renderer and
// proof-pack export keep the same evidence vocabulary.
type ControlEvidence struct {
	Control   ControlEvidenceControl
	Window    ControlEvidenceWindow
	Decisions ControlEvidenceDecisions
	// Posture is the recorded observe↔enforce history. Nil when the
	// API returned no posture block (reader unwired, or no change
	// recorded) — the section renders its honest empty-state.
	Posture *ControlEvidencePosture

	// Receipts, Alerts, Approvals are the additive evidence
	// sections. Each carries its own status (complete|empty|
	// unavailable) so the renderer branches on it for the honest
	// empty / "not yet recorded" states.
	Receipts  apiclient.ControlDecisionsReceipts
	Alerts    apiclient.ControlDecisionsAlerts
	Approvals apiclient.ControlDecisionsSection
}

// ControlEvidencePosture is the view model for the Posture section's
// recorded history: the observe→enforce flip moment and the most recent
// change. Zero times mean "not recorded".
type ControlEvidencePosture struct {
	Current         string
	BecameEnforceAt time.Time
	BecameEnforceBy string
	LastChangedAt   time.Time
}

type ControlEvidenceControl struct {
	ID     string
	Domain string
	Entity string
	Action string
	// Actions is the full set the control governs (a control mints from a
	// whole domain, so this carries every declared action). Action is the
	// primary/first for titling; Actions lets the identity card show them all.
	Actions     []string
	Environment string
	Workflow    string
	Status      string
	Mode        string
	ActivatedAt time.Time
	ActivatedBy string
}

type ControlEvidenceWindow struct {
	From time.Time
	To   time.Time
}

type ControlEvidenceDecisions struct {
	Total     int
	Counts    map[string]int
	Truncated bool
	Rows      []apiclient.ProposalDecision
	// ByAction is the per-action window-complete breakdown for a control
	// that governs several actions. Empty for single-action controls.
	ByAction []ControlEvidenceActionCounts
}

// ControlEvidenceActionCounts is one governed action's window-complete tally.
type ControlEvidenceActionCounts struct {
	Action string
	Total  int
	Counts map[string]int
}

func ControlEvidenceFromAPI(resp *apiclient.ControlDecisionsResponse) *ControlEvidence {
	if resp == nil {
		return nil
	}
	action := strings.TrimSpace(resp.Control.Action)
	if action == "" && len(resp.Control.Actions) > 0 {
		action = resp.Control.Actions[0]
	}
	counts := make(map[string]int, len(controlRoomOutcomes))
	for _, outcome := range controlRoomOutcomes {
		counts[outcome] = resp.Decisions.Counts[outcome]
	}
	for outcome, count := range resp.Decisions.Counts {
		if _, ok := counts[outcome]; !ok {
			counts[outcome] = count
		}
	}
	return &ControlEvidence{
		Control: ControlEvidenceControl{
			ID:          resp.Control.ID,
			Domain:      resp.Control.Domain,
			Entity:      resp.Control.Entity,
			Action:      action,
			Actions:     append([]string(nil), resp.Control.Actions...),
			Environment: resp.Control.Environment,
			Workflow:    resp.Control.Workflow,
			Status:      resp.Control.Status,
			Mode:        resp.Control.Mode,
			ActivatedAt: resp.Control.ActivatedAt,
			ActivatedBy: resp.Control.ActivatedBy,
		},
		Window: ControlEvidenceWindow{From: resp.Window.From, To: resp.Window.To},
		Decisions: ControlEvidenceDecisions{
			Total:     resp.Decisions.Total,
			Counts:    counts,
			Truncated: resp.Decisions.Truncated,
			Rows:      append([]apiclient.ProposalDecision(nil), resp.Decisions.Rows...),
			ByAction:  controlEvidenceByActionFromAPI(resp.ByAction),
		},
		Posture:   controlEvidencePostureFromAPI(resp.Control.Posture),
		Receipts:  resp.Receipts,
		Alerts:    resp.Alerts,
		Approvals: resp.Approvals,
	}
}

// controlEvidenceByActionFromAPI maps the per-action breakdown wire
// shape to the view model. Nil/empty stays nil so the renderer branches
// on "has a breakdown" cleanly.
func controlEvidenceByActionFromAPI(in []apiclient.ActionCountsView) []ControlEvidenceActionCounts {
	if len(in) == 0 {
		return nil
	}
	out := make([]ControlEvidenceActionCounts, 0, len(in))
	for _, a := range in {
		counts := make(map[string]int, len(a.Counts))
		for k, v := range a.Counts {
			counts[k] = v
		}
		out = append(out, ControlEvidenceActionCounts{Action: a.Action, Total: a.Total, Counts: counts})
	}
	return out
}

// controlEvidencePostureFromAPI maps the additive API posture block to
// the view model. Returns nil when the block is absent so the Posture
// section can branch on "recorded vs not".
func controlEvidencePostureFromAPI(p *apiclient.ControlDecisionsPosture) *ControlEvidencePosture {
	if p == nil {
		return nil
	}
	out := &ControlEvidencePosture{
		Current:         p.Current,
		BecameEnforceBy: p.BecameEnforceBy,
	}
	if p.BecameEnforceAt != nil {
		out.BecameEnforceAt = *p.BecameEnforceAt
	}
	if p.LastChangedAt != nil {
		out.LastChangedAt = *p.LastChangedAt
	}
	return out
}

func controlRoomDecisionCount(e *ControlEvidence, outcome string) int {
	if e == nil || e.Decisions.Counts == nil {
		return 0
	}
	return e.Decisions.Counts[outcome]
}

func controlRoomGovernanceTotal(e *ControlEvidence) int {
	return controlRoomDecisionCount(e, "allowed") +
		controlRoomDecisionCount(e, "blocked") +
		controlRoomDecisionCount(e, "approval_required")
}

func controlRoomWindowLabel(e *ControlEvidence) string {
	if e == nil || e.Window.From.IsZero() || e.Window.To.IsZero() {
		return "last 30 days"
	}
	return fmt.Sprintf("%s to %s", e.Window.From.Format("Jan 2"), e.Window.To.Format("Jan 2"))
}

func controlRoomModeLabel(mode string) string {
	switch strings.TrimSpace(mode) {
	case "observe", "shadow": // "shadow" is the earlier spelling
		return "observe"
	case "enforce":
		return "enforce"
	default:
		return "not recorded"
	}
}

func controlRoomOutcomePillClass(outcome string) string {
	switch outcome {
	case "allowed":
		return "dash-pill-ok"
	case "blocked":
		return "dash-pill-error"
	case "approval_required":
		return "dash-pill-info"
	default:
		return "dash-pill-muted"
	}
}

func controlRoomReasonLabel(row apiclient.ProposalDecision) string {
	if len(row.Reasons) == 0 {
		return "none recorded"
	}
	return strings.Join(row.Reasons, ", ")
}

// cardsForRefusalHref points a mandate refusal at the cards its holder
// holds. It answers only for the refusals a card explains: a block on
// state or permissions was decided before any card was consulted, and
// sending a reader to the cards for it would teach them the wrong
// thing about where the decision came from.
//
// It leads to the holder's cards rather than to one card, because the
// decision row records the actor and the reason, not which card was
// checked — several may cover one action, and naming the wrong one
// would be worse than naming none.
func cardsForRefusalHref(row apiclient.ProposalDecision) (string, bool) {
	if strings.TrimSpace(row.ActorID) == "" {
		return "", false
	}
	for _, r := range row.Reasons {
		// The key refusals lead there too: the holder's card page is
		// where the key issued for that agent is created.
		if r == "no_mandate_issued" || r == "key_not_bound_to_agent" || r == "actor_not_key_holder" || strings.HasPrefix(r, "mandate_") {
			return "/cards?holder=" + url.QueryEscape(row.ActorID), true
		}
	}
	return "", false
}

func controlRoomReceiptLabel(row apiclient.ProposalDecision) string {
	if row.ReceiptTraceID != "" {
		return row.ReceiptTraceID
	}
	if row.ReceiptStatus != "" && row.ReceiptStatus != "none" {
		return row.ReceiptStatus
	}
	return "not linked"
}

// controlRoomAllowedStatesLabel joins the action's allowed states for
// the State cell's tooltip. Empty when none recorded.
func controlRoomAllowedStatesLabel(row apiclient.ProposalDecision) string {
	if len(row.AllowedStates) == 0 {
		return ""
	}
	return strings.Join(row.AllowedStates, ", ")
}

// controlRoomStateContextLabel renders the short in-cell hint next to the
// reconstructed state: whether that state was in the action's allowed
// set. It makes a refusal legible — "PAID ✗ not allowed" reads as the
// decision being made against the entity's actual reconstructed state.
func controlRoomStateContextLabel(row apiclient.ProposalDecision) string {
	if row.StateAtDecision == "" || len(row.AllowedStates) == 0 {
		return ""
	}
	for _, s := range row.AllowedStates {
		if s == row.StateAtDecision {
			return "✓ allowed from here"
		}
	}
	return "✗ not in allowed states"
}

func controlRoomControlLabel(ctrl apiclient.Control) string {
	if len(ctrl.Actions) == 0 {
		return ctrl.Domain
	}
	return ctrl.Domain + " / " + ctrl.Actions[0]
}

// controlRoomActionsCountLabel is the honest header for the set of
// actions a control governs: "1 governed action" / "8 governed actions".
// A control mints from a whole domain (NewControlFromParse copies every
// action), so a domain with 8 actions is one control governing 8 — the
// nav used to show only Actions[0] and hide the rest.
func controlRoomActionsCountLabel(ctrl apiclient.Control) string {
	n := len(ctrl.Actions)
	if n == 1 {
		return "1 governed action"
	}
	return fmt.Sprintf("%d governed actions", n)
}

func controlRoomSelectedControlID(v DashboardControlsView) string {
	if v.SelectedControlID != "" {
		return v.SelectedControlID
	}
	if v.Evidence != nil {
		return v.Evidence.Control.ID
	}
	return ""
}

func controlStatusClass(status string) string {
	if strings.TrimSpace(status) == "active" {
		return "dash-pill-ok"
	}
	return "dash-pill-muted"
}

func controlEvidenceActivatedLabel(e *ControlEvidence) string {
	if e == nil || e.Control.ActivatedAt.IsZero() {
		return "not recorded"
	}
	label := e.Control.ActivatedAt.Format("Jan 2, 15:04")
	if e.Control.ActivatedBy != "" {
		label += " by " + e.Control.ActivatedBy
	}
	return label
}

func controlRoomHeading(v DashboardControlsView) string {
	if v.Evidence != nil {
		return v.Evidence.Control.Action + " evidence."
	}
	if len(v.Controls) == 0 {
		return "No controls yet."
	}
	return "Protected Control evidence."
}

func controlRoomProofPackHref(e *ControlEvidence) string {
	if e == nil {
		return "/dashboard"
	}
	href := "/dashboard/govern/proof-pack/" + url.PathEscape(e.Control.Action)
	if e.Control.Environment != "" {
		href += "?environment=" + url.QueryEscape(e.Control.Environment)
	}
	return href
}

// controlRoomPostureRecorded reports whether the API returned a posture
// history block with at least one recorded change. False keeps the
// section on its honest empty-state.
func controlRoomPostureRecorded(e *ControlEvidence) bool {
	if e == nil || e.Posture == nil {
		return false
	}
	return !e.Posture.LastChangedAt.IsZero() || !e.Posture.BecameEnforceAt.IsZero()
}

// controlRoomBecameEnforceLabel renders "Jan 2, 15:04 by <whom>" for the
// most recent flip into enforce, or "" when no such flip is recorded
// (always-observe, or always-enforce with no recorded transition).
func controlRoomBecameEnforceLabel(e *ControlEvidence) string {
	if e == nil || e.Posture == nil || e.Posture.BecameEnforceAt.IsZero() {
		return ""
	}
	label := e.Posture.BecameEnforceAt.UTC().Format("Jan 2, 2006 15:04 MST")
	if e.Posture.BecameEnforceBy != "" {
		label += " by " + e.Posture.BecameEnforceBy
	}
	return label
}

// controlRoomLastChangedLabel renders the timestamp of the most recent
// posture change of any kind, or "" when none is recorded.
func controlRoomLastChangedLabel(e *ControlEvidence) string {
	if e == nil || e.Posture == nil || e.Posture.LastChangedAt.IsZero() {
		return ""
	}
	return e.Posture.LastChangedAt.UTC().Format("Jan 2, 2006 15:04 MST")
}

// ── Receipts / Verifiability / Alerts / Approvals section helpers ──
// Each section carries its own status from the wire: "complete" →
// render data, "empty" → honest empty, "unavailable" → "not yet
// recorded". The web never infers status from an empty array.

func controlRoomSectionComplete(status string) bool {
	return strings.TrimSpace(status) == "complete"
}

func controlRoomSectionUnavailable(status string) bool {
	return strings.TrimSpace(status) == "unavailable"
}

func controlRoomReceiptsHeadline(e *ControlEvidence) string {
	if e == nil {
		return "No receipts."
	}
	r := e.Receipts
	return fmt.Sprintf("%d receipts · %d signed · %d anchored", r.Count, r.SignedCount, r.AnchoredCount)
}

// controlRoomReceiptsScopeNote is the honesty line that keeps a
// control-scoped sample from reading as a window-complete list.
func controlRoomReceiptsScopeNote(e *ControlEvidence) string {
	if e == nil {
		return ""
	}
	if e.Receipts.Truncated {
		return "Receipts for the decisions shown. The sample is capped, so this isn't the whole window."
	}
	return "Receipts for the decisions in this window."
}

func controlRoomReceiptSampleTime(s apiclient.ReceiptSampleView) string {
	if s.IssuedAt.IsZero() {
		return "not recorded"
	}
	return s.IssuedAt.UTC().Format("Jan 2, 2006 15:04 MST")
}

func controlRoomReceiptHref(traceID string) string {
	if strings.TrimSpace(traceID) == "" {
		return "/dashboard/receipts"
	}
	return "/dashboard/receipts/" + escapeReceiptPath(traceID)
}

func controlRoomReceiptSignedClass(s apiclient.ReceiptSampleView) string {
	if s.Signed {
		return "dash-pill-ok"
	}
	return "dash-pill-muted"
}

func controlRoomReceiptSignedLabel(s apiclient.ReceiptSampleView) string {
	if s.Signed {
		return "signed"
	}
	return "unsigned"
}

func controlRoomReceiptAnchorClass(s apiclient.ReceiptSampleView) string {
	if s.Anchored {
		return "dash-pill-ok"
	}
	return "dash-pill-info"
}

func controlRoomReceiptAnchorLabel(s apiclient.ReceiptSampleView) string {
	if s.Anchored {
		return "anchored"
	}
	return "signed-only"
}

// controlRoomReceiptIsDecision reports whether a sample is a decision
// (pre-execution) receipt — KIFF's signed proof that it decided before the
// action ran. Prefers the API's explicit kind; falls back to the decision-
// trace prefix for older API responses that omit kind.
func controlRoomReceiptIsDecision(s apiclient.ReceiptSampleView) bool {
	if s.Kind != "" {
		return s.Kind == "decision"
	}
	return strings.HasPrefix(s.TraceID, "decision-")
}

// controlRoomReceiptKindLabel is the pill text distinguishing a
// pre-execution decision receipt from an execution receipt.
func controlRoomReceiptKindLabel(s apiclient.ReceiptSampleView) string {
	if controlRoomReceiptIsDecision(s) {
		return "pre-execution"
	}
	return "execution"
}

func controlRoomReceiptKindClass(s apiclient.ReceiptSampleView) string {
	if controlRoomReceiptIsDecision(s) {
		return "dash-pill-info"
	}
	return "dash-pill-muted"
}

// controlRoomAnchoredTierClass / Label map the computed anchored tier
// (none|partial|all) to a pill.
func controlRoomAnchoredTierClass(anchored string) string {
	switch strings.TrimSpace(anchored) {
	case "all":
		return "dash-pill-ok"
	case "partial":
		return "dash-pill-info"
	default:
		return "dash-pill-muted"
	}
}

func controlRoomAnchoredTierLabel(anchored string) string {
	switch strings.TrimSpace(anchored) {
	case "all":
		return "all anchored"
	case "partial":
		return "partially anchored"
	default:
		return "signed, not anchored"
	}
}

// controlRoomVerifiabilityScopeNote renders the scope caveat so an
// "all anchored" claim in control scope is never misread as
// window-complete (/ Note 1).
func controlRoomVerifiabilityScopeNote(e *ControlEvidence) string {
	if e == nil {
		return ""
	}
	if strings.TrimSpace(e.Receipts.Verifiability.Scope) == "sample" {
		return fmt.Sprintf("Computed across the %d receipts shown, not the full window.", e.Receipts.Count)
	}
	return "Computed across all receipts in the window."
}

// controlRoomDeliveryClass / Label map a delivery status
// (delivered|failed|blocked) to a pill. "delivered" reads as "sent".
func controlRoomDeliveryClass(status string) string {
	switch strings.TrimSpace(status) {
	case "delivered":
		return "dash-pill-ok"
	case "blocked", "failed":
		return "dash-pill-error"
	default:
		return "dash-pill-muted"
	}
}

func controlRoomDeliveryLabel(status string) string {
	switch strings.TrimSpace(status) {
	case "delivered":
		return "sent"
	case "blocked":
		return "blocked"
	case "failed":
		return "failed"
	default:
		return controlFallback(status, "unknown")
	}
}
