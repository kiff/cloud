package domain_test

import (
	"context"
	"strings"
	"testing"

	studio "github.com/kiff/kiff-studio"
	kiffaction "github.com/kiff/kiff/pkg/kiff/action"

	"github.com/kiff/cloud/api/internal/domain"
	"github.com/kiff/cloud/api/internal/executors"
)

// canonicalRefundInput is the structured-form authoring shape
// the dashboard's guided builder submits for the AC's
// smoke-run input. Driving the same fixture through
// kiff-studio's deterministic generator + BuildYAML is the
// single source of truth for "what does the cloud's executor
// registry plus the framework's domain validator accept?"
//
// The shape mirrors the post-#203 canonical
// permission shape: paired proposing + .approve permissions,
// one tenant_owner role holding both. kiff-studio v0.1.0's
// generator emits exactly that shape.
func canonicalRefundInput() studio.BuilderInput {
	return studio.BuilderInput{
		WorkflowName: "refund-flow",
		EntityType:   "Order",
		States:       []string{"READY_FOR_REFUND", "REFUNDED"},
		Actions: []studio.ActionInput{{
			Name:               "ISSUE_REFUND",
			AllowedStates:      []string{"READY_FOR_REFUND"},
			RequiredParameters: []string{"amount", "reason"},
			ApprovalRequired:   true,
			Risk:               "high",
		}},
	}
}

// renderBuilderYAML drives the canonical input through the
// package's generator + YAML renderer, returning the bytes the
// framework's domain parser sees. Failures fail the calling
// test; on success, the returned bytes are guaranteed
// Validate-clean per the package's contract.
func renderBuilderYAML(t *testing.T) []byte {
	t.Helper()
	bp, err := studio.GenerateBlueprint(context.Background(), canonicalRefundInput())
	if err != nil {
		t.Fatalf("studio.GenerateBlueprint: %v", err)
	}
	yaml, err := studio.BuildYAML(bp)
	if err != nil {
		t.Fatalf("studio.BuildYAML: %v", err)
	}
	return yaml
}

// TestBuilderYAML_ParsesAgainstFramework asserts kiff-studio's
// canonical output validates against the framework's domain
// parser using the cloud's default executor registry. This is
// the AC's "validation failures should be impossible from
// valid form input" guarantee, expressed as a contract test:
// when the package's output drifts from what the framework
// accepts, the cloud's test suite breaks before the cloud
// pins to the new package version.
func TestBuilderYAML_ParsesAgainstFramework(t *testing.T) {
	t.Parallel()

	yaml := renderBuilderYAML(t)

	res, err := domain.ParseAndBuild(yaml, executors.DefaultRegistry())
	if err != nil {
		t.Fatalf("builder yaml must parse cleanly: %v\n--- yaml ---\n%s", err, string(yaml))
	}

	if res.Definition.Name != "refund-flow" {
		t.Errorf("domain name: got %q, want refund-flow", res.Definition.Name)
	}
	contract, ok := res.Definition.Actions.Get("ISSUE_REFUND")
	if !ok {
		t.Fatal("ISSUE_REFUND missing from parsed definition")
	}
	if contract.Risk != kiffaction.RiskHigh {
		t.Errorf("risk: got %q, want high", contract.Risk)
	}
	if contract.ApprovalRequirement != kiffaction.ApprovalRequired {
		t.Errorf("approval: got %q, want required", contract.ApprovalRequirement)
	}
	if contract.Executor == nil {
		t.Error("cloud.proxy executor was not bound; registry drift?")
	}
}

// TestBuilderYAML_NoApprovalLowRiskParses asserts the
// approval=never form path produces YAML the framework
// accepts. The kiff-studio renderer always emits the same
// transition + permission shape; the only difference at the
// YAML layer between approval-required and approval-never
// inputs is the risk + approval lines. String-replacing those
// keeps the test focused on the parser-side branch without
// re-rendering through GenerateBlueprint.
func TestBuilderYAML_NoApprovalLowRiskParses(t *testing.T) {
	t.Parallel()

	base := renderBuilderYAML(t)
	yaml := strings.NewReplacer(
		"approval: required", "approval: never",
		"risk: high", "risk: low",
	).Replace(string(base))

	res, err := domain.ParseAndBuild([]byte(yaml), executors.DefaultRegistry())
	if err != nil {
		t.Fatalf("builder yaml (low risk) must parse cleanly: %v", err)
	}
	contract, _ := res.Definition.Actions.Get("ISSUE_REFUND")
	if contract.ApprovalRequirement != kiffaction.ApprovalNever {
		t.Errorf("approval: got %q, want never", contract.ApprovalRequirement)
	}
	if contract.Risk != kiffaction.RiskLow {
		t.Errorf("risk: got %q, want low", contract.Risk)
	}
}
