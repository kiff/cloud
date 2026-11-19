package handlers_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	kiffadapter "github.com/kiff/kiff/pkg/kiff/adapter"
	kiffevent "github.com/kiff/kiff/pkg/kiff/event"
)

// / the authenticated-surface design guarantee #1: a recorded governance decision
// carries the entity's reconstructed state at decision time and the
// action's allowed states as explicit audit fields, so the evidence
// surface can render "decided against reconstructed state X" without
// string-parsing reasons or error text.

// containsString reports membership in a []any decoded from JSONB or a
// []string, so the assertion is robust to how the audit store typed it.
func containsString(v any, want string) bool {
	switch xs := v.(type) {
	case []string:
		for _, s := range xs {
			if s == want {
				return true
			}
		}
	case []any:
		for _, s := range xs {
			if str, ok := s.(string); ok && str == want {
				return true
			}
		}
	}
	return false
}

// An allowed decision records the state it was decided against
// (PAID) and the action's allowed states.
func TestProposals_StateAtDecision_AllowedRecordsState(t *testing.T) {
	t.Parallel()
	wrapped, rt, tenantID := envDecideSetup(t, "")

	rec := drivePost(t, wrapped, "/v1/proposals/decide", map[string]any{
		"entity_id": "o-env", "entity_type": "Order", "action_name": "GATED_REFUND",
		"actor_id":   "agent",
		"parameters": map[string]any{"amount": 100, "reason": "x"},
	})
	assertOutcome(t, rec, http.StatusOK, "allowed", nil)

	found := decisionRecordFor(t, rt, tenantID)
	if found == nil {
		t.Fatal("no proposal_decision audit record")
	}
	if found.Data["state_at_decision"] != "PAID" {
		t.Errorf("state_at_decision = %v, want PAID", found.Data["state_at_decision"])
	}
	if !containsString(found.Data["allowed_states"], "PAID") {
		t.Errorf("allowed_states = %v, want to contain PAID", found.Data["allowed_states"])
	}
}

// A blocked-on-state decision records the refusing state: the entity is
// in CREATED, the action is allowed only from PAID, so the record shows
// state_at_decision=CREATED + allowed_states=[PAID] — the legible
// "refused because already/not yet in the right state" case.
func TestProposals_StateAtDecision_BlockedRecordsRefusingState(t *testing.T) {
	t.Parallel()
	wrapped, rt, tenantID := envDecideSetup(t, "")

	// Fresh entity advanced only to CREATED (ORDER_PLACED), not PAID.
	resolved, err := rt.For(context.Background(), tenantID)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	raw := kiffadapter.RawInput{
		ID: "ORDER_PLACED-created", Adapter: "cloud", Type: "ORDER_PLACED", Source: "test",
		ReceivedAt: time.Now().UTC(), EntityID: "o-created", EntityType: "Order",
		ActorID: "a", Metadata: kiffevent.Metadata{TraceID: "t-created"},
		Payload: map[string]any{},
	}
	if _, err := resolved.IngestRaw(context.Background(), raw); err != nil {
		t.Fatalf("ingest ORDER_PLACED: %v", err)
	}

	rec := drivePost(t, wrapped, "/v1/proposals/decide", map[string]any{
		"entity_id": "o-created", "entity_type": "Order", "action_name": "GATED_REFUND",
		"actor_id":   "agent",
		"parameters": map[string]any{"amount": 100, "reason": "x"},
	})
	assertOutcome(t, rec, http.StatusOK, "blocked", []string{"state_not_allowed"})

	found := decisionRecordFor(t, rt, tenantID)
	if found == nil {
		t.Fatal("no proposal_decision audit record")
	}
	if found.Data["state_at_decision"] != "CREATED" {
		t.Errorf("state_at_decision = %v, want CREATED (the refusing state)", found.Data["state_at_decision"])
	}
	if !containsString(found.Data["allowed_states"], "PAID") {
		t.Errorf("allowed_states = %v, want to contain PAID", found.Data["allowed_states"])
	}
}
