package receipt_test

import (
	"context"
	"testing"
	"time"

	kiffaudit "github.com/kiff/kiff/pkg/kiff/audit"

	"github.com/kiff/cloud/api/internal/receipt"
)

// proposalIDFromRecords reads data.proposal_id from whichever record in
// the trace carries it. Today the only producer is the cloud's
// proposal_decision audit record (written with TraceID = prop.ID); the
// framework's action_validated/action_executed records do NOT carry it.
// So this only finds a proposal id when the receipt's trace includes
// the decision record — i.e. when the execution trace equals prop.ID.
// True trace-independent linkage (carrying proposal_id onto a record
// Build reads regardless of trace) shipped in G2b: see
// TestBuild_ResolvesProposalIDFromDecisionOnDifferentTrace. This test
// covers the in-trace primary path, which still wins when the decision
// record shares the execution trace.
func TestBuild_StampsProposalIDFromDecisionRecord(t *testing.T) {
	t.Parallel()
	traceID := "trace-pl-1"
	records := fixtureRefundTrace(traceID)
	// Faithful to production: proposal_id lives on a proposal_decision
	// record (the cloud gate writes it with TraceID = prop.ID), present
	// in this trace only because trace == prop.ID here.
	records = append(records, kiffaudit.Record{
		ID:         "rec-decision",
		Kind:       kiffaudit.Kind("proposal_decision"),
		EntityID:   "order-001",
		EntityType: "Order",
		Message:    "proposal decision recorded",
		Data:       map[string]any{"proposal_id": "prop-pl-1", "action": "MARK_PAID", "outcome": "allowed"},
		TraceID:    traceID,
		CreatedAt:  time.Date(2026, time.May, 24, 12, 0, 0, 0, time.UTC),
	})
	store := newAuditStoreWith(t, records)

	r, err := receipt.Build(context.Background(), store, receipt.BuildOptions{
		TenantID: "ten", TraceID: traceID,
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if r.ProposalID != "prop-pl-1" {
		t.Errorf("ProposalID: got %q, want prop-pl-1", r.ProposalID)
	}
}

// When neither the execution trace nor the tenant audit store holds a
// matching proposal_decision record, the receipt stamps an empty
// ProposalID. After G2b Build falls back to a by-entity/action
// resolve when the execution trace itself carries no decision record,
// so an empty result now means there is genuinely no decision to link —
// not merely that the decision lives on a different trace. fixtureRefundTrace
// emits only framework records, so the store has no decision at all here.
func TestBuild_ExecutionTraceWithoutDecisionRecordHasNoProposalID(t *testing.T) {
	t.Parallel()
	traceID := "trace-pl-2"
	// fixtureRefundTrace has only framework records (event/state/
	// validated/executed) — none carry proposal_id, matching a real
	// execution trace.
	store := newAuditStoreWith(t, fixtureRefundTrace(traceID))
	r, err := receipt.Build(context.Background(), store, receipt.BuildOptions{TenantID: "ten", TraceID: traceID})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if r.ProposalID != "" {
		t.Errorf("ProposalID: got %q, want empty (no decision record in the execution trace)", r.ProposalID)
	}
}

// MemoryStore.GetByProposalID resolves the receipt by its explicit link, tenant-scoped, and 404s an empty/unknown proposal id.
func TestMemoryStore_GetByProposalID(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := receipt.NewMemoryStore()
	rec := receipt.Receipt{
		SchemaVersion: receipt.SchemaVersion,
		TraceID:       "tr-1", ProposalID: "prop-1", TenantID: "ten",
		EntityID: "o1", EntityType: "Order", ActionName: "REFUND", Outcome: "allowed",
		IssuedAt:    time.Now().UTC(),
		CausalChain: []receipt.ChainStep{{Sequence: 1, Kind: "action_executed", DataHash: "h"}},
	}
	if err := store.Save(ctx, rec); err != nil {
		t.Fatalf("save: %v", err)
	}

	got, err := store.GetByProposalID(ctx, "ten", "prop-1")
	if err != nil {
		t.Fatalf("GetByProposalID: %v", err)
	}
	if got.TraceID != "tr-1" {
		t.Errorf("got trace %q, want tr-1", got.TraceID)
	}
	// Wrong tenant -> not found.
	if _, err := store.GetByProposalID(ctx, "other", "prop-1"); err != receipt.ErrReceiptNotFound {
		t.Errorf("cross-tenant: got %v, want ErrReceiptNotFound", err)
	}
	// Empty / unknown -> not found.
	if _, err := store.GetByProposalID(ctx, "ten", ""); err != receipt.ErrReceiptNotFound {
		t.Errorf("empty proposal id: got %v, want ErrReceiptNotFound", err)
	}
	if _, err := store.GetByProposalID(ctx, "ten", "nope"); err != receipt.ErrReceiptNotFound {
		t.Errorf("unknown proposal id: got %v, want ErrReceiptNotFound", err)
	}
}

// TestBuild_ResolvesProposalIDFromDecisionOnDifferentTrace is the
// acceptance case: in production the proposal gate writes the
// decision under TraceID = prop.ID, while the action executes under a
// different framework trace. The decision record is therefore absent
// from the execution trace, so the in-trace primary returns empty and
// Build must fall back to resolving the link by (entity, action,
// outcome, time) against the tenant audit store.
func TestBuild_ResolvesProposalIDFromDecisionOnDifferentTrace(t *testing.T) {
	t.Parallel()

	// Execution lives on its own trace (diverged from prop.ID).
	execTrace := "exec-trace-42"
	records := fixtureRefundTrace(execTrace)

	// Decision recorded earlier, under the proposal's own trace
	// (prop-9), same entity/action, outcome allowed, no later than the
	// execution it authorised.
	t0 := time.Date(2026, time.May, 24, 12, 0, 0, 0, time.UTC)
	records = append(records, kiffaudit.Record{
		ID:         "rec-decision-prop9",
		Kind:       kiffaudit.Kind("proposal_decision"),
		EntityID:   "order-001",
		EntityType: "Order",
		Message:    "proposal decision recorded",
		Data:       map[string]any{"proposal_id": "prop-9", "action": "MARK_PAID", "outcome": "allowed"},
		TraceID:    "prop-9",
		CreatedAt:  t0,
	})
	store := newAuditStoreWith(t, records)

	r, err := receipt.Build(context.Background(), store, receipt.BuildOptions{
		TenantID: "ten", TraceID: execTrace,
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if r.ProposalID != "prop-9" {
		t.Errorf("ProposalID: got %q, want prop-9 (resolved from decision on a different trace)", r.ProposalID)
	}
	// The receipt is still built from the execution trace.
	if r.TraceID != execTrace {
		t.Errorf("TraceID: got %q, want %q", r.TraceID, execTrace)
	}
}

// TestBuild_ResolveIgnoresMismatchedDecisions guards the resolver's
// match predicate: a decision for a different action, a different
// outcome, or one recorded after the execution must not be linked.
func TestBuild_ResolveIgnoresMismatchedDecisions(t *testing.T) {
	t.Parallel()

	execTrace := "exec-trace-mismatch"
	records := fixtureRefundTrace(execTrace)
	t0 := time.Date(2026, time.May, 24, 12, 0, 0, 0, time.UTC)
	execAt := t0.Add(3 * time.Millisecond) // matches fixture's action_executed

	records = append(records,
		// Wrong action.
		kiffaudit.Record{
			ID: "d-action", Kind: kiffaudit.Kind("proposal_decision"),
			EntityID: "order-001", EntityType: "Order",
			Data:    map[string]any{"proposal_id": "prop-wrong-action", "action": "REFUND", "outcome": "allowed"},
			TraceID: "prop-a", CreatedAt: t0,
		},
		// Wrong outcome.
		kiffaudit.Record{
			ID: "d-outcome", Kind: kiffaudit.Kind("proposal_decision"),
			EntityID: "order-001", EntityType: "Order",
			Data:    map[string]any{"proposal_id": "prop-wrong-outcome", "action": "MARK_PAID", "outcome": "blocked"},
			TraceID: "prop-b", CreatedAt: t0,
		},
		// Recorded after the execution.
		kiffaudit.Record{
			ID: "d-late", Kind: kiffaudit.Kind("proposal_decision"),
			EntityID: "order-001", EntityType: "Order",
			Data:    map[string]any{"proposal_id": "prop-too-late", "action": "MARK_PAID", "outcome": "allowed"},
			TraceID: "prop-c", CreatedAt: execAt.Add(1 * time.Millisecond),
		},
	)
	store := newAuditStoreWith(t, records)

	r, err := receipt.Build(context.Background(), store, receipt.BuildOptions{
		TenantID: "ten", TraceID: execTrace,
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if r.ProposalID != "" {
		t.Errorf("ProposalID: got %q, want empty (no decision matches action+outcome+time)", r.ProposalID)
	}
}
