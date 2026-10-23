package receipt_test

import (
	"context"
	"errors"
	"testing"
	"time"

	kiffaudit "github.com/kiff/kiff/pkg/kiff/audit"

	"github.com/kiff/cloud/api/internal/receipt"
)

// decisionTrace is the audit shape the decide path writes for one
// proposal: a proposal_decision record under TraceID == proposalID
// (mirrors handlers.recordOutcome / attributionData).
func decisionRecord(proposalID, outcome string, at time.Time) kiffaudit.Record {
	return kiffaudit.Record{
		ID:         "dec-" + proposalID + "-" + outcome,
		Kind:       kiffaudit.Kind("proposal_decision"),
		EntityID:   "cust-001",
		EntityType: "Customer",
		ActorID:    "customer-ops-team",
		TraceID:    proposalID,
		CreatedAt:  at,
		Message:    "proposal decision recorded",
		Data: map[string]any{
			"proposal_id":       proposalID,
			"action":            "CONTACT_CUSTOMER",
			"outcome":           outcome,
			"reasons":           []string{},
			"proposed_actor_id": "customer-ops-team",
			"principal_subject": "key-1",
			"control_id":        "ctl-abc",
			"environment":       "aws",
			"domain_id":         "refunds",
		},
	}
}

// TestBuildDecision_NoDomainID confirms a decision record without a
// domain_id (built before stamping) yields an empty Receipt
// DomainID rather than an error — back-compat for legacy decisions.
func TestBuildDecision_NoDomainID(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, time.June, 14, 12, 0, 0, 0, time.UTC)
	rec := kiffaudit.Record{
		ID:         "dec-legacy-allowed",
		Kind:       kiffaudit.Kind("proposal_decision"),
		EntityID:   "cust-001",
		EntityType: "Customer",
		TraceID:    "prop-legacy",
		CreatedAt:  at,
		Data: map[string]any{
			"proposal_id": "prop-legacy",
			"action":      "CONTACT_CUSTOMER",
			"outcome":     "allowed",
		},
	}
	store := newAuditStoreWith(t, []kiffaudit.Record{rec})
	r, err := receipt.BuildDecision(context.Background(), store, "ten-demo", "prop-legacy")
	if err != nil {
		t.Fatalf("BuildDecision: %v", err)
	}
	if r.DomainID != "" {
		t.Errorf("domain_id = %q, want empty for a legacy decision", r.DomainID)
	}
}

func TestBuildDecision_AllowedProducesReceipt(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, time.June, 14, 12, 0, 0, 0, time.UTC)
	store := newAuditStoreWith(t, []kiffaudit.Record{decisionRecord("prop-1", "allowed", at)})

	r, err := receipt.BuildDecision(context.Background(), store, "ten-demo", "prop-1")
	if err != nil {
		t.Fatalf("BuildDecision: %v", err)
	}
	if r.TraceID != receipt.DecisionReceiptTraceID("prop-1") {
		t.Errorf("trace_id = %q, want %q", r.TraceID, receipt.DecisionReceiptTraceID("prop-1"))
	}
	if r.ProposalID != "prop-1" {
		t.Errorf("proposal_id = %q, want prop-1", r.ProposalID)
	}
	if r.Outcome != "allowed" {
		t.Errorf("outcome = %q, want allowed", r.Outcome)
	}
	if r.ActionName != "CONTACT_CUSTOMER" {
		t.Errorf("action = %q, want CONTACT_CUSTOMER", r.ActionName)
	}
	if r.EntityID != "cust-001" || r.EntityType != "Customer" {
		t.Errorf("entity mismatch: %s/%s", r.EntityType, r.EntityID)
	}
	if r.TenantID != "ten-demo" {
		t.Errorf("tenant = %q, want ten-demo", r.TenantID)
	}
	if r.DomainID != "refunds" {
		t.Errorf("domain_id = %q, want refunds (stamped from the decision record)", r.DomainID)
	}
	if len(r.CausalChain) != 1 || r.CausalChain[0].Kind != "proposal_decision" {
		t.Fatalf("chain = %+v, want one proposal_decision step", r.CausalChain)
	}
	if r.CausalChain[0].DataHash == "" {
		t.Error("chain step missing data hash")
	}
	if r.IssuedAt != at {
		t.Errorf("issued_at = %v, want %v", r.IssuedAt, at)
	}
	// Unproven fields ship nil (signed later by the anchor loop).
	if r.Attestation != nil || r.RiskScore != nil || r.InsurerEnvelope != nil {
		t.Error("decision receipt must ship with nil attestation/risk/insurer at build time")
	}
	// The receipt must hash (it gets signed downstream).
	if _, err := r.CausalChainHash(); err != nil {
		t.Errorf("CausalChainHash: %v", err)
	}
}

func TestBuildDecision_BlockedOutcome(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, time.June, 14, 12, 5, 0, 0, time.UTC)
	store := newAuditStoreWith(t, []kiffaudit.Record{decisionRecord("prop-blocked", "blocked", at)})

	r, err := receipt.BuildDecision(context.Background(), store, "ten-demo", "prop-blocked")
	if err != nil {
		t.Fatalf("BuildDecision: %v", err)
	}
	if r.Outcome != "blocked" {
		t.Errorf("outcome = %q, want blocked", r.Outcome)
	}
}

