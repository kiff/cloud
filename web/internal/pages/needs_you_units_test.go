package pages

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/kiff/cloud/web/internal/apiclient"
)

func TestScaledNumber(t *testing.T) {
	for _, c := range []struct {
		n     int64
		scale int
		want  string
	}{
		{2550, 2, "25.50"},
		{5, 2, "0.05"},
		{0, 2, "0.00"},
		{255000000, 2, "2,550,000.00"},
		{-1250, 2, "-12.50"},
		{1500, 0, "1,500"},
		{7, 3, "0.007"},
	} {
		if got := scaledNumber(c.n, c.scale); got != c.want {
			t.Errorf("scaledNumber(%d, %d) = %q, want %q", c.n, c.scale, got, c.want)
		}
	}
}

// The person approving sees the amount in the unit its Card declares,
// with every decimal place, on what it carries, what the Card allows
// and how far outside it is. Without a unit nothing changes.
func TestHeldCallAmountIsInTheCardsUnit(t *testing.T) {
	var e apiclient.CardException
	// The count is listed first: the amount is still the refund's.
	if err := json.Unmarshal([]byte(`{"id":"exc-1","proposal_id":"tc-1","subject":"claude","action":"refund","status":"held",
		"breaches":[
			{"mandate_id":"mdt-day","outcome":"limit_reached","quantity":"count","limit":10,"used":10,"draw":1},
			{"mandate_id":"mdt-gw","outcome":"limit_reached","quantity":"sum(amount_cents)","limit":2000,"used":1800,"draw":2550,"unit":"EUR","scale":2}
		]}`), &e); err != nil {
		t.Fatal(err)
	}
	if got := exceptionAmount(e); got != "25.50 EUR" {
		t.Errorf("amount = %q, want 25.50 EUR", got)
	}
	if got := breachAllows(e.Breaches[1]); got != "20.00 EUR in its window, 2.00 EUR left" {
		t.Errorf("allows = %q", got)
	}
	var b bytes.Buffer
	if err := needsYouItem(e, false, nil).Render(context.Background(), &b); err != nil {
		t.Fatal(err)
	}
	body := b.String()
	for _, want := range []string{"25.50 EUR", "23.50 EUR past this Card’s 20.00 EUR total", "10 in its window, 0 left"} {
		if !strings.Contains(body, want) {
			t.Errorf("held call missing %q", want)
		}
	}
	if strings.Contains(body, "€25") {
		t.Error("the amount was guessed as euros with its cents dropped")
	}

	// A Card that declares no unit reads as before.
	bare := apiclient.CardBreach{Outcome: "per_action_exceeded", Quantity: "sum(amount_cents)", Limit: 50000, Draw: 150000}
	if got := breachAllows(bare); got != "up to "+authorityValue(50000, "sum(amount_cents)")+" per action" {
		t.Errorf("bare allows = %q", got)
	}
	if got := exceptionAmount(apiclient.CardException{Breaches: []apiclient.CardBreach{bare}}); got != authorityValue(150000, "sum(amount_cents)") {
		t.Errorf("bare amount = %q", got)
	}
}

// a review: when what an amount means could not be read, it is shown as
// the stored number with a warning, never guessed as euros with the
// cents dropped.
func TestHeldCallAmountUnreadIsSaidNotGuessed(t *testing.T) {
	var e apiclient.CardException
	if err := json.Unmarshal([]byte(`{"id":"exc-1","proposal_id":"tc-1","subject":"claude","action":"refund","status":"held",
		"units_unavailable":true,
		"breaches":[{"mandate_id":"mdt-us","outcome":"limit_reached","quantity":"sum(amount_cents)","limit":2000,"used":1800,"draw":2550,"unit_unavailable":true}]}`), &e); err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	if err := needsYouItem(e, false, nil).Render(context.Background(), &b); err != nil {
		t.Fatal(err)
	}
	body := b.String()
	for _, want := range []string{"2,550", "2,000 in its window, 200 left", "2,350 past this Card’s 2,000 total", "currency and decimal places"} {
		if !strings.Contains(body, want) {
			t.Errorf("unread amount missing %q", want)
		}
	}
	if strings.Contains(body, "€") {
		t.Error("an unread amount was guessed as euros")
	}
}

