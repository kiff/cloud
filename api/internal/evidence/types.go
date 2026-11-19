// Package evidence is the "one view model, two renderers" composer
// from the control evidence design. It assembles a single ControlEvidence value from the
// tenant-scoped read stores so the on-screen Control Room and the
// downloadable proof pack build from the same object and cannot drift.
//
// The composer is boring glue: each section is a small collector that
// reads one store and reports an honest status. It never reaches across
// tenants — every store call is tenant-scoped at the boundary, the same
// property the underlying readers already enforce.
//
// Scope: the Control Room addresses a control by control_id; the proof
// pack addresses it by action (+ optional environment) and deliberately
// keeps its decision/receipt queries action-scoped so it doesn't drop
// decisions recorded before G0b stamping. The composer takes the
// scope-appropriate inputs from the caller rather than inventing one
// canonical query — see Input.
package evidence

import (
	"time"

	"github.com/kiff/cloud/api/internal/domain"
	"github.com/kiff/cloud/api/internal/proposals"
	"github.com/kiff/cloud/api/internal/receipt"
)

// SectionStatus is the honest state of one evidence section. It keeps
// "we can't answer this yet" (Unavailable) distinct from "answered,
// nothing there" (Empty) distinct from "answered, here it is"
// (Complete). Truncation is a separate axis (see the per-section
// Truncated flag) because a Complete section can still be sampled.
type SectionStatus string

const (
	// StatusComplete: the section's source was available and answered.
	StatusComplete SectionStatus = "complete"
	// StatusEmpty: the source was available and there is nothing to show.
	StatusEmpty SectionStatus = "empty"
	// StatusUnavailable: the backing store/scope isn't wired or the
	// data can't be mechanically tied to this control/window yet, so the
	// section makes no claim rather than a hollow one.
	StatusUnavailable SectionStatus = "unavailable"
)

// Window is the inclusive [From, To] time window the evidence covers.
type Window struct {
	From time.Time
	To   time.Time
}

// Input drives one composition. Exactly one control-resolution path is
// used: ControlID (Control Room) or Action+Environment (proof pack).
// Decisions is the caller-built, scope-appropriate decision query — the
// composer does not invent it, because the two surfaces scope it
// differently on purpose.
type Input struct {
	TenantID string

	// Control resolution — set ControlID for the Control Room, or
	// Action(+Environment) for the proof-pack route.
	ControlID   string
	Action      string
	Environment string

	Window    Window
	Decisions proposals.DecisionQuery
}

// ControlEvidence is the single view model both surfaces render from.
// Fields are the raw, computed section materials; each renderer
// projects the subset it exposes. A nil/empty section is honest, not an
// error.
type ControlEvidence struct {
	TenantID    string
	Control     *domain.Control // resolved control identity; nil when unresolved
	Window      Window
	GeneratedAt time.Time

	Decisions DecisionsSection
	Receipts  ReceiptsSection
	Posture   PostureSection
	Alerts    AlertsSection
	Approvals ApprovalsSection

	// Domain contract/state-machine/policy — populated only when a
	// domain source is wired (the proof pack renders these; the
	// Control Room does not).
	Domain DomainSection
}

// DecisionsSection carries window-complete counts (independent of the
// row cap), the capped row sample, and a precise Truncated flag.
//
// ByAction is the per-action breakdown for a control that governs
// several actions (a control mints from a whole domain). Each entry is
// window-complete for that one action, so a Govern surface can show
// every governed action's own allowed/blocked/held instead of only the
// control-wide rollup. Empty for single-action or action-scoped reads.
type DecisionsSection struct {
	Status    SectionStatus
	Counts    map[string]int
	Total     int
	Truncated bool
	Rows      []proposals.DecisionRow
	ByAction  []ActionCounts
}

// ActionCounts is one governed action's window-complete decision tally.
type ActionCounts struct {
	Action string
	Counts map[string]int
	Total  int
}

// Receipt-scope values for ReceiptsSection.Scope / Verifiability.Scope.
// They tell a consumer whether the signed/anchored claim is over the
// full window or only the shown (capped) sample — so a control-scoped
// "all anchored" can never be misread as window-complete.
const (
	// ScopeWindow: receipts listed over the full action+window (proof pack).
	ScopeWindow = "window"
	// ScopeSample: receipts resolved over the capped decision sample
	// (Control Room control scope).
	ScopeSample = "sample"
)

