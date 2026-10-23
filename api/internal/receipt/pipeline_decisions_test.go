package receipt_test

import (
	"context"
	"testing"
	"time"

	kiffaudit "github.com/kiff/kiff/pkg/kiff/audit"
	kiffruntime "github.com/kiff/kiff/pkg/kiff/runtime"

	"github.com/kiff/cloud/api/internal/receipt"
)

// proposalDecisionRecord mirrors what handlers.recordOutcome writes for a
// governed decision: a proposal_decision audit record under TraceID ==
// proposalID, carrying the proposal_id/action/outcome in Data.
func proposalDecisionRecord(proposalID, outcome string, at time.Time) kiffaudit.Record {
	return kiffaudit.Record{
		ID:         "dec-" + proposalID,
		Kind:       kiffaudit.Kind("proposal_decision"),
		EntityID:   "cust-001",
		EntityType: "Customer",
		ActorID:    "customer-ops-team",
		TraceID:    proposalID,
		CreatedAt:  at,
		Message:    "proposal decision recorded",
		Data: map[string]any{
			"proposal_id": proposalID,
			"action":      "CONTACT_CUSTOMER",
			"outcome":     outcome,
		},
	}
}

func TestReconcileDecisions_MintsDecisionReceipt(t *testing.T) {
	t.Parallel()
	pl, store, _ := newLocalPipeline(t)
	at := time.Date(2026, time.June, 14, 12, 0, 0, 0, time.UTC)
	rt := &kiffruntime.Runtime{Audit: newAuditStoreWith(t, []kiffaudit.Record{
		proposalDecisionRecord("prop-1", "allowed", at),
	})}

	built, err := pl.ReconcileDecisions(context.Background(), "ten-1", rt)
	if err != nil {
		t.Fatalf("ReconcileDecisions: %v", err)
	}
	if built != 1 {
		t.Fatalf("built = %d, want 1", built)
	}

	r, err := store.Get(context.Background(), "ten-1", receipt.DecisionReceiptTraceID("prop-1"))
	if err != nil {
		t.Fatalf("store.Get decision receipt: %v", err)
	}
	if r.ProposalID != "prop-1" || r.Outcome != "allowed" || r.ActionName != "CONTACT_CUSTOMER" {
		t.Fatalf("decision receipt mismatch: %+v", r)
	}
	if r.Attestation != nil {
		t.Errorf("reconcile must not attest; got %+v", r.Attestation)
	}
}

func TestReconcileDecisions_Idempotent(t *testing.T) {
	t.Parallel()
	pl, _, _ := newLocalPipeline(t)
	at := time.Date(2026, time.June, 14, 12, 0, 0, 0, time.UTC)
	rt := &kiffruntime.Runtime{Audit: newAuditStoreWith(t, []kiffaudit.Record{
		proposalDecisionRecord("prop-1", "blocked", at),
	})}

	if _, err := pl.ReconcileDecisions(context.Background(), "ten-1", rt); err != nil {
		t.Fatalf("first reconcile: %v", err)
	}
	built, err := pl.ReconcileDecisions(context.Background(), "ten-1", rt)
	if err != nil {
		t.Fatalf("second reconcile: %v", err)
	}
	if built != 0 {
		t.Errorf("second reconcile built = %d, want 0 (idempotent)", built)
	}
}

// Non-governance outcomes (invalid/limit_exceeded) never produce a
// decision receipt.
func TestReconcileDecisions_SkipsNonReceiptableOutcomes(t *testing.T) {
	t.Parallel()
	pl, store, _ := newLocalPipeline(t)
	at := time.Date(2026, time.June, 14, 12, 0, 0, 0, time.UTC)
	rt := &kiffruntime.Runtime{Audit: newAuditStoreWith(t, []kiffaudit.Record{
		proposalDecisionRecord("prop-invalid", "invalid", at),
		proposalDecisionRecord("prop-limit", "limit_exceeded", at),
	})}

	built, err := pl.ReconcileDecisions(context.Background(), "ten-1", rt)
	if err != nil {
		t.Fatalf("ReconcileDecisions: %v", err)
	}
	if built != 0 {
		t.Fatalf("built = %d, want 0", built)
	}
	if _, err := store.Get(context.Background(), "ten-1", receipt.DecisionReceiptTraceID("prop-invalid")); err == nil {
		t.Error("invalid outcome must not produce a decision receipt")
	}
}

// When an execution receipt already exists for the proposal, the richer
// execution receipt wins — no second (decision) receipt is minted.
func TestReconcileDecisions_SkipsWhenExecutionReceiptExists(t *testing.T) {
	t.Parallel()
	pl, store, _ := newLocalPipeline(t)
	at := time.Date(2026, time.June, 14, 12, 0, 0, 0, time.UTC)

	// Pre-seed an execution receipt linked to the same proposal id.
	if err := store.Save(context.Background(), receipt.Receipt{
		SchemaVersion: receipt.SchemaVersion,
		TraceID:       "exec-trace-1",
		ProposalID:    "prop-1",
		TenantID:      "ten-1",
		EntityID:      "cust-001",
		ActionName:    "CONTACT_CUSTOMER",
		Outcome:       "allowed",
		IssuedAt:      at,
	}); err != nil {
		t.Fatalf("seed execution receipt: %v", err)
	}

	rt := &kiffruntime.Runtime{Audit: newAuditStoreWith(t, []kiffaudit.Record{
		proposalDecisionRecord("prop-1", "allowed", at),
	})}

	built, err := pl.ReconcileDecisions(context.Background(), "ten-1", rt)
	if err != nil {
		t.Fatalf("ReconcileDecisions: %v", err)
	}
	if built != 0 {
		t.Errorf("built = %d, want 0 (execution receipt already exists)", built)
	}
	if _, err := store.Get(context.Background(), "ten-1", receipt.DecisionReceiptTraceID("prop-1")); err == nil {
		t.Error("must not mint a decision receipt when an execution receipt exists for the proposal")
	}
}