// When a proposal recorded more than one decision row under its trace,
// the terminal (latest) decision is the one the receipt attests.
func TestBuildDecision_UsesTerminalDecision(t *testing.T) {
	t.Parallel()
	t0 := time.Date(2026, time.June, 14, 12, 0, 0, 0, time.UTC)
	store := newAuditStoreWith(t, []kiffaudit.Record{
		decisionRecord("prop-2", "approval_required", t0),
		decisionRecord("prop-2", "allowed", t0.Add(time.Second)),
	})

	r, err := receipt.BuildDecision(context.Background(), store, "ten-demo", "prop-2")
	if err != nil {
		t.Fatalf("BuildDecision: %v", err)
	}
	if r.Outcome != "allowed" {
		t.Errorf("outcome = %q, want allowed (terminal decision)", r.Outcome)
	}
	if len(r.CausalChain) != 2 {
		t.Errorf("chain steps = %d, want 2 (both decision records)", len(r.CausalChain))
	}
}

func TestBuildDecision_NoDecisionRecord(t *testing.T) {
	t.Parallel()
	store := newAuditStoreWith(t, nil)
	if _, err := receipt.BuildDecision(context.Background(), store, "ten-demo", "prop-missing"); !errors.Is(err, receipt.ErrNoDecision) {
		t.Fatalf("err = %v, want ErrNoDecision", err)
	}
}

// A non-governance outcome (invalid / errored / malformed) must not produce
// a receipt, even if recorded under the proposal_decision kind. The builder
// gates on the outcome itself.
func TestBuildDecision_NonReceiptableOutcome(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, time.June, 14, 12, 0, 0, 0, time.UTC)
	for _, outcome := range []string{"invalid", "limit_exceeded", "errored", ""} {
		store := newAuditStoreWith(t, []kiffaudit.Record{decisionRecord("prop-x-"+outcome, outcome, at)})
		if _, err := receipt.BuildDecision(context.Background(), store, "ten-demo", "prop-x-"+outcome); !errors.Is(err, receipt.ErrOutcomeNotReceiptable) {
			t.Errorf("outcome %q: err = %v, want ErrOutcomeNotReceiptable", outcome, err)
		}
	}
}

func TestBuildDecision_RequiresTenantAndProposal(t *testing.T) {
	t.Parallel()
	store := newAuditStoreWith(t, nil)
	if _, err := receipt.BuildDecision(context.Background(), store, "", "prop-1"); !errors.Is(err, receipt.ErrTenantIDRequired) {
		t.Errorf("missing tenant: err = %v, want ErrTenantIDRequired", err)
	}
	if _, err := receipt.BuildDecision(context.Background(), store, "ten-demo", ""); !errors.Is(err, receipt.ErrProposalIDRequired) {
		t.Errorf("missing proposal: err = %v, want ErrProposalIDRequired", err)
	}
}

// A decision receipt id never collides with the raw execution trace, so a
// later execution receipt for the same proposal can coexist on a distinct
// (tenant_id, trace_id) key.
func TestDecisionReceiptTraceID_DistinctFromExecutionTrace(t *testing.T) {
	t.Parallel()
	if got := receipt.DecisionReceiptTraceID("prop-1"); got == "prop-1" {
		t.Errorf("decision receipt trace id must differ from the execution trace; got %q", got)
	}
}

func TestIsDecisionReceiptTrace(t *testing.T) {
	t.Parallel()
	if !receipt.IsDecisionReceiptTrace(receipt.DecisionReceiptTraceID("prop-1")) {
		t.Error("a DecisionReceiptTraceID must be recognized as a decision trace")
	}
	for _, notDecision := range []string{"prop-1", "trace-abc", "trace-abc-blocked-rec-1", ""} {
		if receipt.IsDecisionReceiptTrace(notDecision) {
			t.Errorf("%q must not be recognized as a decision trace", notDecision)
		}
	}
}

func TestDecisionOutcomeReceiptable(t *testing.T) {
	t.Parallel()
	cases := map[string]bool{
		"allowed":           true,
		"blocked":           true,
		"approval_required": true,
		"invalid":           false,
		"limit_exceeded":    false,
		"":                  false,
	}
	for outcome, want := range cases {
		if got := receipt.DecisionOutcomeReceiptable(outcome); got != want {
			t.Errorf("DecisionOutcomeReceiptable(%q) = %v, want %v", outcome, got, want)
		}
	}
}

// kiff/tafs#167: a decision's record of what its call moved and was on is
// in its chain step, so the signature covers the amount and the hash of
// the payment the owner's record names. A decision without them has no
// step data, as before.
func TestBuildDecision_StepCarriesAmountAndWhatItIsOn(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, time.October, 21, 0, 40, 0, 0, time.UTC)
	rec := decisionRecord("tc-1", "allowed", at)
	rec.Data["amount"] = map[string]any{"value": "150.00", "unit": "EUR"}
	rec.Data["on_arg"] = "charge"
	rec.Data["on_hash"] = "sha256:ab"
	plain := decisionRecord("tc-2", "allowed", at)
	store := newAuditStoreWith(t, []kiffaudit.Record{rec, plain})

	r, err := receipt.BuildDecision(context.Background(), store, "ten-demo", "tc-1")
	if err != nil {
		t.Fatal(err)
	}
	data := r.CausalChain[0].Data
	amount, _ := data["amount"].(map[string]any)
	if amount["value"] != "150.00" || amount["unit"] != "EUR" || data["on_arg"] != "charge" || data["on_hash"] != "sha256:ab" || len(data) != 3 {
		t.Fatalf("step data = %v", data)
	}
	signed, _ := r.CausalChainHash()
	r.CausalChain[0].Data["on_hash"] = "sha256:cd"
	if again, _ := r.CausalChainHash(); again == signed {
		t.Fatal("the chain hash does not cover the step's data")
	}

	r2, err := receipt.BuildDecision(context.Background(), store, "ten-demo", "tc-2")
	if err != nil {
		t.Fatal(err)
	}
	if r2.CausalChain[0].Data != nil {
		t.Fatalf("a decision without them got step data: %v", r2.CausalChain[0].Data)
	}
}
