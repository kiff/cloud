package pages

import "testing"

func TestSuggestAmountUnit(t *testing.T) {
	for in, want := range map[string]string{
		"amount_eur": "EUR:0", "amount_eur_cents": "EUR:2", "amount_cents": "", "usd_amount": "USD:0",
		"price_pence": "GBP:2", "quantity": "", "": "",
	} {
		if got := suggestAmountUnit(in); got != want {
			t.Errorf("suggestAmountUnit(%q) = %q, want %q", in, got, want)
		}
		if _, _, ok := ParseAmountUnit(suggestAmountUnit(in)); !ok {
			t.Errorf("suggestion for %q is not an offered option", in)
		}
	}
	for _, c := range []struct {
		unit  string
		scale int
		want  string
	}{{"", 0, ""}, {"EUR", 0, "EUR"}, {"EUR", 2, "EUR cents"}, {"GBP", 2, "GBP pence"}, {"points", 0, "points"}} {
		if got := amountUnitLabel(c.unit, c.scale); got != c.want {
			t.Errorf("amountUnitLabel(%q, %d) = %q, want %q", c.unit, c.scale, got, c.want)
		}
	}
}
