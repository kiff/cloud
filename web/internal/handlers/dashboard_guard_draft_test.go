package handlers

import (
	"strings"
	"testing"

	"github.com/kiff/cloud/web/internal/apiclient"
)

func TestGuardConnectionBlueprint_DuplicatePayment(t *testing.T) {
	t.Parallel()

	conn := apiclient.GuardConnection{
		Project:     "cookbook",
		Environment: "aws",
		AgentID:     "ap-agent",
		Workflow:    "duplicate-payment",
		Adapter:     "openclaw",
		Mode:        "enforce",
	}
	bp := guardConnectionBlueprint(conn, nil)
	if err := bp.Validate(); err != nil {
		t.Fatalf("blueprint Validate: %v", err)
	}
	if bp.Domain != "duplicate-payment" {
		t.Fatalf("Domain = %q, want duplicate-payment", bp.Domain)
	}
	if bp.Entity != "Invoice" {
		t.Fatalf("Entity = %q, want Invoice", bp.Entity)
	}
	if got := bp.Actions[0].Name; got != "PAY_INVOICE" {
		t.Fatalf("Action = %q, want PAY_INVOICE", got)
	}

	yaml, err := guardConnectionDraftYAML(conn, nil)
	if err != nil {
		t.Fatalf("guardConnectionDraftYAML: %v", err)
	}
	body := string(yaml)
	for _, want := range []string{
		"domain: duplicate-payment",
		"entity: Invoice",
		"name: PAY_INVOICE",
		"executor: cloud.proxy",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("draft yaml missing %q:\n%s", want, body)
		}
	}
}

func TestGuardConnectionBlueprint_GenericWorkflow(t *testing.T) {
	t.Parallel()

	conn := apiclient.GuardConnection{
		Project:     "ops",
		Environment: "prod",
		AgentID:     "ops-agent",
		Workflow:    "risk-review",
		Adapter:     "openclaw",
		Mode:        "observe",
	}
	bp := guardConnectionBlueprint(conn, nil)
	if err := bp.Validate(); err != nil {
		t.Fatalf("blueprint Validate: %v", err)
	}
	if bp.Domain != "risk-review" {
		t.Fatalf("Domain = %q, want risk-review", bp.Domain)
	}
	if bp.Entity != "RiskReviewRecord" {
		t.Fatalf("Entity = %q, want RiskReviewRecord", bp.Entity)
	}
	if got := bp.Actions[0].Name; got != "RUN_RISK_REVIEW" {
		t.Fatalf("Action = %q, want RUN_RISK_REVIEW", got)
	}
	if got := bp.Events[0]; got != "RISK_REVIEW_OPENED" {
		t.Fatalf("Events[0] = %q, want RISK_REVIEW_OPENED", got)
	}
}

func TestGuardConnectionBlueprint_ObservedToolCatalog(t *testing.T) {
	t.Parallel()

	conn := apiclient.GuardConnection{
		Project:     "cookbook",
		Environment: "aws",
		AgentID:     "ap-agent",
		Workflow:    "duplicate-payment",
		Adapter:     "openclaw",
		Mode:        "enforce",
	}
	obs := &apiclient.GuardObservation{
		Tools: []apiclient.GuardToolObservation{{
			Name:            "pay_invoice",
			Description:     "Pay an outstanding invoice",
			EntityArg:       "invoice_id",
			Action:          "PAY_INVOICE",
			EntityType:      "Invoice",
			ParameterSchema: []byte(`{"type":"object","required":["invoice_id","amount_cents"]}`),
		}},
	}
	bp := guardConnectionBlueprint(conn, obs)
	if err := bp.Validate(); err != nil {
		t.Fatalf("blueprint Validate: %v", err)
	}
	if len(bp.Actions) != 1 {
		t.Fatalf("Actions len = %d, want 1", len(bp.Actions))
	}
	action := bp.Actions[0]
	if action.Name != "PAY_INVOICE" {
		t.Fatalf("Action = %q, want PAY_INVOICE", action.Name)
	}
	if got := strings.Join(action.RequiredParameters, ","); got != "amount_cents" {
		t.Fatalf("RequiredParameters = %q, want amount_cents", got)
	}
	if bp.Entity != "Invoice" {
		t.Fatalf("Entity = %q, want Invoice", bp.Entity)
	}
}

func TestFindGuardConnectionRequiresExactRuntimeIdentity(t *testing.T) {
	t.Parallel()

	rows := []apiclient.GuardConnection{{
		Project:     "cookbook",
		Environment: "aws",
		AgentID:     "ap-agent",
		Workflow:    "duplicate-payment",
	}}
	if _, ok := findGuardConnection(rows, guardDraftRequest{
		Project:     "cookbook",
		Environment: "aws",
		AgentID:     "ap-agent",
		Workflow:    "duplicate-payment",
	}); !ok {
		t.Fatal("expected exact runtime match")
	}
	if _, ok := findGuardConnection(rows, guardDraftRequest{
		Project:     "cookbook",
		Environment: "prod",
		AgentID:     "ap-agent",
		Workflow:    "duplicate-payment",
	}); ok {
		t.Fatal("matched a different environment")
	}
}
