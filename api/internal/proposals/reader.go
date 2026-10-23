// Package proposals exposes the tenant-scoped read surface for
// proposal-decision rows: the per-row shape that GET /v1/proposals
// returns and the storage-agnostic Reader the HTTP handler
// consumes.
//
// Why a separate package: the proposal-gate handler
// (handlers.ProposalsHandler) already owns the write path. Pulling
// the read path into its own package keeps the gate's surface
// focused on POST /v1/proposals/decide and lets tests of the list
// endpoint inject an in-memory Reader without standing up the
// full gate stack. The handler depends on the Reader interface
// here, not on the postgres client.
//
// Storage shape:
//
//   - Decision rows live in the per-tenant kiff_t_<id>.kiff_audit
//     table, written by handlers.ProposalsHandler.recordOutcome
//     (kind=proposal_decision) and recordRejection
//     (kind=proposal_rejected). Their JSONB `data` column carries
//     outcome / reasons / principal attribution.
//   - Receipt enrichment reads cloud_meta.receipts via the
//     receipt.Store interface — a receipt only exists when a
//     governed action actually executed, so non-executed outcomes
//     (blocked / invalid / approval_required / limit_exceeded)
//     leave receipt_trace_id null by construction.
//
// Per-row safety: the Reader never surfaces the request
// `parameters` map or the caller-supplied `reasoning_summary`. The
// gate's recordOutcome / recordRejection do not write either to
// kiff_audit.data, so the absence on the wire is mechanical, not
// post-processing — but DecisionRow itself does not carry those
// fields so even a future change to the write path cannot leak
// them through this surface without breaking the build.
package proposals

import (
	"context"
	"github.com/kiff/cloud/api/internal/tooloutcome"
	"time"
)

