package receipt

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	kiffaudit "github.com/kiff/kiff/pkg/kiff/audit"
)

// AuditReader is the subset of audit.Store the builder needs.
// Defined here (rather than depending on the framework's full
// Store interface) so tests can pass an in-memory implementation
// without wiring the rest of the framework.
type AuditReader interface {
	Query(ctx context.Context, filter kiffaudit.Filter) ([]kiffaudit.Record, error)
}

// BuildOptions configure a Build call.
type BuildOptions struct {
	// TenantID is stamped on the receipt. Required.
	TenantID string

	// TraceID is the framework trace correlating one execution's
	// audit records. Every record sharing this trace becomes a
	// step in the causal chain. Required.
	TraceID string

	// IssuedAt is the receipt's issuance time. Defaults to the
	// time the most recent audit record in the chain was created.
	// Override for deterministic tests.
	IssuedAt time.Time

	// ReplayLink is the cloud URL that replays this trace's
	// decision under any policy version. Optional at v0.1; the
	// receipt ships with the field empty when not provided.
	ReplayLink string

	// ComplianceMap is the per-domain compliance vocabulary. v0.1
	// ships a hand-curated map for the demo refund domain only;
	// callers stamp it onto the receipt explicitly so the package
	// itself does not encode domain knowledge.
	ComplianceMap []ComplianceMapping
}

// Errors returned by Build. Callers can inspect with errors.Is.
var (
	ErrTenantIDRequired        = errors.New("receipt: tenant id required")
	ErrTraceIDRequired         = errors.New("receipt: trace id required")
	ErrNoActionExecuted        = errors.New("receipt: no action_executed record found for trace")
	ErrNoBlockedDecision       = errors.New("receipt: no state_not_allowed action_failed record found")
	ErrMultipleActionsExecuted = errors.New("receipt: multiple action_executed records found for trace; v0.1 emits one receipt per execution")
	ErrRuntimeMissing          = errors.New("receipt: runtime is nil")
	ErrAuditStoreMissing       = errors.New("receipt: runtime has no audit store")
)

// Build assembles a v0.1 Attested Action Receipt from one trace's
// worth of audit records. The returned receipt has no
// Attestation, no RiskScore, no InsurerEnvelope: those fields are
// populated by later slices and can ship as null at v0.1.
//
// The build is deterministic given the same audit input: the
// causal chain is ordered by audit-record CreatedAt then ID, the
// per-step DataHash is sha256 over canonical JSON of the record's
// Data map, and the chain itself can be re-hashed with
// CausalChainHash to verify integrity.
//
// The trace must contain exactly one KindActionExecuted record.
// Multiple executions in a single trace would mean the framework
// recorded two action results under the same correlation, which
// is not how IngestEvent → ExecuteAction is wired today; the
// builder rejects it explicitly to keep the receipt's
// "what-action" semantics unambiguous.
func Build(ctx context.Context, audit AuditReader, opts BuildOptions) (Receipt, error) {
	if opts.TenantID == "" {
		return Receipt{}, ErrTenantIDRequired
	}
	if opts.TraceID == "" {
		return Receipt{}, ErrTraceIDRequired
	}

	records, err := audit.Query(ctx, kiffaudit.Filter{TraceID: opts.TraceID})
	if err != nil {
		return Receipt{}, fmt.Errorf("query audit: %w", err)
	}
	orderRecords(records)

	executed, err := findExecution(records)
	if err != nil {
		return Receipt{}, err
	}

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
			Message:    rec.Message,
			DataHash:   dataHash,
		})
	}

	issuedAt := opts.IssuedAt
	if issuedAt.IsZero() {
		issuedAt = executed.CreatedAt
	}
	issuedAt = issuedAt.UTC()

	actionName, _ := executed.Data["action"].(string)

	proposalID := proposalIDFromRecords(records)
	if proposalID == "" {
		proposalID = resolveProposalID(ctx, audit, executed.EntityID, actionName, "allowed", executed.CreatedAt)
	}

	r := Receipt{
		SchemaVersion:   SchemaVersion,
		TraceID:         opts.TraceID,
		ProposalID:      proposalID,
		TenantID:        opts.TenantID,
		EntityID:        executed.EntityID,
		EntityType:      executed.EntityType,
		ActionName:      actionName,
		Outcome:         "allowed",
		IssuedAt:        issuedAt,
		CausalChain:     steps,
		Attestation:     nil,
		RiskScore:       nil,
		ReplayLink:      opts.ReplayLink,
		ComplianceMap:   append([]ComplianceMapping(nil), opts.ComplianceMap...),
		InsurerEnvelope: nil,
	}
	return r, nil
}

