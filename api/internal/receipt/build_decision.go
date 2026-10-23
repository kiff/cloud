package receipt

import (
	"context"
	"errors"
	"fmt"
	"strings"

	kiffaudit "github.com/kiff/kiff/pkg/kiff/audit"
)

// build_decision.go is the "decision receipt" builder.
//
// The execution receipt (Build) requires a KindActionExecuted record —
// it proves an action ran. But a connected kiff-guard runtime in enforce
// mode is vote-shape: it calls POST /v1/proposals/decide and lets the
// framework run (or withhold) the tool itself. The cloud decides; the
// action does NOT execute through the cloud, so no action_executed record
// exists and Build cannot produce a receipt.
//
// BuildDecision closes that gap. It builds a signable receipt from the
// proposal_decision audit record the decide path writes (recordOutcome),
// so a vote-shape enforce runtime still produces cloud-visible, signable
// proof: "KIFF decided this before it ran." Anchoring rides the existing
// loop unchanged once the receipt is persisted.
//
// Honest boundary: a decision receipt attests the DECISION, not an
// execution. Its causal chain is the decision record(s), and its Outcome
// is the decision outcome (allowed | blocked | approval_required).

// ErrProposalIDRequired is returned when BuildDecision is called without a
// proposal id to resolve the decision record.
var ErrProposalIDRequired = errors.New("receipt: proposal id required")

// ErrNoDecision is returned when no proposal_decision audit record exists
// for the given proposal.
var ErrNoDecision = errors.New("receipt: no proposal_decision record found for proposal")

// ErrOutcomeNotReceiptable is returned when the terminal decision's outcome
// is not a governance outcome (allowed | blocked | approval_required) — so
// the builder refuses to mint a receipt for it. This makes the
// "only governance outcomes are receiptable" guarantee hold at the builder
// itself, not merely because the decide path happens to record invalid /
// limit_exceeded under a different audit kind.
var ErrOutcomeNotReceiptable = errors.New("receipt: decision outcome is not receiptable")

// decisionKind is the audit Kind the decide path stamps on a recorded
// decision (handlers.recordOutcome).
const decisionKind = kiffaudit.Kind("proposal_decision")

// DecisionReceiptTraceID is the cloud receipt id for a decision receipt.
// It is intentionally distinct from the execution-receipt id (the raw
// framework trace) and the blocked-receipt id so a decision receipt and a
// later execution receipt for the same proposal never collide on
// (tenant_id, trace_id). Deterministic, so re-minting upserts in place.
func DecisionReceiptTraceID(proposalID string) string {
	return "decision-" + proposalID
}

// IsDecisionReceiptTrace reports whether a receipt trace id was produced
// by DecisionReceiptTraceID — i.e. the receipt attests a DECISION
// (pre-execution), not an executed action. Single source of the
// "decision-" convention so consumers (the API sample view, the UI) don't
// re-encode the prefix.
func IsDecisionReceiptTrace(traceID string) bool {
	return strings.HasPrefix(traceID, "decision-")
}

// DecisionOutcomeReceiptable reports whether a decision outcome warrants a
// decision receipt. Governance outcomes (allowed | blocked |
// approval_required) do; non-governance rejections (invalid,
// limit_exceeded) do not — they are not a control deciding on an action.
func DecisionOutcomeReceiptable(outcome string) bool {
	switch outcome {
	case "allowed", "blocked", "approval_required", "owner_required", "approved_no_room":
		return true
	default:
		return false
	}
}

// BuildDecision assembles a v0.1 decision receipt from the
// proposal_decision audit record(s) the decide path wrote for proposalID.
// The decide path stamps the decision under TraceID == proposalID, so the
// builder queries that trace for proposal_decision records, orders them,
// and treats the last as the terminal decision.
//
// The returned receipt has no Attestation/RiskScore/InsurerEnvelope (the
// anchor loop signs it later, exactly as for execution receipts). Its own
// TraceID is DecisionReceiptTraceID(proposalID); ProposalID links it to
// the decision row in GET /v1/proposals.
func BuildDecision(ctx context.Context, audit AuditReader, tenantID, proposalID string) (Receipt, error) {
	if tenantID == "" {
		return Receipt{}, ErrTenantIDRequired
	}
	if proposalID == "" {
		return Receipt{}, ErrProposalIDRequired
	}

	records, err := audit.Query(ctx, kiffaudit.Filter{TraceID: proposalID, Kind: decisionKind})
	if err != nil {
		return Receipt{}, fmt.Errorf("query proposal_decision: %w", err)
	}
	orderRecords(records)
	if len(records) == 0 {
		return Receipt{}, ErrNoDecision
	}

	// The terminal decision is the most recent record in the trace.
	decision := records[len(records)-1]

	steps := make([]ChainStep, 0, len(records))
	for i, rec := range records {
		dataHash, err := hashRecordData(rec)
		if err != nil {
			return Receipt{}, fmt.Errorf("hash audit record %s: %w", rec.ID, err)
		}
		steps = append(steps, ChainStep{
			Sequence:   i + 1,
			Kind:       string(rec.Kind),
			ActorID:    rec.ActorID,
			OccurredAt: rec.CreatedAt.UTC(),
			// What it decided; a repeat reads "asked again".
			Message:  decisionStepMessage(records, i),
			DataHash: dataHash,
			Data:     decisionStepData(rec),
		})
	}

	outcome, _ := decision.Data["outcome"].(string)
	actionName, _ := decision.Data["action"].(string)
	domainID, _ := decision.Data["domain_id"].(string)

	// Gate at the builder: only governance outcomes are receiptable, so an
	// invalid / errored / malformed decision never yields a receipt even if
	// it were somehow recorded under the proposal_decision kind.
	if !DecisionOutcomeReceiptable(outcome) {
		return Receipt{}, fmt.Errorf("%w: %q", ErrOutcomeNotReceiptable, outcome)
	}

	return Receipt{
		SchemaVersion:   SchemaVersion,
		TraceID:         DecisionReceiptTraceID(proposalID),
		ProposalID:      proposalID,
		TenantID:        tenantID,
		EntityID:        decision.EntityID,
		EntityType:      decision.EntityType,
		DomainID:        domainID,
		ActionName:      actionName,
		Outcome:         outcome,
		IssuedAt:        decision.CreatedAt.UTC(),
		CausalChain:     steps,
		Attestation:     nil,
		RiskScore:       nil,
		ComplianceMap:   nil,
		InsurerEnvelope: nil,
	}, nil
}

// decisionStepData is what a decision recorded about its call, for the
// signed chain to carry (kiff/tafs#167): the amount, and the name of the
// argument the call is on with its value's hash. Nil when it recorded
// none, so a decision step without them is as before.
func decisionStepData(rec kiffaudit.Record) map[string]any {
	var data map[string]any
	for _, k := range []string{"amount", "on_arg", "on_hash"} {
		if v, ok := rec.Data[k]; ok && v != nil && v != "" {
			if data == nil {
				data = map[string]any{}
			}
			data[k] = v
		}
	}
	return data
}
