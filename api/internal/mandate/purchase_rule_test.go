package mandate_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/kiff/cloud/api/internal/mandate"
)

func buying(rule mandate.PurchaseRule) mandate.Grant {
	return mandate.Grant{Action: "buy", Opens: []mandate.Opening{{Class: "money"}}, Purchase: &rule}
}

var within = mandate.PurchaseRule{Mode: mandate.PurchaseWithinLimits, Currency: "EUR", PerPurchase: 5000, PerDay: 20000, PerHour: 3, PerDayCount: 10,
	Categories: []string{"stationery_office_supplies_printing_and_writing_paper"}}

// A purchase rule opens money, names its currency and categories, and, to
// buy within limits, a per-purchase limit, a day's limit and a count, so a
// loop of small buys can't drain the day.
func TestAPurchaseRuleIsOnlyOneKIFFCanKeep(t *testing.T) {
	if err := mandate.ValidatePurchaseForTest("buy", buying(within)); err != nil {
		t.Fatal(err)
	}
	ask := mandate.PurchaseRule{Mode: mandate.PurchaseAskEachTime, Currency: "EUR", Categories: within.Categories}
	if err := mandate.ValidatePurchaseForTest("buy", buying(ask)); err != nil {
		t.Fatalf("asking each time: %v", err)
	}
	noMoney := buying(within)
	noMoney.Opens = nil
	for name, g := range map[string]mandate.Grant{
		"money not opened":        noMoney,
		"no count":                buying(func() mandate.PurchaseRule { r := within; r.PerHour, r.PerDayCount = 0, 0; return r }()),
		"no day":                  buying(func() mandate.PurchaseRule { r := within; r.PerDay = 0; return r }()),
		"a purchase over the day": buying(func() mandate.PurchaseRule { r := within; r.PerPurchase = 30000; return r }()),
		"no category":             buying(func() mandate.PurchaseRule { r := within; r.Categories = nil; return r }()),
		"another mode":            buying(func() mandate.PurchaseRule { r := within; r.Mode = "always"; return r }()),
		"a currency name":         buying(func() mandate.PurchaseRule { r := within; r.Currency = "euro"; return r }()),
		"an empty price rule": buying(func() mandate.PurchaseRule {
			r := within
			r.PriceRule = &mandate.PurchasePriceRule{}
			return r
		}()),
	} {
		if err := mandate.ValidatePurchaseForTest("buy", g); !errors.Is(err, mandate.ErrInvalidMandate) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// Loosening a purchase rule widens the Card, so it goes through the owner's
// approval; tightening it doesn't.
func TestLooseningAPurchaseRuleWidensTheCard(t *testing.T) {
	card := func(rule *mandate.PurchaseRule) mandate.Mandate {
		m := spendMandate(500000)
		m.Grants[0].Opens = []mandate.Opening{{Class: "money"}}
		m.Grants[0].Purchase = rule
		return m
	}
	ask := within
	ask.Mode = mandate.PurchaseAskEachTime
	cur := card(&ask)
	for name, tc := range map[string]struct {
		rule *mandate.PurchaseRule
		word string
	}{
		"asking turned into within limits": {&within, "without asking you"},
		"a day raised":                     {func() *mandate.PurchaseRule { r := ask; r.PerDay = 30000; return &r }(), "a day"},
		"a count removed":                  {func() *mandate.PurchaseRule { r := ask; r.PerHour = 0; return &r }(), "purchases an hour"},
		"a category added": {func() *mandate.PurchaseRule {
			r := ask
			r.Categories = append([]string{"book_stores"}, r.Categories...)
			return &r
		}(), "book_stores"},
		"a price rule added": {func() *mandate.PurchaseRule {
			r := ask
			r.PriceRule = &mandate.PurchasePriceRule{UpTo: 200, For: []string{"shipping"}}
			return &r
		}(), "200 more"},
		"another currency": {func() *mandate.PurchaseRule { r := ask; r.Currency = "GBP"; return &r }(), "GBP"},
	} {
		ws := mandate.Widens(&cur, card(tc.rule))
		found := false
		for _, w := range ws {
			if w.Kind == "purchase" && strings.Contains(w.Words, tc.word) {
				found = true
			}
		}
		if !found {
			t.Errorf("%s: %+v", name, ws)
		}
	}
	if ws := mandate.Widens(&cur, card(nil)); len(ws) != 0 {
		t.Errorf("buying removed: %+v", ws)
	}
	tighter := ask
	tighter.PerDay = 10000
	if ws := mandate.Widens(&cur, card(&tighter)); len(ws) != 0 {
		t.Errorf("a day lowered: %+v", ws)
	}
	none := card(nil)
	if ws := mandate.Widens(&none, cur); len(ws) == 0 || !strings.Contains(ws[len(ws)-1].Words, "buy") {
		t.Errorf("buying added: %+v", ws)
	}
}
