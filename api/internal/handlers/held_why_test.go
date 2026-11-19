package handlers

import (
	"context"
	"slices"
	"testing"

	"github.com/kiff/cloud/api/internal/domain"
	"github.com/kiff/cloud/api/internal/mandate"
)

// A hold tells the agent which limit its call went over, in the words
// the owner reads on the hold: an amount in its Card's unit, else KIFF's
// own reason.
func TestAHoldSaysWhichLimitTheCallWentOver(t *testing.T) {
	ctx := context.Background()
	amount := mandate.Quantity{Kind: mandate.KindSum, Parameter: "amount_cents"}
	store := mandate.NewMemoryStore()
	if err := store.Issue(ctx, mandate.Mandate{ID: "mdt-refunds", TenantID: "t1", Subject: "refund-agent",
		Grants:     []mandate.Grant{{Action: "refund", PerAction: []mandate.PerActionLimit{{Quantity: amount, Max: 10000}}}},
		Aggregates: []mandate.Aggregate{{Quantity: amount, Limit: 25000, Window: mandate.WindowCalendarDay}},
	}, "test"); err != nil {
		t.Fatal(err)
	}
	spec := domain.Spec{Actions: []domain.ActionSpec{{Name: "refund",
		Parameters: []domain.ParameterSpec{{Name: "amount_cents", Type: "int", Unit: "USD", Scale: 2}}}}}
	breaches := []mandate.Breach{
		{MandateID: "mdt-refunds", Outcome: mandate.OutcomePerActionExceeded, Quantity: amount, Limit: 10000, Draw: 15000, Reason: "sum(amount_cents) of 15000 exceeds 10000"},
		{MandateID: "mdt-refunds", Outcome: mandate.OutcomeLimitReached, Quantity: amount, Limit: 25000, Used: 22000, Draw: 4000, Reason: "sum(amount_cents) would reach 26000 of 25000"},
		{MandateID: "mdt-refunds", Outcome: mandate.OutcomeResourceOutside, Resource: "order", Value: "T-9", Reason: `order "T-9" is not one this Card covers`},
		{MandateID: "mdt-gone", Outcome: mandate.OutcomeLimitReached, Quantity: amount, Limit: 1, Draw: 2, Reason: "sum(amount_cents) would reach 2 of 1"},
	}
	h := &ProposalsHandler{mandates: mandate.NewAuthorizer(store, nil, nil)}
	got := h.heldWhy(ctx, "t1", spec, breaches)
	want := []string{
		"150.00 USD is above the 100.00 USD limit for one call",
		"40.00 USD would go over the 250.00 USD limit: 220.00 USD is used",
		`order "T-9" is not one this Card covers`,
		"sum(amount_cents) would reach 2 of 1",
	}
	if !slices.Equal(got, want) {
		t.Errorf("why = %q\nwant  %q", got, want)
	}

	// Cards that can't be read: KIFF's own reasons, never nothing.
	h = &ProposalsHandler{mandates: coversOnly{}}
	if got := h.heldWhy(ctx, "t1", spec, breaches[:1]); !slices.Equal(got, []string{breaches[0].Reason}) {
		t.Errorf("without Cards, why = %q", got)
	}
}

// coversOnly is a mandate gate that can't list Cards.
type coversOnly struct{}

func (coversOnly) Authorize(context.Context, string, string, mandate.Request) (mandate.Decision, error) {
	return mandate.Decision{}, nil
}

func (coversOnly) Covers(context.Context, string, mandate.Request) (bool, error) { return true, nil }