// ReceiptsSection carries the receipt samples and the signed/anchored
// tally. Verifiability is computed from these counts by Verifiability().
//
// Truncated is meaningful only in control scope: there, receipts are
// resolved per decision over the capped decision sample (not a
// window-complete list), so Truncated mirrors the decisions sample
// being capped. Action scope (proof pack) leaves it false — its
// receipts are listed over the full action+window.
//
// Scope (window|sample) records which of those two the section is, so
// the verifiability claim carries it explicitly rather than relying on
// the reader to infer it from Truncated.
type ReceiptsSection struct {
	Status        SectionStatus
	Count         int
	SignedCount   int
	AnchoredCount int
	Truncated     bool
	Scope         string
	Samples       []receipt.Receipt
}

// PostureSection is the recorded observe↔enforce history (G5). Current
// is the effective mode; the became-enforce / last-changed moments are
// zero when not recorded.
type PostureSection struct {
	Status          SectionStatus
	Current         string
	BecameEnforceAt time.Time
	BecameEnforceBy string
	LastChangedAt   time.Time
}

// AlertDelivery is one alert delivery attempt tied to a triggering
// decision (G4), flattened for the view model.
type AlertDelivery struct {
	ProposalID  string
	TargetID    string
	TargetType  string
	Status      string
	Detail      string
	AttemptedAt time.Time
}

// AlertsSection carries alert deliveries enriched onto the decision
// rows by proposal_id (the G4 join key).
type AlertsSection struct {
	Status     SectionStatus
	Deliveries []AlertDelivery
}

// ApprovalsSection is intentionally Unavailable in Phase 1: the public
// approvals surface is tenant-wide/status-filtered and there is no
// mechanical (control, window) association yet, so the section makes no
// claim rather than a guessed one.
type ApprovalsSection struct {
	Status SectionStatus
}

// DomainSection carries the action contract, the entity state machine,
// and the relevant role→permission policy. Populated for the proof
// pack; absent (Unavailable) when no domain source is wired.
type DomainSection struct {
	Status      SectionStatus
	Contract    *domain.ActionSpec
	States      []string
	Transitions []domain.TransitionSpec
	Policy      map[string][]string
}

// Verifiability is the computed signed/anchored claim over the
// receipts — never asserted. Kept as a method so both renderers derive
// it identically from the same ReceiptsSection.
type Verifiability struct {
	SignedAlgorithm string
	SignedCount     int
	AnchoredCount   int
	Anchored        string // "none" | "partial" | "all"
	Note            string
	// Scope (window|sample) qualifies the claim: in control scope the
	// counts are over the shown sample, so "all" means "all in the
	// sample", not window-complete.
	Scope string
}

// Verifiability computes the tier-accurate claim from the receipts
// section. The Note strings are the canonical, audit-facing copy.
func (e ControlEvidence) Verifiability() Verifiability {
	r := e.Receipts
	v := Verifiability{
		SignedAlgorithm: receipt.AttestationAlgorithm,
		SignedCount:     r.SignedCount,
		AnchoredCount:   r.AnchoredCount,
		Scope:           r.Scope,
	}
	switch {
	case r.Count == 0:
		v.Anchored = "none"
		v.Note = "No receipts in this window — receipts are issued only when a governed action executes; blocked and approval-required decisions appear under decisions, not receipts."
	case r.AnchoredCount == 0:
		v.Anchored = "none"
		v.Note = "Receipts are signed and locally verifiable (recover the signer from the signature over the causal-chain hash). On-chain anchoring is not enabled for this tenant/window."
	case r.AnchoredCount == r.Count:
		v.Anchored = "all"
		v.Note = "Every receipt is signed and anchored: the signature verifies off-chain and the anchor transaction commits the record to the public trail."
	default:
		v.Anchored = "partial"
		v.Note = "All receipts are signed and locally verifiable; some are also anchored on the public trail (see each receipt's attestation)."
	}
	return v
}
