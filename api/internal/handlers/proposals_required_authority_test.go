package handlers

import (
	"context"
	"strings"
	"testing"

	"github.com/kiff/cloud/api/internal/domain"
	"github.com/kiff/cloud/api/internal/mandate"
)

// the issued-authority design. An action can declare that an issued mandate is a
// precondition rather than an optional cap.
//
// the mandates design made absence a no-op so a tenant's first mandate could not
// break a running integration. These pin the opt-out: where an action
// declares it requires authority, `not_covered` is the refusal, and it
// refuses under its own reason so a missing step is never diagnosed as
// an exhausted one.

func specRequiring(action string) domain.Spec {
	return domain.Spec{Actions: []domain.ActionSpec{
		{Name: "READ_ORDER"},
		{Name: action, RequiresAuthority: true},
	}}
}

// The rule. Nothing covers the subject, the action declares it needs
// authority, so the action is refused rather than waved through.
func TestRequiresAuthority_UncoveredIsRefused(t *testing.T) {
	stub := &stubMandates{decision: mandate.Decision{Outcome: mandate.OutcomeNotCovered}}
	h := &ProposalsHandler{mandates: stub, logger: testLogger(), tenants: noRuntime{}}

	resp, refused := h.mandateRefusal(context.Background(), identityForTest(),
		propForTest("refund-agent-eu"), specRequiring("REFUND_INVOICE"), "PAID")
	if !refused {
		t.Fatal("an action requiring issued authority must be refused when nothing covers it")
	}
	if got := resp.Reasons; len(got) != 1 || got[0] != "no_mandate_issued" {
		t.Errorf("reasons = %v, want [no_mandate_issued]", got)
	}
	for _, want := range []string{"refund-agent-eu", "REFUND_INVOICE", "issue an agent card"} {
		if !strings.Contains(resp.Message, want) {
			t.Errorf("message must name the holder, the action and the next step; missing %q: %s", want, resp.Message)
		}
	}
}

// The reason is distinct from every ceiling reason. An operator told
// "limit reached" goes looking for a limit to raise; there isn't one.
func TestRequiresAuthority_ReasonIsNotACeilingReason(t *testing.T) {
	stub := &stubMandates{decision: mandate.Decision{Outcome: mandate.OutcomeNotCovered}}
	h := &ProposalsHandler{mandates: stub, logger: testLogger(), tenants: noRuntime{}}

	resp, _ := h.mandateRefusal(context.Background(), identityForTest(),
		propForTest("agent-1"), specRequiring("REFUND_INVOICE"), "PAID")
	for _, forbidden := range []string{"mandate_limit_reached", "mandate_not_covered", "mandate_expired"} {
		for _, got := range resp.Reasons {
			if got == forbidden {
				t.Errorf("reason %q collapses a missing card into an exhausted one", got)
			}
		}
	}
}

// An action that does not declare the dependency keeps the mandates design
// behaviour exactly. Adding the field changes nothing until it is set.
func TestRequiresAuthority_UndeclaredActionIsUnaffected(t *testing.T) {
	stub := &stubMandates{decision: mandate.Decision{Outcome: mandate.OutcomeNotCovered}}
	h := &ProposalsHandler{mandates: stub, logger: testLogger(), tenants: noRuntime{}}

	if _, refused := h.mandateRefusal(context.Background(), identityForTest(),
		propForTest("agent-1"), domain.Spec{Actions: []domain.ActionSpec{{Name: "REFUND_INVOICE"}}}, "PAID"); refused {
		t.Error("an action that declares no authority requirement must keep the additive no-op")
	}
}

// A covered subject inside its ceiling still runs. Requiring authority
// is a precondition, not a second refusal.
func TestRequiresAuthority_CoveredAndWithinLimitAllows(t *testing.T) {
	stub := &stubMandates{decision: mandate.Decision{Outcome: mandate.OutcomeWithinLimit}}
	h := &ProposalsHandler{mandates: stub, logger: testLogger(), tenants: noRuntime{}}

	if _, refused := h.mandateRefusal(context.Background(), keyBoundTo("agent-1"),
		propForTest("agent-1"), specRequiring("REFUND_INVOICE"), "PAID"); refused {
		t.Error("an issued card with room left must allow")
	}
}

