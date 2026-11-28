package purchase

import (
	"fmt"
	"strings"
)

// Approval is a held purchase as the apps draw it (contracts/purchase.json
// approval.value): the case version, its digest, and every line in KIFF's
// words. English until the words batch gives every language.
type Approval struct {
	CaseID  string        `json:"case_id"`
	Version int           `json:"version"`
	Digest  string        `json:"digest"`
	Words   ApprovalWords `json:"words"`
}

// ApprovalWords are the lines the apps draw, none composed by the apps.
type ApprovalWords struct {
	Title     string `json:"title"`
	Subtitle  string `json:"subtitle"`
	Total     string `json:"total"`
	TotalNote string `json:"total_note"`
	Delivery  string `json:"delivery"`
	ShowMore  string `json:"show_more"`
	// ShowLess is the toggle's words while the lines are open.
	ShowLess     string      `json:"show_less"`
	Lines        []WordsLine `json:"lines"`
	Charges      []WordsLine `json:"charges"`
	PriceRule    string      `json:"price_rule,omitempty"`
	Grant        string      `json:"grant"`
	BuysNow      string      `json:"buys_now"`
	NothingUntil string      `json:"nothing_until"`
	Reason       string      `json:"reason"`
	Prompt       string      `json:"prompt"`
}

// WordsLine is one line of the open list.
type WordsLine struct {
	Name     string `json:"name"`
	Quantity string `json:"quantity,omitempty"`
	Amount   string `json:"amount"`
}

// ApprovalOf is the held case version as the apps draw it.
func ApprovalOf(r Row) Approval {
	c := r.Case
	money := func(v int64) string { return Money(v, c.Currency) }
	w := ApprovalWords{
		Title:        "Buy from " + c.Merchant.Name,
		Subtitle:     c.Merchant.Site,
		Total:        money(c.Price.Total),
		TotalNote:    "With shipping and VAT, as the checkout shows it",
		Delivery:     addressLine(c.Delivery),
		ShowMore:     "Show more",
		ShowLess:     "Show less",
		Grant:        fmt.Sprintf("A card for this purchase: up to %s, at this shop, used once.", money(c.Price.Max)),
		BuysNow:      "It buys now. If the shop doesn't take it within 15 minutes, nothing is bought.",
		NothingUntil: "Nothing is bought until you approve.",
		Reason:       reasonFor(c.Mode),
		Prompt:       fmt.Sprintf("Buy from %s · %s", c.Merchant.Name, money(c.Price.Total)),
	}
	for _, l := range c.Lines {
		w.Lines = append(w.Lines, WordsLine{Name: l.Name, Quantity: fmt.Sprintf("× %d", l.Quantity), Amount: money(l.Amount)})
	}
	if c.Price.Shipping > 0 {
		w.Charges = append(w.Charges, WordsLine{Name: "Shipping", Amount: money(c.Price.Shipping)})
	}
	if c.Price.Tax > 0 {
		w.Charges = append(w.Charges, WordsLine{Name: "Tax", Amount: money(c.Price.Tax)})
	}
	if p := c.Price.Rule; p != nil {
		w.PriceRule = fmt.Sprintf("It can go up to %s more for %s.", money(p.UpTo), strings.Join(p.For, " and "))
	}
	return Approval{CaseID: c.CaseID, Version: c.Version, Digest: r.Digest, Words: w}
}

func reasonFor(mode string) string {
	if mode == "within_limits" {
		return "It's outside your Card's limits."
	}
	return "Your Card asks you each time."
}

// Money writes minor units in a currency, as the apps show amounts: the
// symbol for the euro, the pound and the dollar, else the code.
func Money(v int64, currency string) string {
	sign := ""
	if v < 0 {
		sign, v = "-", -v
	}
	n := fmt.Sprintf("%d.%02d", v/100, v%100)
	switch currency {
	case "EUR":
		return sign + "€" + n
	case "GBP":
		return sign + "£" + n
	case "USD":
		return sign + "$" + n
	}
	return sign + n + " " + currency
}

func addressLine(a Address) string {
	parts := []string{}
	for _, p := range []string{a.Name, a.Line1, a.Line2, strings.TrimSpace(a.PostalCode + " " + a.City), a.Region, a.Country} {
		if strings.TrimSpace(p) != "" {
			parts = append(parts, p)
		}
	}
	return strings.Join(parts, ", ")
}
