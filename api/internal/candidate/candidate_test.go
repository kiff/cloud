package candidate

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/kiff/cloud/api/internal/guard"
)

// fixtureA loads the refund-ceiling-guard connected-runtime fixture.
func fixtureA(t *testing.T) Fixture {
	t.Helper()
	f, err := LoadFixture(filepath.Join("testdata", "refund_ceiling_guard.json"))
	if err != nil {
		t.Fatalf("load fixture: %v", err)
	}
	return f
}

// Acceptance #1: kiff-cloud can derive at least one candidate control from
// a connected guard runtime.
func TestDerive_FixtureA_ProducesCandidate(t *testing.T) {
	t.Parallel()
	f := fixtureA(t)

	controls := DeriveFromRuntime(f.Connection, f.Observation)
	if len(controls) != 1 {
		t.Fatalf("got %d candidates, want 1", len(controls))
	}
	c := controls[0]

	if c.Tool != "issue_refund" {
		t.Errorf("tool = %q, want issue_refund", c.Tool)
	}
	if c.Action != "ISSUE_REFUND" {
		t.Errorf("action = %q, want ISSUE_REFUND", c.Action)
	}
	if c.EntityType != "Order" {
		t.Errorf("entity_type = %q, want Order", c.EntityType)
	}
	if c.EntityArg != "order_id" {
		t.Errorf("entity_arg = %q, want order_id", c.EntityArg)
	}
	if c.Risk != "high" {
		t.Errorf("risk = %q, want high (refund is a money-moving verb)", c.Risk)
	}
	if len(c.RiskReasons) == 0 {
		t.Error("risk_reasons empty; Path A must surface the mechanical signal it matched")
	}
}

// Acceptance #2: the candidate cites the kiff-guard runtime as its source,
// not an uploaded tools/list manifest. The binding (adapter/workflow/mode)
// must trace back to the connection.
func TestDerive_FixtureA_CitesRuntimeSource(t *testing.T) {
	t.Parallel()
	f := fixtureA(t)
	c := DeriveFromRuntime(f.Connection, f.Observation)[0]

	if c.Evidence.Source != SourceRuntime {
		t.Errorf("evidence.source = %q, want %q", c.Evidence.Source, SourceRuntime)
	}
	if c.Evidence.Tool != "issue_refund" {
		t.Errorf("evidence.tool = %q, want issue_refund", c.Evidence.Tool)
	}
	if c.Evidence.Adapter != "langgraph" {
		t.Errorf("evidence.adapter = %q, want langgraph", c.Evidence.Adapter)
	}
	if c.Evidence.Workflow != "refund-ceiling" {
		t.Errorf("evidence.workflow = %q, want refund-ceiling", c.Evidence.Workflow)
	}
	if c.Evidence.Mode != "observe" {
		t.Errorf("evidence.mode = %q, want observe (observe/shadow-first)", c.Evidence.Mode)
	}
}

// Acceptance #3: no invented thresholds or states. They must be JSON null,
// not fabricated values.
func TestDerive_FixtureA_NoInventedThresholdsOrStates(t *testing.T) {
	t.Parallel()
	f := fixtureA(t)
	c := DeriveFromRuntime(f.Connection, f.Observation)[0]

	if c.PolicyThresholds != nil {
		t.Error("policy_thresholds must be nil — KIFF never derives thresholds")
	}
	if c.States != nil {
		t.Error("states must be nil — KIFF never derives the state machine")
	}

	// Confirm they actually marshal to null in the JSON a reviewer sees.
	raw, err := json.Marshal(c)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var generic map[string]json.RawMessage
	if err := json.Unmarshal(raw, &generic); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if string(generic["policy_thresholds"]) != "null" {
		t.Errorf("policy_thresholds JSON = %s, want null", generic["policy_thresholds"])
	}
	if string(generic["states"]) != "null" {
		t.Errorf("states JSON = %s, want null", generic["states"])
	}
}

// Fields come from the parameter schema, with required mirroring the
// schema's required set exactly.
func TestDerive_FixtureA_FieldsFromSchema(t *testing.T) {
	t.Parallel()
	f := fixtureA(t)
	c := DeriveFromRuntime(f.Connection, f.Observation)[0]

	want := map[string]Field{
		"order_id":     {Name: "order_id", Type: "string", Required: true},
		"amount_cents": {Name: "amount_cents", Type: "integer", Required: true},
	}
	if len(c.Fields) != len(want) {
		t.Fatalf("got %d fields, want %d: %+v", len(c.Fields), len(want), c.Fields)
	}
	for _, f := range c.Fields {
		w, ok := want[f.Name]
		if !ok {
			t.Errorf("unexpected field %q", f.Name)
			continue
		}
		if f != w {
			t.Errorf("field %q = %+v, want %+v", f.Name, f, w)
		}
	}
}

