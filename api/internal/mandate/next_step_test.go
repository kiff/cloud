package mandate_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/domain"
	"github.com/kiff/cloud/api/internal/mandate"
)

// stepsCard is homeCard offering the owner two next steps besides approve
// and reject.
func stepsCard(id string, steps ...string) mandate.Mandate {
	m := homeCard(id, 50000, 500000)
	m.Grants[0].NextSteps = steps
	return m
}

// Invariant: a refusal's next step is one the held call's own Card offers,
// and it stays a refusal: nothing is drawn, the call ends, and the agent can
// read what the owner asked it to do instead.
// Category: authority
// Attack: refuse with a next step the Card does not list, or treat a refusal with a next step as permission to act
// Refused: an unlisted step is rejected with ErrNextStepNotOnCard and the hold stays open; a listed one records the refusal, draws nothing and the call stays refused
func TestRejectWithNextStep(t *testing.T) {
	r := newRig(t, stepsCard("card-a", "return_first", "proof_first"))
	d := r.decide("p1", 150000)
	ctx := context.Background()

	if _, err := r.store.Reject(ctx, tenantID, d.ExceptionID, "owner@x", r.clock, mandate.Refusal{NextStep: "refund_half"}); !errors.Is(err, mandate.ErrNextStepNotOnCard) {
		t.Fatalf("a step the Card does not offer: %v", err)
	}
	if e, _ := r.store.Exception(ctx, tenantID, d.ExceptionID); e.Status != mandate.ExceptionHeld || e.Refusal != nil {
		t.Fatalf("a refused answer must leave the hold untouched: %+v", e)
	}

	e, err := r.store.Reject(ctx, tenantID, d.ExceptionID, "owner@x", r.clock,
		mandate.Refusal{NextStep: " return_first ", Note: "  Send the return label first.  "})
	if err != nil {
		t.Fatal(err)
	}
	if e.Status != mandate.ExceptionRejected || e.Refusal == nil ||
		*e.Refusal != (mandate.Refusal{NextStep: "return_first", Note: "Send the return label first."}) {
		t.Fatalf("refusal not recorded as given: %+v", e)
	}
	if again := r.decide("p1", 150000); again.Outcome != mandate.OutcomeOwnerRejected {
		t.Fatalf("a refusal with a next step is still a refusal: %+v", again)
	}
	if r.used("card-a") != 0 || r.outside("card-a") != 0 {
		t.Fatal("a refused action drew or was recorded")
	}
	if got, _ := r.store.Exception(ctx, tenantID, d.ExceptionID); got.Refusal == nil || got.Refusal.NextStep != "return_first" {
		t.Fatalf("the refusal must be stored with the hold: %+v", got.Refusal)
	}
}

func TestRejectWithoutNextStepIsPlain(t *testing.T) {
	r := newRig(t, stepsCard("card-a", "return_first"))
	d := r.decide("p1", 150000)
	e, err := r.store.Reject(context.Background(), tenantID, d.ExceptionID, "owner@x", r.clock, mandate.Refusal{Note: "  "})
	if err != nil {
		t.Fatal(err)
	}
	if e.Refusal != nil {
		t.Fatalf("an empty refusal is a plain no: %+v", e.Refusal)
	}
}

func TestRejectNoteBounds(t *testing.T) {
	r := newRig(t, stepsCard("card-a", "return_first"))
	ctx := context.Background()
	for name, note := range map[string]string{
		"too long": strings.Repeat("é", mandate.MaxRefusalNote+1),
		"control":  "ok\x07",
	} {
		d := r.decide("p-"+name, 150000)
		if _, err := r.store.Reject(ctx, tenantID, d.ExceptionID, "owner@x", r.clock, mandate.Refusal{Note: note}); !errors.Is(err, mandate.ErrInvalidRefusal) {
			t.Errorf("%s: %v", name, err)
		}
	}
	d := r.decide("p-ok", 150000)
	if _, err := r.store.Reject(ctx, tenantID, d.ExceptionID, "owner@x", r.clock,
		mandate.Refusal{Note: strings.Repeat("é", mandate.MaxRefusalNote) + "\n"}); err != nil {
		t.Fatalf("a note at the limit, with a line break, is fine: %v", err)
	}
}