// DecisionRow is one row in the GET /v1/proposals response. The
// JSON tags are the wire contract; field names match the dashboard's
// per-row shape.
//
// Fields that may be empty:
//
//   - ActorID is empty when the original proposal carried no
//     actor_id (the gate stamps "<unknown>" only on the rejection
//     path; we surface it verbatim).
//   - PrincipalSubject is empty when the gate authenticated via
//     a context that did not carry a subject (rare; the auth
//     middleware enforces non-empty in production).
//   - PrincipalAPIKeyID is empty when the call authenticated via
//     a session JWT, not an API key.
//   - ReceiptTraceID is empty (and ReceiptStatus = "none") when
//     no receipt has been issued for the underlying trace id.
//     Non-executed outcomes are always in this state.
//   - AnchorTxHash is empty when the receipt has been issued but
//     the anchor cycle has not yet committed a transaction.
//
// Fields that MUST never be populated by any caller:
//
//   - Anything containing the request `parameters` map — it is
//     tenant-controlled free-form JSON that may carry PII and
//     must not appear on this surface.
//   - The caller-supplied `reasoning_summary` string — same
//     reason.
type DecisionRow struct {
	ProposalID        string    `json:"proposal_id"`
	EntityID          string    `json:"entity_id"`
	EntityType        string    `json:"entity_type"`
	ActionName        string    `json:"action_name"`
	ActorID           string    `json:"actor_id,omitempty"`
	Outcome           string    `json:"outcome"`
	Reasons           []string  `json:"reasons,omitempty"`
	CreatedAt         time.Time `json:"created_at"`
	PrincipalSubject  string    `json:"principal_subject,omitempty"`
	PrincipalAPIKeyID string    `json:"principal_api_key_id,omitempty"`
	// ActorVerified is true when the decision was made with a key bound
	// to exactly ActorID: the actor is the credential's own
	// agent. False for an unbound key, a session, or a row recorded
	// before this existed — ActorID is then the caller's claim.
	ActorVerified bool `json:"actor_verified,omitempty"`

	// ToolOutcome is how an allowed gateway call ended at the tool, as
	// the gateway recorded it: done, refused by the tool, unknown,
	// not sent, or still sending. Set by the evidence handlers, not
	// stored with the decision; nil when there is no record.
	ToolOutcome *tooloutcome.Outcome `json:"tool_outcome,omitempty"`

	// Control identity. ControlID/Environment
	// scope a decision to the control that governed it, so the
	// window-native query and the evidence object can group
	// by control. Empty for decisions recorded before G0b or when no
	// active control matched the action. ControlVersion is the
	// governing control's version at decide time; 0 when
	// unknown.
	ControlID      string `json:"control_id,omitempty"`
	Environment    string `json:"environment,omitempty"`
	ControlVersion int    `json:"control_version,omitempty"`

	// State-at-decision.
	// StateAtDecision is the entity's lifecycle state KIFF
	// reconstructed at decision time — not a snapshot the caller
	// supplied; AllowedStates is the set of states from which the
	// action is permitted. Together they make the
	// "decided against reconstructed state" guarantee legible (e.g. a
	// retry refused because the entity was already PAID and PAID is
	// not in the action's allowed states). Empty for decisions
	// recorded before states were stamped, or for outcomes recorded before state
	// resolution (invalid / unknown_action).
	StateAtDecision string   `json:"state_at_decision,omitempty"`
	AllowedStates   []string `json:"allowed_states,omitempty"`

	// Receipt linkage. Today the proposal id is opportunistically
	// equal to the trace id the customer threads through their
	// later execute call; there is no enforced association. A
	// follow-up ticket can either thread prop.ID into execute as
	// the canonical trace id, or add a proposal_id column to
	// cloud_meta.receipts. The linkage is best-effort:
	// if a receipt row exists for (tenant_id, proposal_id), we
	// surface its trace id and status; otherwise the row reads as
	// "no receipt yet."
	ReceiptTraceID string `json:"receipt_trace_id,omitempty"`

	// ReceiptStatus is one of:
	//
	//   - "none"     — no receipt row exists for this proposal
	//                  id. All non-executed outcomes land here by
	//                  construction.
	//   - "pending"  — a receipt row exists but the cloud has not
	//                  yet committed the on-chain anchor
	//                  transaction. The receipt is signed (the
	//                  cloud signs at write time); the anchor is
	//                  what's missing.
	//   - "anchored" — a receipt row exists and AnchorTxHash is
	//                  populated.
	//
	// The "signed" state listed in the issue body has no distinct
	// on-disk form today — the cloud signs and anchors in the
	// same cycle. A future slice that introduces a signed-but-
	// not-anchored window can promote "pending" to a richer enum
	// without breaking the contract. Documented in the PR body.
	ReceiptStatus string `json:"receipt_status"`

	AnchorTxHash string `json:"anchor_tx_hash,omitempty"`
	// ReceiptSigned is true when the receipt carries KIFF's signature.
	// The activity says "signed" from it; not on the wire otherwise.
	ReceiptSigned bool `json:"-"`

	// What the call moves and what it is on, as the decision recorded
	// them (kiff/tafs#167): the amount in its unit, and the name of the
	// argument the call is on with the SHA-256 of its value ("sha256:"
	// and hex). Absent on decisions recorded before, and for actions
	// without them.
	Amount *CallAmount `json:"amount,omitempty"`
	OnArg  string      `json:"on_arg,omitempty"`
	OnHash string      `json:"on_hash,omitempty"`

	// TrialDraft is the draft whose trial made the call (kiff/tafs#173):
	// sent, and decided on the trial's own Cards. Empty for every other
	// call.
	TrialDraft string `json:"trial_draft,omitempty"`
}

// CallAmount is an amount as a decision recorded it: Value a decimal
// string in the unit's scale ("150.00"), never a float; Unit an ISO 4217
// code or the domain's own unit, empty when it declares none.
type CallAmount struct {
	Value string `json:"value"`
	Unit  string `json:"unit,omitempty"`
}

// DecisionQuery scopes a window-native, action-scoped decisions
// read. It filters at the storage layer rather than pulling
// a tenant-wide page and filtering in Go, so a busy tenant's
// evidence for one control over one window cannot silently fall
// outside a tenant-wide cap.
//
// Zero-value fields are "no filter on this dimension":
//   - Action empty       → all actions for the tenant.
//   - ControlID empty    → not scoped to a control (any control).
//   - Environment empty  → not scoped to an environment.
//   - From zero          → no lower bound.
//   - To zero            → no upper bound.
//
// Limit <= 0 uses DefaultLimit; the implementation clamps to
// MaxLimit. The window bounds are inclusive on From and To.
type DecisionQuery struct {
	Action      string
	ControlID   string
	Environment string
	// Actor optionally narrows to a single proposing identity
	// (actor_id) — the per-runtime/agent slice powering "view this
	// runtime's activity". Empty means all actors.
	Actor string
	// Outcomes optionally narrows to a set of outcomes (allowed,
	// blocked, approval_required, limit_exceeded, invalid). Empty
	// means all outcomes. Applied at the store layer so counts and
	// rows stay window-complete under the filter.
	Outcomes []string
	From     time.Time
	To       time.Time
	Limit    int
	// Offset skips the first N matching rows (DESC created_at) for
	// pagination. The per-outcome counts stay window-complete, so a
	// caller derives the page count from Counts + the active Outcomes
	// filter; Offset only pages the returned rows. 0 = first page.
	Offset int
}