// proposalIDFromRecords returns the canonical proposal id from a
// trace's audit records — the explicit decision↔receipt link.
// The proposal gate stamps data.proposal_id on the decision record
// (G0b); this reads it so the receipt carries an explicit reference
// rather than relying on the proposal_id == trace_id coincidence.
// Empty when no record carries it (legacy traces).
func proposalIDFromRecords(records []kiffaudit.Record) string {
	for _, rec := range records {
		if rec.Data == nil {
			continue
		}
		if pid, ok := rec.Data["proposal_id"].(string); ok && pid != "" {
			return pid
		}
	}
	return ""
}

// resolveProposalID finds the proposal id for an execution whose own
// trace carries no decision record — the production reality once the
// execute trace diverges from prop.ID. The proposal gate
// writes the decision under TraceID = prop.ID, which is a different
// trace than the action_executed record, so proposalIDFromRecords on
// the execution trace comes back empty. Rather than depend on a
// contract change in the framework, we query the same tenant audit
// store for the matching proposal_decision record directly.
//
// The match is (EntityID, action, outcome, CreatedAt <= notAfter):
// the decision must be for the same entity and action, with the same
// outcome the receipt records, and must have happened no later than
// the event the receipt is built from. When several decisions match
// (a retried action on the same entity), the latest one wins — it is
// the decision that authorised this execution. Returns empty when the
// entity or action is unknown, or when no decision matches.
func resolveProposalID(ctx context.Context, audit AuditReader, entityID, action, outcome string, notAfter time.Time) string {
	if entityID == "" || action == "" {
		return ""
	}
	records, err := audit.Query(ctx, kiffaudit.Filter{
		Kind:     kiffaudit.Kind("proposal_decision"),
		EntityID: entityID,
	})
	if err != nil {
		return ""
	}

	var (
		bestID string
		bestAt time.Time
	)
	for _, rec := range records {
		if rec.Data == nil {
			continue
		}
		if a, _ := rec.Data["action"].(string); a != action {
			continue
		}
		if o, _ := rec.Data["outcome"].(string); o != outcome {
			continue
		}
		if !notAfter.IsZero() && rec.CreatedAt.After(notAfter) {
			continue
		}
		pid, _ := rec.Data["proposal_id"].(string)
		if pid == "" {
			continue
		}
		if bestID == "" || rec.CreatedAt.After(bestAt) {
			bestID, bestAt = pid, rec.CreatedAt
		}
	}
	return bestID
}

// BuildBlocked assembles a v0.1 receipt for a decision that blocked
// before execution. The framework records state validation failures
// as action_failed audit records; this builder gives the cloud a
// distinct receipt id so a blocked retry does not overwrite the
// successful receipt that may share the same framework trace.
func BuildBlocked(ctx context.Context, audit AuditReader, opts BuildOptions) (Receipt, error) {
	if opts.TenantID == "" {
		return Receipt{}, ErrTenantIDRequired
	}
	if opts.TraceID == "" {
		return Receipt{}, ErrTraceIDRequired
	}

	records, err := audit.Query(ctx, kiffaudit.Filter{})
	if err != nil {
		return Receipt{}, fmt.Errorf("query audit: %w", err)
	}
	orderRecords(records)

	blocked, err := findBlockedDecision(records, opts.TraceID)
	if err != nil {
		return Receipt{}, err
	}

	chainRecords := recordsForBlockedReceipt(records, blocked)
	steps := make([]ChainStep, 0, len(chainRecords))
	for i, rec := range chainRecords {
		dataHash, err := hashRecordData(rec)
		if err != nil {
			return Receipt{}, fmt.Errorf("hash audit record %s: %w", rec.ID, err)
		}
		kind := string(rec.Kind)
		message := rec.Message
		if rec.ID == blocked.ID {
			kind = "state_not_allowed"
			message = "state_not_allowed: " + blockedError(blocked)
		}
		steps = append(steps, ChainStep{
			Sequence:   i + 1,
			Kind:       kind,
			ActorID:    rec.ActorID,
			OccurredAt: rec.CreatedAt.UTC(),
			Message:    message,
			DataHash:   dataHash,
		})
	}

	issuedAt := opts.IssuedAt
	if issuedAt.IsZero() {
		issuedAt = blocked.CreatedAt
	}
	issuedAt = issuedAt.UTC()

	actionName, _ := blocked.Data["action"].(string)

	// Primary: the decision record inside this blocked receipt's own
	// chain (trace-scoped, not the whole store — scanning all records
	// was a latent G2 cross-trace bug). Fallback: resolve by
	// (entity, action, outcome) when the blocked trace carries no
	// decision record of its own.
	proposalID := proposalIDFromRecords(chainRecords)
	if proposalID == "" {
		proposalID = resolveProposalID(ctx, audit, blocked.EntityID, actionName, "blocked", blocked.CreatedAt)
	}
	return Receipt{
		SchemaVersion:   SchemaVersion,
		TraceID:         opts.TraceID,
		ProposalID:      proposalID,
		TenantID:        opts.TenantID,
		EntityID:        blocked.EntityID,
		EntityType:      blocked.EntityType,
		ActionName:      actionName,
		Outcome:         "blocked",
		IssuedAt:        issuedAt,
		CausalChain:     steps,
		Attestation:     nil,
		RiskScore:       nil,
		ReplayLink:      opts.ReplayLink,
		ComplianceMap:   append([]ComplianceMapping(nil), opts.ComplianceMap...),
		InsurerEnvelope: nil,
	}, nil
}