// A Card without next steps offers none: approve or refuse, as before.
func TestCardWithoutNextStepsOffersNone(t *testing.T) {
	r := newRig(t, homeCard("card-a", 50000, 500000))
	d := r.decide("p1", 150000)
	if _, err := r.store.Reject(context.Background(), tenantID, d.ExceptionID, "owner@x", r.clock, mandate.Refusal{NextStep: "return_first"}); !errors.Is(err, mandate.ErrNextStepNotOnCard) {
		t.Fatalf("a Card with no next steps: %v", err)
	}
}

func TestNextStepsFor(t *testing.T) {
	a, b, other := stepsCard("card-a", "return_first", "proof_first"), stepsCard("card-b", "proof_first", "call_customer"), stepsCard("card-c", "never")
	e := mandate.Exception{Action: "REFUND_INVOICE", Request: refund(150000),
		Breaches: []mandate.Breach{{MandateID: "card-a"}, {MandateID: "card-b"}}}
	got := mandate.NextStepsFor(e, []mandate.Mandate{a, b, other})
	if strings.Join(got, ",") != "return_first,proof_first,call_customer" {
		t.Fatalf("steps of the named Cards, in order, without repeats: %v", got)
	}
	e.Action = "CANCEL_ORDER"
	if got := mandate.NextStepsFor(e, []mandate.Mandate{a, b}); len(got) != 0 {
		t.Fatalf("a grant for another action offers nothing: %v", got)
	}
}

func TestNextStepsValidation(t *testing.T) {
	actions := []domain.ActionSpec{{Name: "REFUND_INVOICE", AllowedStates: []string{"PAID"},
		Parameters: []domain.ParameterSpec{{Name: "amount_cents", Type: "integer"}}}}
	if err := mandate.Validate(stepsCard("card-a", "return_first", "proof_first"), actions); err != nil {
		t.Fatalf("valid next steps: %v", err)
	}
	for name, steps := range map[string][]string{
		"upper case":  {"Return_first"},
		"space":       {"return first"},
		"leading _":   {"_return"},
		"empty":       {""},
		"too long":    {strings.Repeat("a", mandate.MaxNextStepLen+1)},
		"duplicate":   {"return_first", "return_first"},
		"seven steps": {"a", "b", "c", "d", "e", "f", "g"},
	} {
		if err := mandate.Validate(stepsCard("card-a", steps...), actions); !errors.Is(err, mandate.ErrInvalidMandate) {
			t.Errorf("%s: err = %v, want ErrInvalidMandate", name, err)
		}
	}
}

// An external approver's reason code reaches the agent as a next step only
// when the Card offers it; any other code stays evidence.
func TestExternalRejectReasonAsNextStep(t *testing.T) {
	for code, want := range map[string]string{"return_first": "return_first", "model_unsure": ""} {
		r := newRig(t, stepsCard("card", "return_first"))
		r.store.SetApproverPolicy(func(context.Context, string, string) error { return nil })
		ctx := context.Background()
		d := r.decide("external", 75000)
		e, err := r.store.Exception(ctx, tenantID, d.ExceptionID)
		if err != nil {
			t.Fatal(err)
		}
		g := mandate.ApproverGrant{ID: "grant", TenantID: tenantID, Name: "lead", Principal: "lead", CardID: "card", Worker: e.Subject,
			Action: e.Action, Parameter: "amount_cents", Max: 100000, CreatedBy: "admin", CreatedAt: r.clock, ExpiresAt: r.clock.Add(time.Hour), KeyHash: "h"}
		if err := r.store.CreateApproverGrant(ctx, g); err != nil {
			t.Fatal(err)
		}
		cards, _ := r.store.ForSubject(ctx, tenantID, e.Subject)
		review := mandate.ExternalReview{State: mandate.ReviewState(e), Card: mandate.ReviewCard(cards[0]),
			Answer: mandate.ExternalAnswer{GrantID: g.ID, Principal: g.Principal, Decision: "reject", ReasonCode: code, ReviewToken: "t"}}
		got, err := r.store.AnswerExternal(ctx, tenantID, e.ID, g.ID, review, r.clock)
		if err != nil || got.Status != mandate.ExceptionRejected {
			t.Fatalf("%s: %+v %v", code, got, err)
		}
		if step := ""; got.Refusal != nil {
			step = got.Refusal.NextStep
			if step != want {
				t.Errorf("%s: next step %q, want %q", code, step, want)
			}
		} else if want != "" {
			t.Errorf("%s: no next step, want %q", code, want)
		}
	}
}

