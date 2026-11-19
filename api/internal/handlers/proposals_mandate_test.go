package handlers

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/domain"
	kiffproposal "github.com/kiff/kiff/pkg/kiff/proposal"
	kiffruntime "github.com/kiff/kiff/pkg/kiff/runtime"

	"github.com/kiff/cloud/api/internal/mandate"
)

// numericParameters feeds the sum. A currency arriving as a JSON number
// must reach the ledger, or a limit silently never binds.
func TestNumericParametersProjectsJSONNumbers(t *testing.T) {
	got, uncountable := numericParameters(map[string]any{
		"amount_cents": float64(4290), // how JSON decodes
		"row_count":    int(12),
		"big":          int64(9007199254740993),
		"reason":       "customer complained",
		"nested":       map[string]any{"a": 1},
	})
	for k, want := range map[string]int64{"amount_cents": 4290, "row_count": 12, "big": 9007199254740993} {
		if got[k] != want {
			t.Errorf("%s = %d, want %d", k, got[k], want)
		}
	}
	// Non-numerics are absent from the sum and marked for a covering
	// card to refuse if it actually sums one.
	if _, ok := got["reason"]; ok {
		t.Error("a string parameter must not project into the sum")
	}
	if _, ok := got["nested"]; ok {
		t.Error("a nested object must not project into the sum")
	}
	if !uncountable["reason"] || !uncountable["nested"] {
		t.Errorf("uncountable = %v, want reason and nested", uncountable)
	}
	if empty, bad := numericParameters(nil); empty != nil || bad != nil {
		t.Error("no parameters should project to nil, not an empty map")
	}
}

func TestNumericParametersMarksUncountableAmounts(t *testing.T) {
	for name, value := range map[string]any{
		"numeric string": "100000",
		"fractional":     float64(0.5),
		"overflow":       float64(1e100),
		"null":           nil,
	} {
		t.Run(name, func(t *testing.T) {
			numeric, uncountable := numericParameters(map[string]any{"amount_cents": value})
			if _, ok := numeric["amount_cents"]; ok || !uncountable["amount_cents"] {
				t.Fatalf("numeric = %v, uncountable = %v", numeric, uncountable)
			}
		})
	}
}

func TestNumericParametersRefusesCaseVariantCollision(t *testing.T) {
	numeric, uncountable := numericParameters(map[string]any{
		"amount_cents": float64(100),
		"Amount_Cents": float64(1),
	})
	if _, ok := numeric["amount_cents"]; ok || !uncountable["amount_cents"] {
		t.Fatalf("numeric = %v, uncountable = %v", numeric, uncountable)
	}
}

type stubMandates struct {
	decision mandate.Decision
	err      error
	calls    int
	lastReq  mandate.Request
}

func (s *stubMandates) Authorize(_ context.Context, _, _ string, req mandate.Request) (mandate.Decision, error) {
	s.calls++
	s.lastReq = req
	return s.decision, s.err
}

// Covers mirrors the stubbed decision: a card covers the request unless
// the decision says nothing does.
func (s *stubMandates) Covers(_ context.Context, _ string, _ mandate.Request) (bool, error) {
	if s.err != nil {
		return false, s.err
	}
	return s.decision.Outcome != mandate.OutcomeNotCovered, nil
}

// The message a refusal carries has to be actionable: which mandate,
// and what the limit was.
func TestMandateMessageNamesTheMandateAndTheLimit(t *testing.T) {
	msg := mandateMessage(mandate.Decision{
		Outcome:   mandate.OutcomeLimitReached,
		MandateID: "mdt-refunds",
		Reason:    "sum(amount_cents) limit 500000 per calendar_day reached: 460000 used, this action draws 60000",
	})
	for _, want := range []string{"mdt-refunds", "limit 500000", "460000 used"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message missing %q: %s", want, msg)
		}
	}

	expired := mandateMessage(mandate.Decision{Outcome: mandate.OutcomeExpired})
	if !strings.Contains(expired, "revoked or outside its validity window") {
		t.Errorf("an expired mandate must say so plainly: %s", expired)
	}
}

