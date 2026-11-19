package handlers

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/domain"
	"github.com/kiff/cloud/api/internal/executors"
	"github.com/kiff/cloud/api/internal/mandate"
	"github.com/kiff/cloud/api/internal/templates"
)

// The card demo claims two specific refusals. If the runtime spells
// either of them differently the demo reports itself as contradicted,
// and the reader is told the product did not do what it says.
//
// The web runner's own test stubs the gate, so it can only prove the
// script agrees with the stub — it would pass just as happily on a code
// the runtime never emits, which is exactly what happened:
// mandate_exceeded looked plausible and does not exist. This asserts
// the script against the decide path's real mapping instead.
func TestCardDemoExpectsTheReasonsTheRuntimeEmits(t *testing.T) {
	t.Parallel()

	tmpl, err := templates.Get("card-refund")
	if err != nil {
		t.Fatalf("load template: %v", err)
	}
	var script struct {
		Steps []struct {
			Kind         string `json:"kind"`
			ActionName   string `json:"action_name"`
			Expect       string `json:"expect"`
			ExpectReason string `json:"expect_reason"`
		} `json:"steps"`
	}
	if err := json.Unmarshal(tmpl.DecisionScript, &script); err != nil {
		t.Fatalf("parse script: %v", err)
	}

	// What the decide path actually produces, for the two states the
	// script walks through: nothing issued, and issued but spent.
	produced := map[mandate.Outcome]string{}
	for _, outcome := range []mandate.Outcome{mandate.OutcomeNotCovered, mandate.OutcomeLimitReached} {
		stub := &stubMandates{decision: mandate.Decision{Outcome: outcome, MandateID: "mdt-1"}}
		h := &ProposalsHandler{mandates: stub, logger: testLogger(), tenants: noRuntime{}}
		// The fixture's own action, since what is under test is the
		// outcome-to-reason mapping rather than any action name.
		// The demo issues a card, so it runs in the reader's own
		// workspace on their signed-in session, not a runtime key.
		session := auth.Identity{Subject: "user_owner", Tenant: "tenant-a", Method: auth.MethodSession}
		resp, refused := h.mandateRefusal(context.Background(), session,
			propForTest("agent-1"), specRequiring("REFUND_INVOICE"), "PAID")
		if !refused {
			t.Fatalf("%s did not refuse", outcome)
		}
		produced[outcome] = resp.Reasons[0]
	}

	var declared []string
	for _, step := range script.Steps {
		if step.Kind == "attempt" && step.Expect == "blocked" {
			if step.ExpectReason == "" {
				t.Fatalf("attempt %q expects a refusal without naming it", step.ActionName)
			}
			declared = append(declared, step.ExpectReason)
		}
	}
	if len(declared) != 2 {
		t.Fatalf("the script declares %d refusal reasons, want the two the demo tells", len(declared))
	}

	// In order: refused for want of a card, then refused at the ceiling.
	if want := produced[mandate.OutcomeNotCovered]; declared[0] != want {
		t.Errorf("the demo expects %q for an unissued card; the runtime emits %q", declared[0], want)
	}
	if want := produced[mandate.OutcomeLimitReached]; declared[1] != want {
		t.Errorf("the demo expects %q at the ceiling; the runtime emits %q", declared[1], want)
	}
}

// And the domain the demo runs on has to declare the action that makes
// the first refusal possible, or the script's opening step would be
// allowed and the demo would open on the wrong story.
func TestCardDemoDomainRequiresAuthority(t *testing.T) {
	t.Parallel()

	tmpl, err := templates.Get("card-refund")
	if err != nil {
		t.Fatalf("load template: %v", err)
	}
	parsed, err := domain.ParseAndBuild(tmpl.YAML, executors.DefaultRegistry())
	if err != nil {
		t.Fatalf("parse domain: %v", err)
	}
	var found bool
	for _, a := range parsed.Spec.Actions {
		if a.Name == "REFUND_ORDER" {
			found = a.RequiresAuthority
		}
	}
	if !found {
		t.Error("REFUND_ORDER does not declare requires_authority; the demo's first refusal would not happen")
	}
}