// The refusal and the Card's next steps survive the Postgres store.
func TestRejectWithNextStepPostgres(t *testing.T) {
	pool := requirePostgres(t)
	ctx := context.Background()
	tenant := "pg-next-step"
	seedTenant(t, pool, tenant)
	now := time.Now().UTC()
	s := mandate.NewPostgresStore(pool)
	m := stepsCard("card", "return_first", "proof_first")
	m.TenantID, m.ValidFrom, m.ValidUntil = tenant, now.Add(-time.Hour), now.Add(time.Hour)
	if err := s.Issue(ctx, m, "admin"); err != nil {
		t.Fatal(err)
	}
	a := mandate.NewAuthorizer(s, nil, func() time.Time { return now })
	d, err := a.Authorize(ctx, tenant, "p1", refund(150000))
	if err != nil || d.Outcome != mandate.OutcomeOwnerRequired {
		t.Fatalf("hold: %+v %v", d, err)
	}
	if _, err := s.Reject(ctx, tenant, d.ExceptionID, "admin", now, mandate.Refusal{NextStep: "refund_half"}); !errors.Is(err, mandate.ErrNextStepNotOnCard) {
		t.Fatalf("unlisted step: %v", err)
	}
	if _, err := s.Reject(ctx, tenant, d.ExceptionID, "admin", now, mandate.Refusal{NextStep: "proof_first", Note: "Ask for a photo."}); err != nil {
		t.Fatal(err)
	}
	got, err := s.Exception(ctx, tenant, d.ExceptionID)
	if err != nil || got.Status != mandate.ExceptionRejected || got.Refusal == nil ||
		*got.Refusal != (mandate.Refusal{NextStep: "proof_first", Note: "Ask for a photo."}) {
		t.Fatalf("stored: %+v %v", got.Refusal, err)
	}
	cards, _ := s.ForSubject(ctx, tenant, m.Subject)
	if len(cards) != 1 || strings.Join(cards[0].Grants[0].NextSteps, ",") != "return_first,proof_first" {
		t.Fatalf("Card next steps: %+v", cards)
	}
}

// A retry of a refused call carries the refusal on its decision.
func TestRejectedDecisionCarriesTheRefusal(t *testing.T) {
	r := newRig(t, stepsCard("card-a", "return_first"))
	d := r.decide("p1", 150000)
	if _, err := r.store.Reject(context.Background(), tenantID, d.ExceptionID, "owner@x", r.clock, mandate.Refusal{NextStep: "return_first"}); err != nil {
		t.Fatal(err)
	}
	got := r.decide("p1", 150000)
	if got.Outcome != mandate.OutcomeOwnerRejected || got.Refusal == nil || got.Refusal.NextStep != "return_first" {
		t.Fatalf("decision = %+v", got)
	}
}

// The decision that first holds a call names the Card's next steps, so the
// owner's email can offer them; a retry, which sends no email, does not.
func TestNewlyHeldNamesNextSteps(t *testing.T) {
	r := newRig(t, stepsCard("card-a", "return_first", "proof_first"))
	d := r.decide("p1", 150000)
	if !d.NewlyHeld || strings.Join(d.NextSteps, ",") != "return_first,proof_first" {
		t.Fatalf("first hold: %+v", d)
	}
	if again := r.decide("p1", 150000); again.NewlyHeld || again.NextSteps != nil {
		t.Fatalf("retry: %+v", again)
	}
	plain := newRig(t, homeCard("card-b", 50000, 500000))
	if d := plain.decide("p1", 150000); !d.NewlyHeld || d.NextSteps != nil {
		t.Fatalf("a Card without next steps: %+v", d)
	}
}