// A deployment that issues no mandates must behave exactly as before.
func TestNilMandateAuthorizerIsANoOp(t *testing.T) {
	h := &ProposalsHandler{}
	if _, refused := h.mandateRefusal(context.Background(), identityForTest(), propForTest("agent-1"), domain.Spec{}, "PAID"); refused {
		t.Error("a handler with no mandate authorizer must refuse nothing")
	}
}

// A proposal with no actor cannot be bound to a mandate, because a
// mandate names a subject. It must not be refused for that.
func TestProposalWithoutAnActorIsNotRefused(t *testing.T) {
	stub := &stubMandates{decision: mandate.Decision{Outcome: mandate.OutcomeLimitReached}}
	h := &ProposalsHandler{mandates: stub}
	if _, refused := h.mandateRefusal(context.Background(), identityForTest(), propForTest(""), domain.Spec{}, "PAID"); refused {
		t.Error("a proposal with no actor must not be refused by the mandate gate")
	}
	if stub.calls != 0 {
		t.Errorf("the authorizer was consulted %d times for an actorless proposal", stub.calls)
	}
}

// The subject and state reach the authorizer, or the wrong mandate is
// consulted.
func TestGatePassesSubjectAndStateThrough(t *testing.T) {
	stub := &stubMandates{decision: mandate.Decision{Outcome: mandate.OutcomeWithinLimit}}
	h := &ProposalsHandler{mandates: stub}
	h.mandateRefusal(context.Background(), keyBoundTo("agent://refunds"), propForTest("agent://refunds"), domain.Spec{}, "PAID")
	if stub.lastReq.Subject != "agent://refunds" {
		t.Errorf("Subject = %q", stub.lastReq.Subject)
	}
	if stub.lastReq.EntityState != "PAID" {
		t.Errorf("EntityState = %q", stub.lastReq.EntityState)
	}
	if stub.lastReq.Parameters["amount_cents"] != 4290 {
		t.Errorf("Parameters = %+v", stub.lastReq.Parameters)
	}
}

func TestGateDoesNotRejectUncountableWithoutCoveringCard(t *testing.T) {
	stub := &stubMandates{decision: mandate.Decision{Outcome: mandate.OutcomeNotCovered}}
	h := &ProposalsHandler{mandates: stub}
	prop := propForTest("agent-1")
	prop.Parameters["amount_cents"] = "100000"
	if _, refused := h.mandateRefusal(context.Background(), keyBoundTo("agent-1"), prop, domain.Spec{}, "PAID"); refused {
		t.Fatal("an action no card sums must keep the action contract's result")
	}
	if !stub.lastReq.Uncountable["amount_cents"] {
		t.Fatal("the uncountable value must reach the card engine")
	}
}

func TestGateMapsUncountableCardDrawToInvalidParameter(t *testing.T) {
	stub := &stubMandates{decision: mandate.Decision{Outcome: mandate.OutcomeUncountableDraw}}
	h := &ProposalsHandler{mandates: stub, logger: testLogger(), tenants: noRuntime{}}
	prop := propForTest("agent-1")
	prop.Parameters["amount_cents"] = "100000"
	resp, refused := h.mandateRefusal(context.Background(), keyBoundTo("agent-1"), prop, domain.Spec{}, "PAID")
	if !refused || resp.Outcome != string(outcomeInvalid) || len(resp.Reasons) != 1 || resp.Reasons[0] != "invalid_parameter" {
		t.Fatalf("response = %+v, refused = %v", resp, refused)
	}
}

func TestUnavailableMandateStoreRefuses(t *testing.T) {
	h := &ProposalsHandler{
		mandates: &stubMandates{err: errors.New("postgres is unreachable")},
		logger:   testLogger(),
		tenants:  noRuntime{},
	}
	resp, refused := h.mandateRefusal(context.Background(), identityForTest(), propForTest("agent-1"), domain.Spec{}, "PAID")
	if !refused {
		t.Fatal("an unavailable mandate store must refuse, not allow on an unknown balance")
	}
	if resp.Outcome != string(outcomeBlocked) {
		t.Errorf("outcome = %q, want blocked", resp.Outcome)
	}
	if len(resp.Reasons) == 0 || resp.Reasons[0] != "mandate_unavailable" {
		t.Errorf("reasons = %v, want mandate_unavailable", resp.Reasons)
	}
}