// A proposal with multiple decision rows (e.g. approval_required then a
// terminal blocked) mints exactly one decision receipt, and its outcome is
// the TERMINAL decision — proving BuildDecision (not the first-seen row) is
// the receiptability authority.
func TestReconcileDecisions_OneReceiptPerProposalTerminalOutcome(t *testing.T) {
	t.Parallel()
	pl, store, _ := newLocalPipeline(t)
	t0 := time.Date(2026, time.June, 14, 12, 0, 0, 0, time.UTC)
	rt := &kiffruntime.Runtime{Audit: newAuditStoreWith(t, []kiffaudit.Record{
		proposalDecisionRecord("prop-1", "approval_required", t0),
		{
			ID: "dec-prop-1-blocked", Kind: kiffaudit.Kind("proposal_decision"),
			EntityID: "cust-001", EntityType: "Customer", TraceID: "prop-1",
			CreatedAt: t0.Add(time.Second), Message: "proposal decision recorded",
			Data: map[string]any{"proposal_id": "prop-1", "action": "CONTACT_CUSTOMER", "outcome": "blocked"},
		},
	})}

	built, err := pl.ReconcileDecisions(context.Background(), "ten-1", rt)
	if err != nil {
		t.Fatalf("ReconcileDecisions: %v", err)
	}
	if built != 1 {
		t.Errorf("built = %d, want 1 (one receipt per proposal)", built)
	}
	r, err := store.Get(context.Background(), "ten-1", receipt.DecisionReceiptTraceID("prop-1"))
	if err != nil {
		t.Fatalf("store.Get: %v", err)
	}
	if r.Outcome != "blocked" {
		t.Errorf("outcome = %q, want blocked (terminal decision)", r.Outcome)
	}
}

// Reverse-order behavior (documents the narrowed dedup claim, point 1):
// when a decision receipt is minted FIRST and an execution receipt for the
// same proposal arrives LATER, both coexist as distinct artifacts linked by
// proposal_id (the store has no delete). The sweep does not mint a second
// DECISION receipt — it stays idempotent on its own trace id. Suppressing
// the now-redundant decision receipt is deferred to the Option B
// (execution-through-cloud) work.
func TestReconcileDecisions_LaterExecutionReceiptCoexists(t *testing.T) {
	t.Parallel()
	pl, store, _ := newLocalPipeline(t)
	at := time.Date(2026, time.June, 14, 12, 0, 0, 0, time.UTC)
	rt := &kiffruntime.Runtime{Audit: newAuditStoreWith(t, []kiffaudit.Record{
		proposalDecisionRecord("prop-1", "allowed", at),
	})}

	// 1) Decision receipt minted first.
	if _, err := pl.ReconcileDecisions(context.Background(), "ten-1", rt); err != nil {
		t.Fatalf("first reconcile: %v", err)
	}
	decisionTrace := receipt.DecisionReceiptTraceID("prop-1")
	if _, err := store.Get(context.Background(), "ten-1", decisionTrace); err != nil {
		t.Fatalf("decision receipt should exist: %v", err)
	}

	// 2) An execution receipt for the same proposal arrives later
	//    (distinct raw trace).
	if err := store.Save(context.Background(), receipt.Receipt{
		SchemaVersion: receipt.SchemaVersion, TraceID: "exec-trace-1", ProposalID: "prop-1",
		TenantID: "ten-1", EntityID: "cust-001", ActionName: "CONTACT_CUSTOMER",
		Outcome: "allowed", IssuedAt: at.Add(time.Minute),
	}); err != nil {
		t.Fatalf("save execution receipt: %v", err)
	}

	// 3) Re-running the sweep mints no NEW decision receipt (idempotent on
	//    its own trace); both artifacts coexist.
	built, err := pl.ReconcileDecisions(context.Background(), "ten-1", rt)
	if err != nil {
		t.Fatalf("second reconcile: %v", err)
	}
	if built != 0 {
		t.Errorf("second reconcile built = %d, want 0 (decision receipt already exists)", built)
	}
	if _, err := store.Get(context.Background(), "ten-1", decisionTrace); err != nil {
		t.Errorf("decision receipt must still exist: %v", err)
	}
	if _, err := store.Get(context.Background(), "ten-1", "exec-trace-1"); err != nil {
		t.Errorf("execution receipt must still exist: %v", err)
	}
}

func TestReconcileDecisions_RejectsNilRuntimeAndAudit(t *testing.T) {
	t.Parallel()
	pl, _, _ := newLocalPipeline(t)
	if _, err := pl.ReconcileDecisions(context.Background(), "ten-1", nil); err == nil {
		t.Error("nil runtime should error")
	}
	if _, err := pl.ReconcileDecisions(context.Background(), "ten-1", &kiffruntime.Runtime{}); err == nil {
		t.Error("runtime without audit should error")
	}
}