// Acceptance #5 (grounding): the derived candidate passes every grounding
// check against the runtime observation.
func TestValidate_FixtureA_GroundingPasses(t *testing.T) {
	t.Parallel()
	f := fixtureA(t)
	controls := DeriveFromRuntime(f.Connection, f.Observation)

	report := Validate(f.Observation, controls)
	if !report.OK {
		for _, c := range report.Checks {
			if !c.Pass {
				t.Errorf("grounding check %q failed: %s", c.Name, c.Detail)
			}
		}
	}
}

// The validator must actually catch ungrounded candidates — a phantom tool,
// an invented required field, and a fabricated threshold all fail.
func TestValidate_CatchesUngroundedCandidates(t *testing.T) {
	t.Parallel()
	f := fixtureA(t)

	// Phantom tool not present in the observation.
	bad := []Control{{
		Tool:     "delete_everything",
		Action:   "DELETE_EVERYTHING",
		Evidence: Evidence{Source: SourceRuntime, Tool: "delete_everything"},
	}}
	if Validate(f.Observation, bad).OK {
		t.Error("validator passed a phantom tool; should fail no_phantom_tools")
	}

	// Real tool but an invented required field + a fabricated threshold.
	thr := json.RawMessage(`{"max_cents": 5000}`)
	bad2 := []Control{{
		Tool:   "issue_refund",
		Action: "ISSUE_REFUND",
		Fields: []Field{
			{Name: "order_id", Type: "string", Required: true},
			{Name: "amount_cents", Type: "integer", Required: true},
			{Name: "secret_backdoor", Type: "string", Required: true},
		},
		Evidence:         Evidence{Source: SourceRuntime, Tool: "issue_refund"},
		PolicyThresholds: &thr,
	}}
	rep := Validate(f.Observation, bad2)
	if rep.OK {
		t.Error("validator passed an invented field + fabricated threshold; should fail")
	}
	assertFailed(t, rep, "no_phantom_fields")
	assertFailed(t, rep, "thresholds_null")
}

// The validator must reject a candidate that re-points the governance seam
// away from the observed binding — the core of the runtime-first proof.
// A real tool, valid fields, valid evidence, but a mutated
// action / entity_type / entity_arg must each fail the seam check.
func TestValidate_RejectsMutatedGovernanceSeam(t *testing.T) {
	t.Parallel()
	f := fixtureA(t)
	base := DeriveFromRuntime(f.Connection, f.Observation)[0]

	cases := []struct {
		name   string
		mutate func(c *Control)
	}{
		{"mutated action", func(c *Control) { c.Action = "DELETE_ORDER" }},
		{"mutated entity_type", func(c *Control) { c.EntityType = "Customer" }},
		{"mutated entity_arg", func(c *Control) { c.EntityArg = "customer_id" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := base // copy
			tc.mutate(&c)
			rep := Validate(f.Observation, []Control{c})
			if rep.OK {
				t.Errorf("validator passed a %s; should fail governance_seam_matches", tc.name)
			}
			assertFailed(t, rep, "governance_seam_matches")
		})
	}
}

func assertFailed(t *testing.T, rep Report, check string) {
	t.Helper()
	for _, c := range rep.Checks {
		if c.Name == check {
			if c.Pass {
				t.Errorf("expected check %q to fail, but it passed", check)
			}
			return
		}
	}
	t.Errorf("check %q not found in report", check)
}

// Guard against drift: the fixture must stay a single-tool observation
// (the Fixture A anchor). Multi-tool ranking is Fixture B / follow-up.
func TestFixtureA_IsSingleTool(t *testing.T) {
	t.Parallel()
	f := fixtureA(t)
	if len(f.Observation.Tools) != 1 {
		t.Fatalf("Fixture A should have exactly 1 tool, got %d", len(f.Observation.Tools))
	}
	if f.Connection.Mode != guard.ModeObserve {
		t.Errorf("Fixture A mode = %q, want observe", f.Connection.Mode)
	}
}
