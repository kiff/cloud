package purchase

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// CheckoutRead is a checkout as the relay read it on the page and signed
// into its browser block: the only source a case is built from, never the
// taf's arguments. Prices are decimal strings as the page shows them.
type CheckoutRead struct {
	Site     string         `json:"site"`
	Name     string         `json:"name"`
	Country  string         `json:"country"`
	Lines    []CheckoutLine `json:"lines"`
	Shipping string         `json:"shipping"`
	Tax      string         `json:"tax"`
	Total    string         `json:"total"`
	Currency string         `json:"currency"`
	Delivery Address        `json:"delivery"`
	// Path is how the shop takes payment: a token where it takes one,
	// else a card fill.
	Path string `json:"path"`
	// StripeProfile is the seller's Stripe profile the shop's own payment
	// endpoint names, for a token; StripeProfileFrom is the host the relay
	// read it on, which must be on the checkout's own site.
	StripeProfile     string `json:"stripe_profile,omitempty"`
	StripeProfileFrom string `json:"stripe_profile_from,omitempty"`
}

// CheckoutLine is one line as read.
type CheckoutLine struct {
	Item     string `json:"item"`
	Quantity int    `json:"quantity"`
	Price    string `json:"price"`
}

// CardRule is what a case copies from the Card it's made under.
type CardRule struct {
	Card       CardRef
	Mode       string
	Categories []string
	PriceRule  *PriceRule
}

// Opening is who a case is for.
type Opening struct {
	Tenant, Agent, Checkout, Request string
	Payer                            Payer
	// PayerMarket is the payer's country.
	PayerMarket string
}

// ErrCheckout: the read can't be a case (its parts don't add up, an amount
// isn't one, or a field is missing).
var ErrCheckout = errors.New("purchase: the checkout read isn't a purchase KIFF can build")

// FromCheckout builds a case's first version from the relay's checkout
// read and the Card's rule. The shop's own category code isn't known before
// the authorization, so the card is controlled by the Card's categories and
// the live authorization matches the shop itself.
func FromCheckout(o Opening, read CheckoutRead, rule CardRule) (Case, error) {
	bad := func(why string) (Case, error) { return Case{}, fmt.Errorf("%w: %s", ErrCheckout, why) }
	cur := strings.ToUpper(strings.TrimSpace(read.Currency))
	amount := func(s string) (int64, error) { return MinorUnits(s, cur) }
	c := Case{Schema: Schema, CaseID: NewCaseID(), Version: 1, Tenant: o.Tenant, Agent: o.Agent, Card: rule.Card, Mode: rule.Mode,
		Route: "issued_card", Path: read.Path, Currency: cur, ValidForSeconds: ValidFor, Delivery: read.Delivery,
		Reference: Reference{Checkout: o.Checkout, Request: o.Request}, Payer: o.Payer,
		Merchant: Merchant{Name: strings.TrimSpace(read.Name), Site: strings.TrimSpace(read.Site), Country: strings.ToUpper(read.Country)},
		Markets:  Markets{Payer: strings.ToUpper(o.PayerMarket), Merchant: strings.ToUpper(read.Country)}}
	c.Merchant.Match = Match{NameKey: Fold(c.Merchant.Name)}
	if read.Path == "token" {
		// The profile is the shop's own claim, so only one read on the
		// checkout's own site counts.
		if !onSite(read.StripeProfileFrom, c.Merchant.Site) {
			return bad("the seller's Stripe profile wasn't read on the checkout's own site")
		}
		c.Merchant.StripeProfile = strings.TrimSpace(read.StripeProfile)
	}
	var sum int64
	for _, l := range read.Lines {
		a, err := amount(l.Price)
		if err != nil {
			return bad("a line's price")
		}
		c.Lines = append(c.Lines, Line{Name: strings.TrimSpace(l.Item), Quantity: l.Quantity, Amount: a})
		sum += a
	}
	ship, err1 := optional(read.Shipping, cur)
	tax, err2 := optional(read.Tax, cur)
	total, err3 := amount(read.Total)
	if err1 != nil || err2 != nil || err3 != nil {
		return bad("an amount")
	}
	if sum+ship+tax != total {
		return bad("the lines, shipping and tax don't add up to the total the page shows")
	}
	c.Price = Price{Lines: sum, Shipping: ship, Tax: tax, Total: total, Rule: rule.PriceRule, Max: total}
	if rule.PriceRule != nil {
		c.Price.Max += rule.PriceRule.UpTo
	}
	c.Grant = Grant{Kind: "single_use_card", Cap: c.Price.Max, Categories: rule.Categories, Uses: 1}
	if err := c.Check(); err != nil {
		return Case{}, err
	}
	return c, nil
}

func optional(s, cur string) (int64, error) {
	if strings.TrimSpace(s) == "" {
		return 0, nil
	}
	return MinorUnits(s, cur)
}

// zeroDecimal are the currencies with no minor unit.
var zeroDecimal = map[string]bool{"JPY": true, "KRW": true, "VND": true, "CLP": true, "ISK": true}

// MinorUnits reads a decimal string as the page shows it ("84.20", "84,20")
// into the currency's minor units, exactly: never through a float. More
// decimals than the currency has is refused.
func MinorUnits(s, currency string) (int64, error) {
	s = strings.TrimSpace(strings.ReplaceAll(s, ",", "."))
	digits := 2
	if zeroDecimal[currency] {
		digits = 0
	}
	whole, frac, _ := strings.Cut(s, ".")
	if whole == "" || strings.Trim(whole, "0123456789") != "" || strings.Trim(frac, "0123456789") != "" || len(frac) > digits {
		return 0, errors.New("purchase: not an amount")
	}
	frac += strings.Repeat("0", digits-len(frac))
	v, err := strconv.ParseInt(whole+frac, 10, 64)
	if err != nil || v < 0 {
		return 0, errors.New("purchase: not an amount")
	}
	return v, nil
}

// NewCaseID is a new case's id.
func NewCaseID() string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return "pc_" + hex.EncodeToString(b[:])
}

// onSite says whether host is site or a host under it.
func onSite(host, site string) bool {
	host, site = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), ".")), strings.ToLower(strings.TrimSpace(site))
	return host != "" && site != "" && (host == site || strings.HasSuffix(host, "."+site))
}
