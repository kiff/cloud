package usage

// Cloud-side counter names. The framework already emits its own
// canonical counters (kiff.events.ingested, kiff.decisions.recorded,
// kiff.actions.validated, kiff.actions.executed,
// kiff.approvals.requested, kiff.approvals.reviewed) when the
// runtime is exercised; those land in cloud_meta.tenant_usage via
// the same Aggregator the proposal gate uses. The constants below
// are the cloud-specific counters that have no framework
// equivalent: outcomes from the proposal gate that exist only as a
// cloud-layer governance answer.
//
// Naming convention: kiff.cloud.* so cloud-emitted rows are
// distinguishable from framework-emitted ones in the same table.
// EntityType attribution rides on every Inc so a snapshot can be
// drawn per-domain when needed.
//
// Why these are separate from the framework's CounterDecisionsRecorded:
//
//   - kiff.decisions.recorded already increments when the proposal
//     handler calls rt.RecordActionProposal. That captures "a
//     proposal arrived"; it cannot capture "what KIFF answered"
//     because the framework does not know about the cloud's
//     allowed/blocked/approval_required/invalid taxonomy.
//   - Splitting outcomes into their own counters lets the
//     activation tracker show "tenant got at least one decision"
//     and the plan-limit logic count proposals that hit the gate
//     (allowed + approval_required + blocked + invalid) without
//     coupling to a framework refactor.
const (
	// CloudCounterProposalsSubmitted is incremented for every
	// genuinely new (non-duplicate) proposal the gate accepts for
	// evaluation, plus shape/runtime rejections — i.e. billable
	// arrivals. It is NOT bumped for idempotency dedup hits (those
	// carry CloudCounterProposalsDeduped) so a retry storm sharing
	// one key does not inflate submitted volume or consume plan
	// quota. Raw arrival volume, if ever needed, is submitted +
	// deduped. Pairs with the outcome counters to give an
	// activation signal independent of which answer KIFF returned.
	CloudCounterProposalsSubmitted = "kiff.cloud.proposals.submitted"

	// CloudCounterProposalsAllowed is incremented when KIFF returned
	// "allowed" — the contract was satisfied and the customer is
	// safe to execute.
	CloudCounterProposalsAllowed = "kiff.cloud.proposals.allowed"

	// CloudCounterProposalsApprovalRequired is incremented when
	// KIFF returned "approval_required". The customer must drive
	// the approval flow before executing.
	CloudCounterProposalsApprovalRequired = "kiff.cloud.proposals.approval_required"

	// CloudCounterProposalsOwnerRequired is incremented when KIFF
	// returned "owner_required": the action is outside a Card set to
	// come home, and is held for the Card's owner.
	// The customer must not execute until a re-decide allows it.
	CloudCounterProposalsOwnerRequired = "kiff.cloud.proposals.owner_required"

	// CloudCounterProposalsBlocked is incremented when KIFF returned
	// "blocked". The contract was violated; the customer must not
	// execute.
	CloudCounterProposalsBlocked = "kiff.cloud.proposals.blocked"

	// CloudCounterProposalsInvalid is incremented when KIFF returned
	// "invalid". The proposal itself was malformed (missing fields,
	// unknown action, infra blip).
	CloudCounterProposalsInvalid = "kiff.cloud.proposals.invalid"

	// CloudCounterProposalsLimitExceeded is incremented when the
	// proposal was rejected because the tenant exhausted their
	// monthly budget. Distinct from "blocked" / "invalid" so the
	// dashboard / activation tracker can show it as an upgrade
	// trigger rather than a governance denial. The proposal is
	// not evaluated against the contract; KIFF returns the
	// stable "limit_exceeded" outcome immediately.
	CloudCounterProposalsLimitExceeded = "kiff.cloud.proposals.limit_exceeded"

	// CloudCounterProposalsOverage is incremented when a paid tenant
	// submits a genuinely new proposal after crossing its included
	// monthly allowance. Unlike a free tenant (which hard-stops with
	// limit_exceeded), a paid over-allowance proposal is still
	// evaluated against the contract — a commercial allowance must
	// not become a production outage on a consequential path. This counter is the billing signal for that
	// excess: it counts the operations beyond the allowance that a
	// future overage rate would price. It rides only on genuinely
	// new proposals (after idempotency dedup), so retries of one
	// logical action never create duplicate billable overage.
	CloudCounterProposalsOverage = "kiff.cloud.proposals.overage"

	// CloudCounterProposalsDeduped is incremented when a proposal
	// carrying a caller-supplied idempotency key (body id or the
	// Idempotency-Key header) matched an earlier decision for the
	// same key within the idempotency window, so the gate returned
	// the recorded decision without re-evaluating or writing a new
	// decision/audit row. This is the retry-storm signal: a high
	// deduped:submitted ratio means a caller is retrying the same
	// logical action, which is exactly the cookbook scenario the
	// dedup protects (one decision, N withholds). The outcome
	// counters are NOT re-incremented on a dedup hit — only the
	// first evaluation counts toward allowed/blocked/etc.
	CloudCounterProposalsDeduped = "kiff.cloud.proposals.deduped"

	// CloudCounterProposalsShadowed is incremented when a proposal
	// was evaluated under observe mode: the outcome was
	// computed and recorded exactly as in enforce mode, but
	// returned to the caller as advisory rather than binding. This
	// is the observe→enforce readiness signal — paired with the
	// outcome counters it shows what a tenant's gate *would* have
	// allowed, blocked, or held for approval before they flip to
	// enforce. The outcome counters still fire under observe (the
	// would-be answer is real); this counter only marks that the
	// answer was non-binding.
	//
	// The series key keeps the pre-#773 "shadowed" spelling on
	// purpose: it is a persisted metric name with recorded history
	// behind it, so renaming it would orphan every existing row and
	// any dashboard reading the series. Only the vocabulary in the
	// product surface changed, not the identity of this counter.
	CloudCounterProposalsShadowed = "kiff.cloud.proposals.shadowed"
)

// CloudCounterNames lists every cloud-emitted counter so callers
// (the snapshot endpoint, kiff-tenant tooling) can iterate over
// the canonical set without hardcoding names twice.
var CloudCounterNames = []string{
	CloudCounterProposalsSubmitted,
	CloudCounterProposalsAllowed,
	CloudCounterProposalsApprovalRequired,
	CloudCounterProposalsOwnerRequired,
	CloudCounterProposalsBlocked,
	CloudCounterProposalsInvalid,
	CloudCounterProposalsLimitExceeded,
	CloudCounterProposalsOverage,
	CloudCounterProposalsDeduped,
	CloudCounterProposalsShadowed,
}

// CloudEntityProposal is the bounded entity_type label cloud
// counters emit instead of caller-supplied entity types. Using a
// caller-controlled string as a usage dimension is a cardinality
// DoS: any authenticated tenant could fan out
// cloud_meta.tenant_usage rows by varying the body. The cloud
// proposal counters do not need per-domain attribution at v0.1
// (the summary endpoint reports total counts), so a single fixed
// label keeps the row count per tenant bounded by len(CloudCounterNames)
// per hour bucket.
//
// Framework runtime counters (kiff.events.ingested,
// kiff.actions.executed, …) keep using the canonical entity type
// from the validated domain — those are server-controlled.
const CloudEntityProposal = "proposal"
