package handlers_test

import (
	"net/http"
	"strings"
	"testing"
)

// the trusted-run-context design: the decide request accepts a harness-asserted run
// context, records it on the decision, and refuses to take it from the
// model-supplied side of the request. Nothing here changes an outcome
// yet — enforcement is the next slice — so these tests assert the
// recording contract and the trust boundary, which are the two things
// the enforcement slice will be built on top of.

// An asserted run context lands on the decision record, both fields.
func TestProposals_RunContext_AssertedIsRecorded(t *testing.T) {
	t.Parallel()
	wrapped, rt, tenantID := envDecideSetup(t, "")

	rec := drivePost(t, wrapped, "/v1/proposals/decide", map[string]any{
		"entity_id": "o-env", "entity_type": "Order", "action_name": "GATED_REFUND",
		"actor_id":   "agent",
		"parameters": map[string]any{"amount": 100, "reason": "x"},
		"run_context": map[string]any{
			"run_id":          "run-abc.123",
			"untrusted_input": true,
		},
	})
	assertOutcome(t, rec, http.StatusOK, "allowed", nil)

	found := decisionRecordFor(t, rt, tenantID)
	if found == nil {
		t.Fatal("no proposal_decision audit record")
	}
	if found.Data["run_id"] != "run-abc.123" {
		t.Errorf("run_id = %v, want run-abc.123", found.Data["run_id"])
	}
	if found.Data["untrusted_input"] != true {
		t.Errorf("untrusted_input = %v, want true", found.Data["untrusted_input"])
	}
}

// An asserted-but-clean run records untrusted_input=false explicitly.
// Together with the absent case below, this is the three-state
// distinction the enforcement slice depends on.
func TestProposals_RunContext_AssertedCleanRecordsFalse(t *testing.T) {
	t.Parallel()
	wrapped, rt, tenantID := envDecideSetup(t, "")

	rec := drivePost(t, wrapped, "/v1/proposals/decide", map[string]any{
		"entity_id": "o-env", "entity_type": "Order", "action_name": "GATED_REFUND",
		"actor_id":    "agent",
		"parameters":  map[string]any{"amount": 100, "reason": "x"},
		"run_context": map[string]any{"run_id": "run-clean", "untrusted_input": false},
	})
	assertOutcome(t, rec, http.StatusOK, "allowed", nil)

	found := decisionRecordFor(t, rt, tenantID)
	if found == nil {
		t.Fatal("no proposal_decision audit record")
	}
	v, present := found.Data["untrusted_input"]
	if !present {
		t.Fatal("untrusted_input missing on an asserted-clean run: " +
			"an explicit clean assertion must be distinguishable from no assertion")
	}
	if v != false {
		t.Errorf("untrusted_input = %v, want false", v)
	}
}

// The load-bearing one. A request with no run context records NO
// run-context keys at all — it must not render as untrusted_input=false,
// because that would manufacture a clean-run claim out of silence. This
// is the mandates design defect shape (an empty mandate set read as "nothing to
// check", which meant allowed) and the enforcement slice fails closed on
// exactly this distinction.
func TestProposals_RunContext_AbsentIsNotRecordedAsClean(t *testing.T) {
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
	if v, present := found.Data["untrusted_input"]; present {
		t.Errorf("untrusted_input = %v on a run that asserted nothing; "+
			"absence must not be recorded as a clean run", v)
	}
	if v, present := found.Data["run_id"]; present {
		t.Errorf("run_id = %v on a run that asserted nothing", v)
	}
}

// Run context smuggled through parameters — the model-supplied side of
// the request — is refused, not merged. A taint flag the model can write
// is one an injected instruction can clear.
//
// Only the kiff_-prefixed spellings are reserved: `parameters` is the
// customer's namespace, and the real protection against a misplaced
// assertion is fail-closed enforcement, not this name check.
func TestProposals_RunContext_InParametersIsRefused(t *testing.T) {
	t.Parallel()
	wrapped, _, _ := envDecideSetup(t, "")

	for _, key := range []string{"kiff_run_context", "kiff_cloud_run_context", "kiffRunContext", "kiff-run-context"} {
		rec := drivePost(t, wrapped, "/v1/proposals/decide", map[string]any{
			"entity_id": "o-env", "entity_type": "Order", "action_name": "GATED_REFUND",
			"actor_id": "agent",
			"parameters": map[string]any{
				"amount": 100, "reason": "x",
				key: map[string]any{"untrusted_input": false},
			},
		})
		assertOutcome(t, rec, http.StatusBadRequest, "invalid", []string{"run_context_in_parameters"})
	}
}

