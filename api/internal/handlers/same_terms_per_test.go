package handlers

import (
	"testing"

	"github.com/kiff/cloud/api/internal/mandate"
)

// A revision that counts a total for each item, or stops doing so, changes
// the Card: "one invoice per order, ever" redrafted as one in the taf's whole
// life is never shown to the owner as unchanged.
func TestSameTermsSeesATotalsPer(t *testing.T) {
	card := func(per string) mandate.Mandate {
		return mandate.Mandate{ID: "invoices-drafts", Subject: "invoices", Domain: "gateway",
			Grants: []mandate.Grant{{Action: "create_invoice"}},
			Aggregates: []mandate.Aggregate{
				{Quantity: mandate.Quantity{Kind: mandate.KindCount}, Limit: 10, Window: mandate.WindowCalendarDay},
				{Quantity: mandate.Quantity{Kind: mandate.KindCount, Per: per}, Limit: 1, Window: mandate.WindowLifetime},
			}}
	}
	if !sameTerms(card("order"), card("order")) {
		t.Fatal("the same Card read as changed")
	}
	if sameTerms(card(""), card("order")) || sameTerms(card("order"), card("")) {
		t.Fatal("a total for each order and one in all read as the same")
	}
	if sameTerms(card("customer"), card("order")) {
		t.Fatal("a total per customer and one per order read as the same")
	}
}