// Reader is the storage-agnostic surface the GET /v1/proposals
// handler consumes. Two implementations:
//
//   - PostgresReader (production) reads kiff_t_<id>.kiff_audit
//     through tenant.SchemaPool and joins to cloud_meta.receipts
//     via receipt.Store.
//   - MemoryReader (tests) holds a slice of pre-built rows so
//     handler-level tests can exercise the wire shape without
//     standing up Postgres.
//
// The Reader is tenant-scoped at the call boundary: every method
// takes a tenantID and the implementation guarantees no row from
// another tenant can be returned. PostgresReader achieves this
// mechanically (the per-tenant pool's search_path is pinned by
// AfterConnect; the cloud_meta read uses tenant_id = $1
// predicate). MemoryReader filters on the tenantID in its slice.
type Reader interface {
	// ListDecisions returns the most recent proposal decisions
	// for tenantID in descending created_at order, capped at
	// limit. limit <= 0 means "use the implementation's
	// default" (the production reader uses 20; tests
	// can pass any positive int directly).
	//
	// Returns an empty slice (not nil) when no rows match. The
	// handler relies on this so it can JSON-encode the empty
	// list without nil-checking.
	ListDecisions(ctx context.Context, tenantID string, limit int) ([]DecisionRow, error)

	// ListDecisionsInWindow returns decisions for tenantID
	// matching q (action / control / environment / time window),
	// filtered at the storage layer, in descending created_at
	// order, capped at q.Limit. This is the window-native query
	// the proof pack and the Control Room evidence object scope
	// with: the window bounds the query, so "truncated"
	// means "more than limit matched this control+window", not
	// "we hit a tenant-wide cap".
	//
	// Returns an empty slice (not nil) when no rows match.
	ListDecisionsInWindow(ctx context.Context, tenantID string, q DecisionQuery) ([]DecisionRow, error)

	// CountDecisionsByOutcome returns the per-outcome counts for
	// tenantID matching q, computed over the FULL window+control —
	// independent of q.Limit. The Control Room header ("N allowed,
	// N blocked, N held") and the proof pack's decisions.counts read
	// from this, so a busy window cannot silently under-report by
	// counting only the capped row sample. q.Limit is ignored.
	//
	// The map is keyed by outcome string; absent outcomes are absent
	// (not zero). The returned total is the sum across outcomes.
	CountDecisionsByOutcome(ctx context.Context, tenantID string, q DecisionQuery) (counts map[string]int, total int, err error)

	// CountDecisionsOverTime returns per-outcome counts bucketed into
	// fixed-width time intervals across q's window — the substrate for
	// the Activity "requests over time" chart. bucket is the interval
	// width (e.g. 1m, 1h); a non-positive bucket is treated as one
	// bucket over the whole window. Buckets are returned in ascending
	// time order and are dense-able by the caller from From/To+bucket
	// (the store returns only non-empty buckets). Same window/predicate
	// scoping as the other reads, so actor/outcome/domain filters apply.
	CountDecisionsOverTime(ctx context.Context, tenantID string, q DecisionQuery, bucket time.Duration) ([]TimeBucket, error)
}

// TimeBucket is one interval of the requests-over-time series: the
// bucket's start (UTC, aligned to the bucket width), the per-outcome
// counts within it, and the total across outcomes.
type TimeBucket struct {
	Start  time.Time      `json:"start"`
	Counts map[string]int `json:"counts"`
	Total  int            `json:"total"`
}

// DefaultLimit is the number of rows returned when the caller
// omits ?limit. Mirrors the receipts endpoint at 50, narrowed to
// 20 because the dashboard's first cut is "show me what
// just happened" and 20 fits comfortably above the fold.
const DefaultLimit = 20

// MaxLimit caps how many rows a single response can carry. The
// handler clamps over-cap requests silently (mirrors the receipts
// endpoint's posture). 100 is the issue's pinned ceiling.
const MaxLimit = 100

// MaxOffset bounds pagination skip so a pathological ?offset can't
// force an unboundedly deep scan. The evidence window is bounded and
// the per-outcome counts are window-complete, so a reader never needs
// to page beyond this; requests past it clamp and yield an empty page.
const MaxOffset = 10000