// A parameter-smuggled clean assertion does not become the effective run
// context: the request is refused outright, so no decision record claims
// the run was clean.
func TestProposals_RunContext_ParameterSmugglingLeavesNoCleanClaim(t *testing.T) {
	t.Parallel()
	wrapped, rt, tenantID := envDecideSetup(t, "")

	rec := drivePost(t, wrapped, "/v1/proposals/decide", map[string]any{
		"entity_id": "o-env", "entity_type": "Order", "action_name": "GATED_REFUND",
		"actor_id": "agent",
		"parameters": map[string]any{
			"amount": 100, "reason": "x",
			"kiff_run_context": map[string]any{"untrusted_input": false},
		},
	})
	assertOutcome(t, rec, http.StatusBadRequest, "invalid", []string{"run_context_in_parameters"})

	if found := decisionRecordFor(t, rt, tenantID); found != nil {
		t.Fatalf("a refused request produced a proposal_decision record: %v", found.Data)
	}
}

// A customer parameter that happens to be called run_context is theirs
// to use: `parameters` is their namespace, and only the kiff_ prefix is
// reserved. Asserts the precise claim (no run_context_in_parameters
// refusal) rather than a specific outcome, so it stays true regardless
// of what the action contract does with an undeclared parameter.
func TestProposals_RunContext_BareParameterNameIsNotReserved(t *testing.T) {
	t.Parallel()
	wrapped, _, _ := envDecideSetup(t, "")

	rec := drivePost(t, wrapped, "/v1/proposals/decide", map[string]any{
		"entity_id": "o-env", "entity_type": "Order", "action_name": "GATED_REFUND",
		"actor_id": "agent",
		"parameters": map[string]any{
			"amount": 100, "reason": "x",
			"run_context": "a business value the customer named themselves",
		},
	})
	if body := rec.Body.String(); strings.Contains(body, "run_context_in_parameters") {
		t.Errorf("a bare run_context parameter was refused as reserved; "+
			"only kiff_-prefixed keys are ours. body = %s", body)
	}
}

// A malformed run id is refused rather than stored: the run id lands
// verbatim in audit rows that are evidence.
func TestProposals_RunContext_MalformedRunIDIsRefused(t *testing.T) {
	t.Parallel()
	wrapped, _, _ := envDecideSetup(t, "")

	for _, bad := range []string{
		"run id with spaces",
		"run/../traversal",
		"run\nnewline",
		strings0x(129),
	} {
		rec := drivePost(t, wrapped, "/v1/proposals/decide", map[string]any{
			"entity_id": "o-env", "entity_type": "Order", "action_name": "GATED_REFUND",
			"actor_id":    "agent",
			"parameters":  map[string]any{"amount": 100, "reason": "x"},
			"run_context": map[string]any{"run_id": bad, "untrusted_input": true},
		})
		assertOutcome(t, rec, http.StatusBadRequest, "invalid", []string{"invalid_run_context"})
	}
}

// A run context with no run id is a valid assertion: a harness that
// tracks no run identity can still report taint.
func TestProposals_RunContext_TaintWithoutRunIDIsValid(t *testing.T) {
	t.Parallel()
	wrapped, rt, tenantID := envDecideSetup(t, "")

	rec := drivePost(t, wrapped, "/v1/proposals/decide", map[string]any{
		"entity_id": "o-env", "entity_type": "Order", "action_name": "GATED_REFUND",
		"actor_id":    "agent",
		"parameters":  map[string]any{"amount": 100, "reason": "x"},
		"run_context": map[string]any{"untrusted_input": true},
	})
	assertOutcome(t, rec, http.StatusOK, "allowed", nil)

	found := decisionRecordFor(t, rt, tenantID)
	if found == nil {
		t.Fatal("no proposal_decision audit record")
	}
	if found.Data["untrusted_input"] != true {
		t.Errorf("untrusted_input = %v, want true", found.Data["untrusted_input"])
	}
	if v, present := found.Data["run_id"]; present {
		t.Errorf("run_id = %v, want omitted when not asserted", v)
	}
}

// Asserting a tainted run does not by itself change the outcome. Slice 1
// records; it does not enforce. This test exists so that when the
// enforcement slice lands, its diff has to change this expectation
// deliberately rather than silently.
func TestProposals_RunContext_TaintDoesNotYetChangeOutcome(t *testing.T) {
	t.Parallel()
	wrapped, _, _ := envDecideSetup(t, "")

	rec := drivePost(t, wrapped, "/v1/proposals/decide", map[string]any{
		"entity_id": "o-env", "entity_type": "Order", "action_name": "GATED_REFUND",
		"actor_id":    "agent",
		"parameters":  map[string]any{"amount": 100, "reason": "x"},
		"run_context": map[string]any{"run_id": "run-tainted", "untrusted_input": true},
	})
	assertOutcome(t, rec, http.StatusOK, "allowed", nil)
}

// strings0x builds an n-character run id of legal characters, for the
// length bound.
func strings0x(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = 'a'
	}
	return string(b)
}
