package handlers

import (
	"testing"

	"github.com/kiff/cloud/api/internal/domain"
	"github.com/kiff/cloud/api/internal/mandate"
)

// A Card's money total lost its currency when the vendor that tells it was
// disconnected (in): the line says which tool its
// currency comes from and that it isn't connected. A line that has its unit,
// or a Card whose tools are all connected, says nothing more.
func TestACardSaysWhyItsAmountHasNoCurrency(t *testing.T) {
	card := mandate.Mandate{Grants: []mandate.Grant{{Action: "create_refund"}, {Action: "list_charges"}}}
	connected := []domain.ActionSpec{{Name: "list_charges"}}
	lines := []map[string]any{
		{"quantity": "sum(amount)"},
		{"quantity": "sum(fee)", "unit": "EUR"},
		{"quantity": "count"},
	}
	withUnitGaps(lines, connected, card, nil)
	if lines[0]["unit_words"] != "Its currency comes from Create refund, which isn't connected." {
		t.Fatalf("the bare total: %v", lines[0])
	}
	if m, _ := lines[0]["unit_missing"].(map[string]any); m["connected"] != false || len(m["tools"].([]string)) != 1 {
		t.Fatalf("unit_missing: %v", lines[0]["unit_missing"])
	}
	if _, said := lines[1]["unit_words"]; said {
		t.Fatalf("a total with its unit said more: %v", lines[1])
	}
	if _, said := lines[2]["unit_words"]; said {
		t.Fatalf("a count said more: %v", lines[2])
	}

	all := []map[string]any{{"quantity": "sum(amount)"}}
	withUnitGaps(all, []domain.ActionSpec{{Name: "create_refund"}, {Name: "list_charges"}}, card, nil)
	if _, said := all[0]["unit_words"]; said {
		t.Fatalf("every tool connected, yet it said: %v", all[0])
	}

	// Where KIFF still has the tool's title, the words use it; the ids stay.
	titled := []map[string]any{{"quantity": "sum(amount)"}}
	withUnitGaps(titled, connected, card, map[string]string{"create_refund": "Refund a payment"})
	if titled[0]["unit_words"] != "Its currency comes from Refund a payment, which isn't connected." ||
		titled[0]["unit_missing"].(map[string]any)["tools"].([]string)[0] != "create_refund" {
		t.Fatalf("titled: %v", titled[0])
	}
}
