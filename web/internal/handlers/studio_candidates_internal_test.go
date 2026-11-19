package handlers

import (
	"strings"
	"testing"

	studio "github.com/kiff/kiff-studio"

	"github.com/kiff/cloud/web/internal/apiclient"
)

// candidateBlueprint maps a runtime-grounded candidate to a draft blueprint:
// the observed action surface, no fabricated lifecycle, no thresholds.
func TestCandidateBlueprint_GroundedNoInvention(t *testing.T) {
	t.Parallel()
	rt := apiclient.GuardRuntimeRef{
		AgentID: "ap-agent", Adapter: "openclaw", Project: "cookbook",
		Environment: "aws", Workflow: "duplicate-payment", Mode: "enforce",
	}
	c := apiclient.GuardCandidate{
		Tool: "pay_invoice", Action: "PAY_INVOICE", EntityType: "Invoice", EntityArg: "invoice_id",
		Fields: []apiclient.GuardCandidateField{
			{Name: "invoice_id", Type: "string", Required: false},
			{Name: "amount_cents", Type: "integer", Required: true},
		},
		Risk: "high",
	}

	bp := candidateBlueprint(rt, c)

	if bp.Entity != "Invoice" {
		t.Errorf("entity = %q, want Invoice", bp.Entity)
	}
	if len(bp.Actions) != 1 || bp.Actions[0].Name != "PAY_INVOICE" {
		t.Fatalf("actions = %+v, want one PAY_INVOICE", bp.Actions)
	}
	a := bp.Actions[0]
	// Required params: the required field, minus the entity-id arg.
	if len(a.RequiredParameters) != 1 || a.RequiredParameters[0] != "amount_cents" {
		t.Errorf("required = %v, want [amount_cents] (entity arg excluded)", a.RequiredParameters)
	}
	if a.Risk != "high" {
		t.Errorf("risk = %q, want high (passed through from the API)", a.Risk)
	}
	// No fabricated lifecycle.
	if len(bp.States) != 0 {
		t.Errorf("states = %v, want none (KIFF never invents the lifecycle)", bp.States)
	}
	if len(a.AllowedStates) != 0 {
		t.Errorf("allowed_states = %v, want none (user maps the lifecycle)", a.AllowedStates)
	}
	if a.ApprovalRequired {
		t.Error("approval must not be fabricated as required")
	}
}

// An unbound observe-mode tool (no action/entity binding) still derives a
// grounded blueprint: action from the tool name, entity left as a neutral
// structural placeholder — never a fabricated business entity.
func TestCandidateBlueprint_UnboundTool(t *testing.T) {
	t.Parallel()
	c := apiclient.GuardCandidate{Tool: "escalate_ticket", Risk: "medium"}
	bp := candidateBlueprint(apiclient.GuardRuntimeRef{Workflow: "observe-quickstart"}, c)

	if bp.Actions[0].Name != "ESCALATE_TICKET" {
		t.Errorf("action = %q, want ESCALATE_TICKET (derived from tool name)", bp.Actions[0].Name)
	}
	if bp.Entity != "Entity" {
		t.Errorf("entity = %q, want neutral placeholder Entity", bp.Entity)
	}
}

// The fallback YAML is non-empty, parseable-shaped, and carries no
// fabricated thresholds/states.
func TestCandidateDraftFallbackYAML_Shape(t *testing.T) {
	t.Parallel()
	rt := apiclient.GuardRuntimeRef{Workflow: "duplicate-payment"}
	c := apiclient.GuardCandidate{
		Tool: "pay_invoice", Action: "PAY_INVOICE", EntityType: "Invoice", EntityArg: "invoice_id",
		Fields: []apiclient.GuardCandidateField{{Name: "amount_cents", Required: true}},
		Risk:   "high",
	}
	yaml := candidateDraftFallbackYAML(rt, c)
	for _, want := range []string{"domain:", "entity: Invoice", "PAY_INVOICE", "amount_cents", "risk: high", "states: []"} {
		if !strings.Contains(yaml, want) {
			t.Errorf("fallback yaml missing %q; got:\n%s", want, yaml)
		}
	}
}

// candidateFromToolCall parses the reference shape and produces a
// grounded candidate: every provided field becomes a proposed required
// field; nothing invented beyond a transparent risk heuristic.
func TestCandidateFromToolCall_ReferenceShape(t *testing.T) {
	t.Parallel()
	raw := `{"tool":"issue_refund","action":"ISSUE_REFUND","entity_type":"refund","fields":{"amount":4900,"reason":"x"}}`
	rt, c, err := candidateFromToolCall(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if c.Tool != "issue_refund" || c.Action != "ISSUE_REFUND" || c.EntityType != "refund" {
		t.Fatalf("parsed candidate mismatch: %+v", c)
	}
	if c.Risk != "high" {
		t.Errorf("risk = %q, want high (refund signal)", c.Risk)
	}
	if len(c.Fields) != 2 {
		t.Fatalf("fields = %d, want 2", len(c.Fields))
	}
	for _, f := range c.Fields {
		if !f.Required {
			t.Errorf("field %q should be a proposed required field", f.Name)
		}
	}
	// Through the shared seam, the derived blueprint carries both fields.
	bp := candidateBlueprint(rt, c)
	yaml, err := studio.BuildYAML(bp)
	if err != nil {
		yaml = []byte(candidateDraftFallbackYAML(rt, c))
	}
	for _, want := range []string{"ISSUE_REFUND", "amount", "reason"} {
		if !strings.Contains(string(yaml), want) {
			t.Errorf("derived yaml missing %q", want)
		}
	}
}

func TestCandidateFromToolCall_RejectsBad(t *testing.T) {
	t.Parallel()
	if _, _, err := candidateFromToolCall("not json"); err == nil {
		t.Error("expected error on non-JSON")
	}
	if _, _, err := candidateFromToolCall(`{"action":"X"}`); err == nil {
		t.Error("expected error when tool is missing")
	}
}