// a review: a refund held only because its call count ran out has no
// summed breach. Its amount still reads in the unit the action declares,
// and the argument says what it means; with no known unit it shows no
// amount rather than the count's "1".
func TestCountOnlyHoldShowsItsAmountInTheActionsUnit(t *testing.T) {
	var e apiclient.CardException
	if err := json.Unmarshal([]byte(`{"id":"exc-2","proposal_id":"tc-2","subject":"claude","action":"refund","status":"held",
		"parameters":{"amount_cents":2550},"arguments":{"amount_cents":2550,"order_id":"70347"},
		"parameter_units":{"amount_cents":{"unit":"USD","scale":2}},
		"breaches":[{"mandate_id":"mdt-day","outcome":"limit_reached","quantity":"count","limit":10,"used":10,"draw":1}]}`), &e); err != nil {
		t.Fatal(err)
	}
	if got := exceptionAmount(e); got != "25.50 USD" {
		t.Errorf("amount = %q, want 25.50 USD", got)
	}
	var b bytes.Buffer
	if err := needsYouItem(e, false, nil).Render(context.Background(), &b); err != nil {
		t.Fatal(err)
	}
	body := b.String()
	if !strings.Contains(body, `<code>2550</code> <span class="ny-arg-means">25.50 USD</span>`) {
		t.Errorf("the amount argument does not say what it means:\n%s", body)
	}
	if strings.Contains(body, "could not read") {
		t.Error("a read unit was reported unread")
	}

	e.ParameterUnits = nil
	if got := exceptionAmount(e); got != "" {
		t.Errorf("count-only amount with no unit = %q, want none", got)
	}
}

// a review at eff74a35: the API stores a request's numeric parameters by
// lowercase name (numericParameters), while the action declares
// amountCents. A count-only hold still reads its amount, and the tool's
// own argument keeps its declared spelling and unit.
func TestCountOnlyHoldMatchesACamelCaseAmount(t *testing.T) {
	var e apiclient.CardException
	if err := json.Unmarshal([]byte(`{"id":"exc-4","proposal_id":"tc-4","subject":"claude","action":"refund","status":"held",
		"parameters":{"amountcents":2550},"arguments":{"amountCents":2550},
		"parameter_units":{"amountCents":{"unit":"USD","scale":2}},
		"breaches":[{"mandate_id":"mdt-day","outcome":"limit_reached","quantity":"count","limit":10,"used":10,"draw":1}]}`), &e); err != nil {
		t.Fatal(err)
	}
	if got := exceptionAmount(e); got != "25.50 USD" {
		t.Errorf("amount = %q, want 25.50 USD", got)
	}
	var b bytes.Buffer
	if err := needsYouItem(e, false, nil).Render(context.Background(), &b); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), `<dt>amountCents</dt>`) || !strings.Contains(b.String(), `<span class="ny-arg-means">25.50 USD</span>`) {
		t.Errorf("the amountCents argument does not say what it means:\n%s", b.String())
	}
}

// a review at 504802c5: on a Card whose actions disagree on the unit
// (refund_us in USD, refund_eu in EUR), the held refund_us still reads
// 25.50 USD, the Card's limits are stored numbers with a warning, and
// no euro is invented.
func TestMixedUnitCardTotalsAreNotReadAsEuros(t *testing.T) {
	var e apiclient.CardException
	if err := json.Unmarshal([]byte(`{"id":"exc-3","proposal_id":"tc-3","subject":"claude","action":"refund_us","status":"held",
		"parameters":{"amount_cents":2550},"arguments":{"amount_cents":2550},
		"parameter_units":{"amount_cents":{"unit":"USD","scale":2}},
		"breaches":[{"mandate_id":"mdt-mixed","outcome":"limit_reached","quantity":"sum(amount_cents)","limit":2000,"used":1800,"draw":2550,"unit_ambiguous":true}]}`), &e); err != nil {
		t.Fatal(err)
	}
	if got := exceptionAmount(e); got != "25.50 USD" {
		t.Errorf("amount = %q, want 25.50 USD", got)
	}
	var b bytes.Buffer
	if err := needsYouItem(e, false, nil).Render(context.Background(), &b); err != nil {
		t.Fatal(err)
	}
	body := b.String()
	for _, want := range []string{"25.50 USD", "2,000 in its window, 200 left", "2,350 past this Card’s 2,000 total", "different currencies or decimal places"} {
		if !strings.Contains(body, want) {
			t.Errorf("mixed-unit Card missing %q", want)
		}
	}
	if strings.Contains(body, "€") {
		t.Error("a mixed-unit Card total was read as euros")
	}
	if strings.Contains(body, "could not read") {
		t.Error("a Card that was read was reported unread")
	}
}