// findExecution finds the single KindActionExecuted record in
// the trace. Returns ErrNoActionExecuted or
// ErrMultipleActionsExecuted on the boundary cases.
func findExecution(records []kiffaudit.Record) (kiffaudit.Record, error) {
	var found kiffaudit.Record
	count := 0
	for _, r := range records {
		if r.Kind == kiffaudit.KindActionExecuted {
			found = r
			count++
		}
	}
	switch count {
	case 0:
		return kiffaudit.Record{}, ErrNoActionExecuted
	case 1:
		return found, nil
	default:
		return kiffaudit.Record{}, ErrMultipleActionsExecuted
	}
}

func findBlockedDecision(records []kiffaudit.Record, receiptTraceID string) (kiffaudit.Record, error) {
	for i := len(records) - 1; i >= 0; i-- {
		rec := records[i]
		if BlockedReceiptTraceID(rec) == receiptTraceID && isStateNotAllowedRecord(rec) {
			return rec, nil
		}
	}
	return kiffaudit.Record{}, ErrNoBlockedDecision
}

func recordsForBlockedReceipt(records []kiffaudit.Record, blocked kiffaudit.Record) []kiffaudit.Record {
	out := make([]kiffaudit.Record, 0, len(records))
	for _, rec := range records {
		if rec.TraceID == blocked.TraceID && rec.TraceID != "" {
			out = append(out, rec)
			continue
		}
		if rec.ID == blocked.ID {
			out = append(out, rec)
		}
	}
	return out
}

func isStateNotAllowedRecord(rec kiffaudit.Record) bool {
	if rec.Kind != kiffaudit.KindActionFailed {
		return false
	}
	return strings.Contains(strings.ToLower(blockedError(rec)), "action state not allowed")
}

func blockedError(rec kiffaudit.Record) string {
	if errText, _ := rec.Data["error"].(string); errText != "" {
		return errText
	}
	if rec.Message != "" {
		return rec.Message
	}
	return "action state not allowed"
}

// BlockedReceiptTraceID returns the cloud receipt id for a blocked
// action_failed audit record. It is intentionally distinct from the
// framework trace id because the allowed attempt and blocked retry can
// share one entity trace.
func BlockedReceiptTraceID(rec kiffaudit.Record) string {
	if rec.TraceID == "" {
		return "blocked-" + rec.ID
	}
	return rec.TraceID + "-blocked-" + rec.ID
}

// orderRecords sorts in place by CreatedAt then ID for ties. The
// audit store's Query already sorts by CreatedAt; the secondary
// ID sort is a defensive tiebreak so two records emitted in the
// same nanosecond produce a stable order across runs.
func orderRecords(records []kiffaudit.Record) {
	for i := 1; i < len(records); i++ {
		for j := i; j > 0; j-- {
			a := records[j-1]
			b := records[j]
			if a.CreatedAt.Before(b.CreatedAt) {
				break
			}
			if a.CreatedAt.Equal(b.CreatedAt) && a.ID <= b.ID {
				break
			}
			records[j-1], records[j] = records[j], records[j-1]
		}
	}
}

// hashRecordData returns a hex-encoded sha256 over the canonical
// JSON of the audit record's Data map. The map is encoded through
// the stdlib json package, which sorts keys; an empty/nil map
// hashes as the sha256 of "null".
//
// Hashing the Data map (rather than the whole audit record) keeps
// the chain step independent of cloud-side fields the framework
// might add to its records in the future without invalidating
// historical receipts.
func hashRecordData(rec kiffaudit.Record) (string, error) {
	encoded, err := json.Marshal(rec.Data)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}