func identityForTest() auth.Identity {
	return auth.Identity{Subject: "key-1", Tenant: "tenant-a", Method: auth.MethodAPIKey, APIKey: true}
}

// keyBoundTo is a runtime key bound to agent, the credential a card's
// holder uses. Tests of the card engine itself use it so an unbound
// key's refusal does not stand in front of the answer under test.
func keyBoundTo(agent string) auth.Identity {
	id := identityForTest()
	id.AgentID = agent
	return id
}

func propForTest(actor string) kiffproposal.ActionProposal {
	return kiffproposal.ActionProposal{
		ID:         "prop-1",
		EntityID:   "inv-1",
		ActionName: "REFUND_INVOICE",
		ActorID:    actor,
		Parameters: map[string]any{"amount_cents": float64(4290), "reason": "duplicate"},
	}
}

func testLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// noRuntime stands in for a tenant whose runtime cannot be resolved, so
// recordOutcome returns after its nil-safe counter instead of reaching
// a decision store. The gate's refusal shape is what is under test.
type noRuntime struct{}

func (noRuntime) For(context.Context, string) (*kiffruntime.Runtime, error) {
	return nil, errors.New("no runtime in this test")
}

func (noRuntime) ForDomain(context.Context, string, string) (*kiffruntime.Runtime, error) {
	return nil, errors.New("no runtime in this test")
}

func (noRuntime) DomainIDFor(_ context.Context, _ string, domainID string) (string, error) {
	return domainID, nil
}

func (noRuntime) SpecForDomain(context.Context, string, string) (domain.Spec, error) {
	return domain.Spec{}, errors.New("no runtime in this test")
}

// the agent-as-unit design: a decision's actor is verified only when a key bound to
// exactly that actor made it. An unbound key, a key bound to someone
// else (refused anyway), and a session are claims, and carry no stamp.
func TestAttributionStampsVerifiedActorOnlyForItsBoundKey(t *testing.T) {
	prop := propForTest("refund-agent")
	for _, c := range []struct {
		name string
		id   auth.Identity
		want bool
	}{
		{"bound to the actor", keyBoundTo("refund-agent"), true},
		{"unbound key", identityForTest(), false},
		{"bound to another agent", keyBoundTo("other-agent"), false},
		{"session", auth.Identity{Subject: "user_owner", Tenant: "tenant-a", Method: auth.MethodSession, Roles: []string{"admin"}}, false},
	} {
		c.id.Roles = append(c.id.Roles, "support")
		data := attributionData(context.Background(), c.id, prop, outcomeAllowed, nil, "", "", 0)
		got, _ := data["actor_verified"].(bool)
		if got != c.want {
			t.Errorf("%s: actor_verified = %v, want %v", c.name, got, c.want)
		}
	}
}

// An action above a Card's per-action limit is refused with its own
// reason, and the message says the action is too large rather than that
// the Card is spent.
func TestGateRefusesAnActionAboveThePerActionLimit(t *testing.T) {
	stub := &stubMandates{decision: mandate.Decision{
		Outcome: mandate.OutcomePerActionExceeded, MandateID: "mdt-refunds", Limit: 50000, Draw: 150000,
		Reason: "sum(amount_cents) of 150000 is above the per-action limit of 50000",
	}}
	h := &ProposalsHandler{mandates: stub, logger: testLogger(), tenants: noRuntime{}}
	resp, refused := h.mandateRefusal(context.Background(), keyBoundTo("agent-1"), propForTest("agent-1"), domain.Spec{}, "PAID")
	if !refused || resp.Outcome != string(outcomeBlocked) || len(resp.Reasons) != 1 || resp.Reasons[0] != "mandate_per_action_exceeded" {
		t.Fatalf("response = %+v, refused = %v", resp, refused)
	}
	for _, want := range []string{"larger than a single action", "mdt-refunds", "per-action limit of 50000"} {
		if !strings.Contains(resp.Message, want) {
			t.Errorf("message missing %q: %s", want, resp.Message)
		}
	}
}
