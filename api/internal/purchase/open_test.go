package purchase

import (
	"errors"
	"testing"
)

func read() CheckoutRead {
	return CheckoutRead{Site: "papeterie-lumiere.example", Name: "Papeterie Lumière", Country: "fr", Currency: "eur", Path: "token",
		StripeProfile: "profile_61RkZt0hLumiere", StripeProfileFrom: "papeterie-lumiere.example",
		Lines:    []CheckoutLine{{Item: "A4 paper, 80 g, 5 reams", Quantity: 2, Price: "59.80"}, {Item: "Ballpoint pens, blue, box of 50", Quantity: 1, Price: "14,90"}},
		Shipping: "9.50", Total: "84.20",
		Delivery: Address{Name: "Acme SAS", Line1: "12 rue des Lilas", PostalCode: "69003", City: "Lyon", Country: "FR"}}
}

var rule = CardRule{Card: CardRef{ID: "card_9f2c", Version: 4}, Mode: "ask_each_time",
	Categories: []string{"stationery_office_supplies_printing_and_writing_paper"}, PriceRule: &PriceRule{UpTo: 200, For: []string{"shipping"}}}

// A case is built from the relay's read alone: its amounts in minor units,
// its maximum the total plus the Card's rule, its card capped there in the
// Card's categories, and its shop matched by name.
func TestACaseIsBuiltFromTheCheckoutRead(t *testing.T) {
	c, err := FromCheckout(Opening{Tenant: "t_acme", Agent: "agt_supplies", Checkout: "chk_7c1e", Request: "run_42.in", Payer: Payer{Kind: "company", ID: "t_acme"}}, read(), rule)
	if err != nil {
		t.Fatal(err)
	}
	if c.Price.Total != 8420 || c.Price.Lines != 7470 || c.Price.Shipping != 950 || c.Price.Max != 8620 || c.Grant.Cap != 8620 || c.Currency != "EUR" ||
		c.Merchant.Match.NameKey != "papeterielumiere" || c.Merchant.StripeProfile != "profile_61RkZt0hLumiere" || c.Merchant.Country != "FR" || c.Lines[1].Amount != 1490 || c.Version != 1 {
		t.Fatalf("case %+v", c)
	}
	if _, err := c.Digest(); err != nil {
		t.Fatal(err)
	}
}

func TestACheckoutThatDoesntAddUpIsNoCase(t *testing.T) {
	for name, mutate := range map[string]func(*CheckoutRead){
		"a total off its parts": func(r *CheckoutRead) { r.Total = "84.21" },
		"three decimals":        func(r *CheckoutRead) { r.Total = "84.200" },
		"a negative price":      func(r *CheckoutRead) { r.Lines[0].Price = "-59.80" },
		"no address":            func(r *CheckoutRead) { r.Delivery.Line1 = "" },
		"no path":               func(r *CheckoutRead) { r.Path = "none" },
		"a word for a price":    func(r *CheckoutRead) { r.Shipping = "free" },
		// The seller's profile is the shop's own claim: only one read on
		// the checkout's own site counts, and a token needs one.
		"a profile read on another site": func(r *CheckoutRead) { r.StripeProfileFrom = "evil.example" },
		"a look-alike site":              func(r *CheckoutRead) { r.StripeProfileFrom = "papeterie-lumiere.example.evil.example" },
		"a host under the site":          func(r *CheckoutRead) { r.StripeProfileFrom = "pay.papeterie-lumiere.example" },
		"a free line":                    func(r *CheckoutRead) { r.Lines[0].Price = "0.00" },
		"a token with no profile":        func(r *CheckoutRead) { r.StripeProfile = "" },
		"a profile that isn't one":       func(r *CheckoutRead) { r.StripeProfile = "acct_123" },
	} {
		r := read()
		r.Lines = append([]CheckoutLine(nil), r.Lines...)
		mutate(&r)
		if _, err := FromCheckout(Opening{Tenant: "t", Agent: "a"}, r, rule); err == nil {
			t.Errorf("%s: built", name)
		} else if !errors.Is(err, ErrCheckout) && !errors.Is(err, ErrCase) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if v, err := MinorUnits("1200", "JPY"); err != nil || v != 1200 {
		t.Fatalf("yen: %d %v", v, err)
	}
	if _, err := MinorUnits("12.5", "JPY"); err == nil {
		t.Fatal("yen with decimals")
	}
}
