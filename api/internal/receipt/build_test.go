package receipt_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	kiffaudit "github.com/kiff/kiff/pkg/kiff/audit"

	"github.com/kiff/cloud/api/internal/receipt"
)

// fixtureRefundTrace is a hand-rolled audit timeline for one
// refund execution. The shape mirrors what the framework emits
// for an IngestEvent + ExecuteAction cycle on the demo refund
// domain: event ingested → state changed → action validated →
// action executed.
//
// Hand-rolling rather than driving a real runtime keeps slice 1's
// tests independent of the cloud's domain wiring; an end-to-end
// test will land in slice 2 once the build is wired into the
// runtime.
func fixtureRefundTrace(traceID string) []kiffaudit.Record {
	t0 := time.Date(2026, time.May, 24, 12, 0, 0, 0, time.UTC)
	return []kiffaudit.Record{
		{
			ID:         "rec-1",
			Kind:       kiffaudit.KindEventIngested,
			EntityID:   "order-001",
			EntityType: "Order",
			ActorID:    "agent-cs-bot",
			Message:    "event ingested",
			Data: map[string]any{
				"event_id":   "evt-001",
				"event_type": "ORDER_PLACED",
			},
			TraceID:   traceID,
			CreatedAt: t0,
		},
		{
			ID:         "rec-2",
			Kind:       kiffaudit.KindStateChanged,
			EntityID:   "order-001",
			EntityType: "Order",
			Message:    "state changed",
			Data: map[string]any{
				"from": "",
				"to":   "CREATED",
			},
			TraceID:   traceID,
			CreatedAt: t0.Add(1 * time.Millisecond),
		},
		{
			ID:         "rec-3",
			Kind:       kiffaudit.KindActionValidated,
			EntityID:   "order-001",
			EntityType: "Order",
			ActorID:    "agent-cs-bot",
			Message:    "action validated",
			Data: map[string]any{
				"action":            "MARK_PAID",
				"requires_approval": false,
			},
			TraceID:   traceID,
			CreatedAt: t0.Add(2 * time.Millisecond),
		},
		{
			ID:         "rec-4",
			Kind:       kiffaudit.KindActionExecuted,
			EntityID:   "order-001",
			EntityType: "Order",
			ActorID:    "agent-cs-bot",
			Message:    "action executed",
			Data: map[string]any{
				"action":   "MARK_PAID",
				"status":   "succeeded",
				"executed": true,
			},
			TraceID:   traceID,
			CreatedAt: t0.Add(3 * time.Millisecond),
		},
	}
}

func newAuditStoreWith(t *testing.T, records []kiffaudit.Record) *kiffaudit.InMemoryStore {
	t.Helper()
	store := kiffaudit.NewInMemoryStore()
	for _, r := range records {
		if err := store.Append(context.Background(), r); err != nil {
			t.Fatalf("append %s: %v", r.ID, err)
		}
	}
	return store
}

