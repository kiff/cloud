package candidate

import (
	"path/filepath"
	"testing"
)

// fixtureB loads the observe-quickstart multi-tool connected-runtime fixture.
func fixtureB(t *testing.T) Fixture {
	t.Helper()
	f, err := LoadFixture(filepath.Join("testdata", "observe_quickstart.json"))
	if err != nil {
		t.Fatalf("load fixture B: %v", err)
	}
	return f
}

// Acceptance #1 + #2: Fixture B has multiple grounded tools, and the
// candidate list includes all of them with no phantom tools.
func TestDerive_FixtureB_AllToolsNoPhantoms(t *testing.T) {
	t.Parallel()
	f := fixtureB(t)

	if len(f.Observation.Tools) < 2 {
		t.Fatalf("Fixture B should be multi-tool, got %d", len(f.Observation.Tools))
	}

	controls := DeriveFromRuntime(f.Connection, f.Observation)
	if len(controls) != len(f.Observation.Tools) {
		t.Fatalf("got %d candidates, want one per observed tool (%d)", len(controls), len(f.Observation.Tools))
	}

	observed := map[string]bool{}
	for _, tool := range f.Observation.Tools {
		observed[tool.Name] = true
	}
	for _, c := range controls {
		if !observed[c.Tool] {
			t.Errorf("candidate %q is a phantom — not in the observation", c.Tool)
		}
		if c.Evidence.Source != SourceRuntime {
			t.Errorf("candidate %q evidence source = %q, want %q", c.Tool, c.Evidence.Source, SourceRuntime)
		}
	}
}

// Acceptance #3: deterministic Path A ranking puts higher-risk tools above
// lower-risk ones. The risk-rank sequence must be non-increasing, and the
// money-moving refund tool must outrank the comms / escalation tools.
func TestDerive_FixtureB_RankedByRisk(t *testing.T) {
	t.Parallel()
	f := fixtureB(t)
	controls := DeriveFromRuntime(f.Connection, f.Observation)

	// Non-increasing risk order.
	for i := 1; i < len(controls); i++ {
		if RiskRank(controls[i-1].Risk) < RiskRank(controls[i].Risk) {
			t.Errorf("ranking not non-increasing: %s(%s) before %s(%s)",
				controls[i-1].Tool, controls[i-1].Risk, controls[i].Tool, controls[i].Risk)
		}
	}

	// refund_order is the highest-risk tool and must rank first.
	if controls[0].Tool != "refund_order" {
		t.Errorf("candidates[0] = %q, want refund_order (highest risk)", controls[0].Tool)
	}
	if controls[0].Risk != "high" {
		t.Errorf("refund_order risk = %q, want high", controls[0].Risk)
	}

	// Every refund (high) tool must outrank every send/escalate tool.
	riskByTool := map[string]string{}
	for _, c := range controls {
		riskByTool[c.Tool] = c.Risk
	}
	if RiskRank(riskByTool["refund_order"]) <= RiskRank(riskByTool["send_email"]) {
		t.Errorf("refund_order (%s) should outrank send_email (%s)", riskByTool["refund_order"], riskByTool["send_email"])
	}
	if RiskRank(riskByTool["refund_order"]) <= RiskRank(riskByTool["escalate_ticket"]) {
		t.Errorf("refund_order (%s) should outrank escalate_ticket (%s)", riskByTool["refund_order"], riskByTool["escalate_ticket"])
	}
}

// Acceptance #4: the grounding validator still passes for the multi-tool
// fixture (no phantom tools/fields, required fidelity, thresholds/states
// null, evidence + governance seam grounded).
func TestValidate_FixtureB_GroundingPasses(t *testing.T) {
	t.Parallel()
	f := fixtureB(t)
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

// Thresholds and states stay null across every candidate (no invention),
// and observe-mode tools (no ToolMap) keep empty entity bindings rather
// than fabricated ones.
func TestDerive_FixtureB_NoInventionUnboundEntities(t *testing.T) {
	t.Parallel()
	f := fixtureB(t)
	controls := DeriveFromRuntime(f.Connection, f.Observation)

	for _, c := range controls {
		if c.PolicyThresholds != nil || c.States != nil {
			t.Errorf("candidate %q invented policy/state", c.Tool)
		}
		// observe-quickstart has no ToolMap, so entity binding is unknown.
		// The action is derived from the tool name; entity stays empty —
		// never fabricated.
		if c.EntityType != "" || c.EntityArg != "" {
			t.Errorf("candidate %q has fabricated entity binding (type=%q arg=%q); observe mode has no ToolMap",
				c.Tool, c.EntityType, c.EntityArg)
		}
		if c.Action == "" {
			t.Errorf("candidate %q has empty action; should derive from tool name", c.Tool)
		}
	}
}
