package handlers

import (
	"strings"
	"testing"
	"time"

	studio "github.com/kiff/kiff-studio"

	"github.com/kiff/cloud/web/internal/apiclient"
)

func TestObservedRuntimeProofFromMatchingPaymentTool(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 6, 2, 10, 0, 0, 0, time.UTC)
	conn := apiclient.GuardConnection{
		Project:     "cookbook",
		Environment: "aws",
		AgentID:     "ap-agent",
		Workflow:    "duplicate-payment",
		Adapter:     "openclaw 0.1.0",
		LastSeenAt:  now.Add(-45 * time.Second),
	}
	obs := &apiclient.GuardObservation{
		Project:     "cookbook",
		Environment: "aws",
		AgentID:     "ap-agent",
		Workflow:    "duplicate-payment",
		Tools: []apiclient.GuardToolObservation{{
			Name:       "pay_invoice",
			Action:     "PAY_INVOICE",
			EntityType: "Invoice",
			EntityArg:  "invoice_id",
			Required:   []string{"amount_cents"},
		}},
	}
	bp := studio.Blueprint{
		Domain:  "duplicate-payment",
		Entity:  "Invoice",
		Actions: []studio.Action{{Name: "PAY_INVOICE"}},
	}

	proof := observedRuntimeProofFrom(bp, conn, obs, now)
	if proof == nil {
		t.Fatal("expected observed runtime proof")
	}
	if proof.ToolName != "pay_invoice" {
		t.Fatalf("ToolName = %q, want pay_invoice", proof.ToolName)
	}
	if proof.ActionName != "PAY_INVOICE" {
		t.Fatalf("ActionName = %q, want PAY_INVOICE", proof.ActionName)
	}
	if got := strings.Join(proof.RequiredParameters, ","); got != "amount_cents" {
		t.Fatalf("RequiredParameters = %q, want amount_cents", got)
	}
	if proof.LastSeenLabel != "just now" {
		t.Fatalf("LastSeenLabel = %q, want just now", proof.LastSeenLabel)
	}
}

func TestObservedRuntimeProofFromRejectsUnrelatedDraft(t *testing.T) {
	t.Parallel()

	conn := apiclient.GuardConnection{Workflow: "duplicate-payment"}
	obs := &apiclient.GuardObservation{
		Workflow: "duplicate-payment",
		Tools: []apiclient.GuardToolObservation{{
			Name:   "pay_invoice",
			Action: "PAY_INVOICE",
		}},
	}
	bp := studio.Blueprint{
		Domain:  "refund-flow",
		Entity:  "Order",
		Actions: []studio.Action{{Name: "ISSUE_REFUND"}},
	}

	if proof := observedRuntimeProofFrom(bp, conn, obs, time.Now()); proof != nil {
		t.Fatalf("proof = %#v, want nil", proof)
	}
}