func TestBuild_ProducesReceiptWithFullChain(t *testing.T) {
	t.Parallel()

	traceID := "trace-refund-001"
	store := newAuditStoreWith(t, fixtureRefundTrace(traceID))

	r, err := receipt.Build(context.Background(), store, receipt.BuildOptions{
		TenantID: "ten-demo",
		TraceID:  traceID,
		ComplianceMap: []receipt.ComplianceMapping{
			{Framework: "SOC2", ControlID: "CC7.2", EvidenceKind: "system_monitoring"},
		},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	if r.SchemaVersion != receipt.SchemaVersion {
		t.Errorf("SchemaVersion: got %q, want %q", r.SchemaVersion, receipt.SchemaVersion)
	}
	if r.TenantID != "ten-demo" {
		t.Errorf("TenantID: got %q", r.TenantID)
	}
	if r.TraceID != traceID {
		t.Errorf("TraceID: got %q", r.TraceID)
	}
	if r.EntityID != "order-001" {
		t.Errorf("EntityID: got %q", r.EntityID)
	}
	if r.EntityType != "Order" {
		t.Errorf("EntityType: got %q", r.EntityType)
	}
	if r.ActionName != "MARK_PAID" {
		t.Errorf("ActionName: got %q, want %q", r.ActionName, "MARK_PAID")
	}
	if r.Outcome != "allowed" {
		t.Errorf("Outcome: got %q, want allowed", r.Outcome)
	}
	if got, want := len(r.CausalChain), 4; got != want {
		t.Fatalf("CausalChain length: got %d, want %d", got, want)
	}

	wantKinds := []string{"event_ingested", "state_changed", "action_validated", "action_executed"}
	for i, want := range wantKinds {
		if got := r.CausalChain[i].Kind; got != want {
			t.Errorf("CausalChain[%d].Kind: got %q, want %q", i, got, want)
		}
		if seq := r.CausalChain[i].Sequence; seq != i+1 {
			t.Errorf("CausalChain[%d].Sequence: got %d, want %d", i, seq, i+1)
		}
		if r.CausalChain[i].DataHash == "" {
			t.Errorf("CausalChain[%d].DataHash empty", i)
		}
	}

	// Fields whose value is unproven at v0.1 must ship as null.
	if r.Attestation != nil {
		t.Errorf("Attestation: got %+v, want nil", r.Attestation)
	}
	if r.RiskScore != nil {
		t.Errorf("RiskScore: got %+v, want nil", r.RiskScore)
	}
	if r.InsurerEnvelope != nil {
		t.Errorf("InsurerEnvelope: got %+v, want nil", r.InsurerEnvelope)
	}

	// IssuedAt should default to the most-recent record's time.
	wantIssued := time.Date(2026, time.May, 24, 12, 0, 0, 0, time.UTC).Add(3 * time.Millisecond)
	if !r.IssuedAt.Equal(wantIssued) {
		t.Errorf("IssuedAt: got %s, want %s", r.IssuedAt, wantIssued)
	}
}

func TestBuildBlocked_StateNotAllowedReceipt(t *testing.T) {
	t.Parallel()

	traceID := "trace-refund-001"
	records := fixtureRefundTrace(traceID)
	blocked := kiffaudit.Record{
		ID:         "rec-5",
		Kind:       kiffaudit.KindActionFailed,
		EntityID:   "order-001",
		EntityType: "Order",
		ActorID:    "agent-cs-bot",
		Message:    "action validation failed",
		Data: map[string]any{
			"action": "MARK_PAID",
			"error":  `action state not allowed: "PAID"`,
		},
		TraceID:   traceID,
		CreatedAt: records[len(records)-1].CreatedAt.Add(time.Millisecond),
	}
	records = append(records, blocked)
	store := newAuditStoreWith(t, records)

	r, err := receipt.BuildBlocked(context.Background(), store, receipt.BuildOptions{
		TenantID: "ten-demo",
		TraceID:  receipt.BlockedReceiptTraceID(blocked),
	})
	if err != nil {
		t.Fatalf("BuildBlocked: %v", err)
	}

	if r.Outcome != "blocked" {
		t.Errorf("Outcome: got %q, want blocked", r.Outcome)
	}
	if r.ActionName != "MARK_PAID" {
		t.Errorf("ActionName: got %q, want MARK_PAID", r.ActionName)
	}
	if r.EntityID != "order-001" || r.EntityType != "Order" {
		t.Errorf("entity: got %s/%s", r.EntityID, r.EntityType)
	}
	last := r.CausalChain[len(r.CausalChain)-1]
	if last.Kind != "state_not_allowed" {
		t.Errorf("blocked step kind: got %q, want state_not_allowed", last.Kind)
	}
	if !strings.Contains(last.Message, "state_not_allowed") {
		t.Errorf("blocked step message missing state_not_allowed: %q", last.Message)
	}
}

func TestBuild_CausalChainHashIsDeterministic(t *testing.T) {
	t.Parallel()

	// Build the same receipt twice from independent stores. The
	// hash must match — that's the property a receipt's signature
	// will rely on.
	traceID := "trace-refund-002"
	r1, err := receipt.Build(context.Background(),
		newAuditStoreWith(t, fixtureRefundTrace(traceID)),
		receipt.BuildOptions{TenantID: "ten-demo", TraceID: traceID})
	if err != nil {
		t.Fatalf("Build #1: %v", err)
	}
	r2, err := receipt.Build(context.Background(),
		newAuditStoreWith(t, fixtureRefundTrace(traceID)),
		receipt.BuildOptions{TenantID: "ten-demo", TraceID: traceID})
	if err != nil {
		t.Fatalf("Build #2: %v", err)
	}

	h1, err := r1.CausalChainHash()
	if err != nil {
		t.Fatalf("CausalChainHash #1: %v", err)
	}
	h2, err := r2.CausalChainHash()
	if err != nil {
		t.Fatalf("CausalChainHash #2: %v", err)
	}
	if h1 != h2 {
		t.Fatalf("CausalChainHash drift: %s vs %s", h1, h2)
	}
}

func TestBuild_ChainHashChangesWhenDataChanges(t *testing.T) {
	t.Parallel()

	// If the underlying audit data is tampered with — even a single
	// field — the chain hash must change. This is the property that
	// makes the receipt tamper-evident at the cloud layer.
	traceID := "trace-refund-003"
	original := fixtureRefundTrace(traceID)
	r1, err := receipt.Build(context.Background(),
		newAuditStoreWith(t, original),
		receipt.BuildOptions{TenantID: "ten-demo", TraceID: traceID})
	if err != nil {
		t.Fatalf("Build original: %v", err)
	}
	h1, _ := r1.CausalChainHash()

	tampered := fixtureRefundTrace(traceID)
	// Flip the action's success status. A motivated bad actor would
	// rewrite this to make a failed refund look like it succeeded.
	tampered[3].Data["status"] = "failed"
	r2, err := receipt.Build(context.Background(),
		newAuditStoreWith(t, tampered),
		receipt.BuildOptions{TenantID: "ten-demo", TraceID: traceID})
	if err != nil {
		t.Fatalf("Build tampered: %v", err)
	}
	h2, _ := r2.CausalChainHash()

	if h1 == h2 {
		t.Fatalf("hash did not change when audit data was tampered: %s", h1)
	}
}

func TestBuild_RejectsTraceWithoutExecution(t *testing.T) {
	t.Parallel()

	// A trace that ingested an event but never executed an action
	// (e.g. a validation-only run) cannot produce a receipt — the
	// receipt is fundamentally about an action that touched
	// production state.
	traceID := "trace-no-exec"
	records := fixtureRefundTrace(traceID)[:3] // drop the action_executed step
	store := newAuditStoreWith(t, records)

	_, err := receipt.Build(context.Background(), store, receipt.BuildOptions{
		TenantID: "ten-demo",
		TraceID:  traceID,
	})
	if !errors.Is(err, receipt.ErrNoActionExecuted) {
		t.Fatalf("Build error: got %v, want ErrNoActionExecuted", err)
	}
}

// Invariant: A trace containing more than one execution is refused
// rather than turned into a receipt. A receipt describes exactly one
// executed action or it is not issued.
// Category: evidence
// Attack: Feed the receipt builder a trace containing two executed actions
// Refused: The build is refused. A receipt describes exactly one executed
// action or it is not issued
func TestBuild_RejectsTraceWithMultipleExecutions(t *testing.T) {
	t.Parallel()

	traceID := "trace-multi-exec"
	records := fixtureRefundTrace(traceID)
	// Append a second action_executed under the same trace.
	dupe := records[3]
	dupe.ID = "rec-5"
	dupe.CreatedAt = dupe.CreatedAt.Add(time.Millisecond)
	records = append(records, dupe)

	_, err := receipt.Build(context.Background(),
		newAuditStoreWith(t, records),
		receipt.BuildOptions{TenantID: "ten-demo", TraceID: traceID})
	if !errors.Is(err, receipt.ErrMultipleActionsExecuted) {
		t.Fatalf("Build error: got %v, want ErrMultipleActionsExecuted", err)
	}
}

func TestBuild_RequiresTenantAndTrace(t *testing.T) {
	t.Parallel()

	store := newAuditStoreWith(t, fixtureRefundTrace("trace-x"))

	if _, err := receipt.Build(context.Background(), store, receipt.BuildOptions{TraceID: "trace-x"}); !errors.Is(err, receipt.ErrTenantIDRequired) {
		t.Errorf("missing tenant: got %v, want ErrTenantIDRequired", err)
	}
	if _, err := receipt.Build(context.Background(), store, receipt.BuildOptions{TenantID: "ten-demo"}); !errors.Is(err, receipt.ErrTraceIDRequired) {
		t.Errorf("missing trace: got %v, want ErrTraceIDRequired", err)
	}
}

func TestBuild_JSONSerializesNullsForUnpopulatedFields(t *testing.T) {
	t.Parallel()

	// The receipt's API/UI consumers expect JSON null (not absent
	// keys) for the deliberately-empty fields. Lock that in: the
	// difference between "we have no value" and "we forgot to add
	// the field" matters for downstream consumers.
	traceID := "trace-json-null"
	r, err := receipt.Build(context.Background(),
		newAuditStoreWith(t, fixtureRefundTrace(traceID)),
		receipt.BuildOptions{TenantID: "ten-demo", TraceID: traceID})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	encoded, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	mustContain := []string{
		`"attestation":null`,
		`"risk_score":null`,
		`"insurer_envelope":null`,
		`"schema_version":"v0.1"`,
		`"causal_chain":[`,
	}
	encodedStr := string(encoded)
	for _, want := range mustContain {
		if !contains(encodedStr, want) {
			t.Errorf("encoded receipt missing %q\nfull: %s", want, encodedStr)
		}
	}
}

func TestSortChain_StableBySequence(t *testing.T) {
	t.Parallel()

	steps := []receipt.ChainStep{
		{Sequence: 3, Kind: "action_executed"},
		{Sequence: 1, Kind: "event_ingested"},
		{Sequence: 2, Kind: "action_validated"},
	}
	receipt.SortChain(steps)
	for i, want := range []int{1, 2, 3} {
		if steps[i].Sequence != want {
			t.Errorf("steps[%d].Sequence: got %d, want %d", i, steps[i].Sequence, want)
		}
	}
}

// contains is a tiny helper to keep the test bodies readable
// without importing strings just for one call.
func contains(haystack, needle string) bool {
	if len(needle) == 0 {
		return true
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