// A card that exists and is exhausted keeps its own reason: the holder
// had authority and spent it, which is a different fact from never
// having been issued any.
func TestRequiresAuthority_ExhaustedKeepsTheCeilingReason(t *testing.T) {
	stub := &stubMandates{decision: mandate.Decision{
		Outcome: mandate.OutcomeLimitReached, MandateID: "mdt-1", Reason: "no room",
	}}
	h := &ProposalsHandler{mandates: stub, logger: testLogger(), tenants: noRuntime{}}

	resp, refused := h.mandateRefusal(context.Background(), keyBoundTo("agent-1"),
		propForTest("agent-1"), specRequiring("REFUND_INVOICE"), "PAID")
	if !refused {
		t.Fatal("an exhausted card must refuse")
	}
	if got := resp.Reasons[0]; got != "mandate_limit_reached" {
		t.Errorf("reason = %q, want mandate_limit_reached", got)
	}
}

// No actor means no holder, so no card can cover it. For an action that
// requires authority that is fatal, not the mandates design no-op — otherwise
// omitting the actor is a way around the whole rule.
func TestRequiresAuthority_ActorlessProposalIsRefused(t *testing.T) {
	stub := &stubMandates{decision: mandate.Decision{Outcome: mandate.OutcomeNotCovered}}
	h := &ProposalsHandler{mandates: stub, logger: testLogger(), tenants: noRuntime{}}

	resp, refused := h.mandateRefusal(context.Background(), identityForTest(),
		propForTest(""), specRequiring("REFUND_INVOICE"), "PAID")
	if !refused {
		t.Fatal("an actorless proposal must be refused when the action requires issued authority")
	}
	if resp.Reasons[0] != "no_mandate_issued" {
		t.Errorf("reason = %q, want no_mandate_issued", resp.Reasons[0])
	}
	if stub.calls != 0 {
		t.Error("the authorizer must not be consulted for a proposal with no holder")
	}
}

// The refusal message has to survive a reader holding the statement
// next to it.
//
// The engine refuses when used + draw > limit, so a card can be refused
// while it still holds authority: 3,000 used of 5,000, an action
// drawing 2,500, and 2,000 still there for something smaller. A message
// saying the agent has no authority left contradicts the balance on the
// screen — and the evidence runbook's rule is to stop the moment two
// surfaces disagree, so this would halt a production run rather than
// merely read badly.
func TestCeilingMessageDoesNotClaimTheCardIsEmpty(t *testing.T) {
	stub := &stubMandates{decision: mandate.Decision{
		Outcome:   mandate.OutcomeLimitReached,
		MandateID: "refund-agent-card",
		Quantity:  mandate.Quantity{Kind: mandate.KindSum, Parameter: "amount_cents"},
		Limit:     5000,
		Used:      3000,
		Reason:    "sum(amount_cents) limit 5000 per calendar_day reached: 3000 used, this action draws 2500",
	}}
	h := &ProposalsHandler{mandates: stub, logger: testLogger(), tenants: noRuntime{}}

	resp, refused := h.mandateRefusal(context.Background(), keyBoundTo("agent-1"),
		propForTest("agent-1"), specRequiring("REFUND_INVOICE"), "PAID")
	if !refused {
		t.Fatal("the action must be refused")
	}
	if strings.Contains(resp.Message, "no authority left") {
		t.Errorf("message claims the card is empty while 2000 remains: %q", resp.Message)
	}
	if !strings.Contains(resp.Message, "exceeds the remaining authority") {
		t.Errorf("message does not say what actually happened: %q", resp.Message)
	}
	// The engine's own figures travel with it, so a reader can
	// reconcile the refusal against the statement without guessing.
	for _, want := range []string{"refund-agent-card", "3000 used", "draws 2500"} {
		if !strings.Contains(resp.Message, want) {
			t.Errorf("message is missing %q: %q", want, resp.Message)
		}
	}
}
