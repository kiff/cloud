// Package purchase is KIFF's purchase case (contracts/purchase.json in
// kiff/tafs): a purchase built from what KIFF and the relay read at a
// checkout, never from the agent's words; decided once, by the owner's
// signature or within a Card's limits, with its room reserved in the same
// transaction; executed at most once per version under a lease; approved at
// most once at the issuer's live authorization; reconciled; and receipted.
package purchase

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"

	"github.com/kiff/cloud/api/internal/permit"
)

// Schema is the case's schema id, signed with it.
const Schema = "kiff.purchase.v1"

// ValidFor is how long an approved, unused case holds: 15 minutes from its
// decision. It is signed, and never anything else.
const ValidFor = 900

// Case is every signed field of a purchase case. Its digest is SHA-256 of
// its canonical JSON (RFC 8785): any change to a field is a new version.
type Case struct {
	Schema          string    `json:"schema"`
	CaseID          string    `json:"case_id"`
	Version         int       `json:"version"`
	Tenant          string    `json:"tenant"`
	Agent           string    `json:"agent"`
	Card            CardRef   `json:"card"`
	Mode            string    `json:"mode"`
	Route           string    `json:"route"`
	Path            string    `json:"path"`
	Merchant        Merchant  `json:"merchant"`
	Lines           []Line    `json:"lines"`
	Currency        string    `json:"currency"`
	Price           Price     `json:"price"`
	Delivery        Address   `json:"delivery"`
	ValidForSeconds int       `json:"valid_for_seconds"`
	Reference       Reference `json:"reference"`
	Payer           Payer     `json:"payer"`
	Markets         Markets   `json:"markets"`
	Grant           Grant     `json:"grant"`
}

// CardRef is the Card that allows the purchase, at the version it had.
type CardRef struct {
	ID      string `json:"id"`
	Version int    `json:"version"`
}

// Merchant is the shop as the checkout read named it.
type Merchant struct {
	Name     string `json:"name"`
	Site     string `json:"site"`
	Category string `json:"category"`
	Country  string `json:"country"`
	Match    Match  `json:"match"`
	// StripeProfile is the seller's Stripe profile a token names
	// (profile_…): read only from the checkout's own site, in the relay's
	// signed block, never from the taf. A token case needs it; a card fill
	// has none.
	StripeProfile string `json:"stripe_profile,omitempty"`
}

// Match is how the live authorization knows it's this shop, not only this
// kind of shop: the issuer's network merchant id when the case has one,
// otherwise the issuer's merchant name, folded, containing NameKey.
type Match struct {
	NameKey   string `json:"name_key"`
	NetworkID string `json:"network_id"`
}

// Line is one line of the cart as the checkout read it, its amount in the
// currency's minor units.
type Line struct {
	Name     string `json:"name"`
	Quantity int    `json:"quantity"`
	Amount   int64  `json:"amount"`
}

// Price is the checkout's total and the rule the Card allows on it. Max is
// what is reserved and what the card's cap is.
type Price struct {
	Lines    int64      `json:"lines"`
	Shipping int64      `json:"shipping"`
	Tax      int64      `json:"tax"`
	Total    int64      `json:"total"`
	Rule     *PriceRule `json:"rule"`
	Max      int64      `json:"max"`
}

// PriceRule is how much more the total may be, and for what, without a new
// decision: the Card's own, copied at the case.
type PriceRule struct {
	UpTo int64    `json:"up_to"`
	For  []string `json:"for"`
}

// Address is where the goods go. It is signed: a page whose address differs
// sends nothing.
type Address struct {
	Name       string `json:"name"`
	Line1      string `json:"line1"`
	Line2      string `json:"line2"`
	PostalCode string `json:"postal_code"`
	City       string `json:"city"`
	Region     string `json:"region"`
	Country    string `json:"country"`
}

// Reference is the checkout session and the request the purchase answers.
type Reference struct {
	Checkout string `json:"checkout"`
	Request  string `json:"request"`
}

// Payer is who pays: the account holder or a company.
type Payer struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

// Markets are the payer's and the merchant's countries, kept apart.
type Markets struct {
	Payer    string `json:"payer"`
	Merchant string `json:"merchant"`
}

// Grant is the single-use card the case allows: capped at Price.Max, in the
// merchant's categories, used once.
type Grant struct {
	Kind       string   `json:"kind"`
	Cap        int64    `json:"cap"`
	Categories []string `json:"categories"`
	Uses       int      `json:"uses"`
}

var (
	modes  = []string{"ask_each_time", "within_limits"}
	routes = []string{"issued_card", "existing_method"}
	paths  = []string{"token", "card_fill", "none"}
	payers = []string{"account", "company"}
	grants = []string{"single_use_card", "existing_method"}
)

// stripeProfile is a Stripe profile id as a token names its seller.
var stripeProfile = regexp.MustCompile(`^profile_[A-Za-z0-9]{1,64}$`)

// ErrCase: the case breaks one of its own rules.
var ErrCase = errors.New("purchase: the case isn't one KIFF signs")

// Check says why a case can't be signed, or nil: its enums, its amounts
// (integers in minor units, the total the sum of its parts, the maximum the
// total plus the rule, the card's cap that maximum), one use, and 15
// minutes.
func (c Case) Check() error {
	bad := func(why string, a ...any) error { return fmt.Errorf("%w: "+why, append([]any{ErrCase}, a...)...) }
	if c.Schema != Schema {
		return bad("schema %q", c.Schema)
	}
	if c.CaseID == "" || c.Version < 1 || c.Tenant == "" || c.Agent == "" || c.Card.ID == "" {
		return bad("its ids and version are required")
	}
	for name, v := range map[string][2]any{"mode": {c.Mode, modes}, "route": {c.Route, routes}, "path": {c.Path, paths}, "payer.kind": {c.Payer.Kind, payers}, "grant.kind": {c.Grant.Kind, grants}} {
		if !oneOf(v[0].(string), v[1].([]string)) {
			return bad("%s %q", name, v[0])
		}
	}
	if len(c.Lines) == 0 {
		return bad("no lines")
	}
	var sum int64
	for _, l := range c.Lines {
		if l.Quantity < 1 || l.Amount <= 0 || strings.TrimSpace(l.Name) == "" {
			return bad("a line %q", l.Name)
		}
		sum += l.Amount
	}
	p := c.Price
	if p.Lines != sum || p.Shipping < 0 || p.Tax < 0 || p.Total != p.Lines+p.Shipping+p.Tax || p.Total <= 0 {
		return bad("the total isn't its lines, shipping and tax")
	}
	upTo := int64(0)
	if p.Rule != nil {
		if p.Rule.UpTo <= 0 || len(p.Rule.For) == 0 {
			return bad("a price rule with nothing in it")
		}
		upTo = p.Rule.UpTo
	}
	if p.Max != p.Total+upTo || c.Grant.Cap != p.Max {
		return bad("the maximum isn't the total plus its rule, or the card's cap isn't it")
	}
	if c.Grant.Uses != 1 || len(c.Grant.Categories) == 0 {
		return bad("the card isn't single-use in the merchant's categories")
	}
	if c.ValidForSeconds != ValidFor {
		return bad("valid_for_seconds %d", c.ValidForSeconds)
	}
	if len(c.Currency) != 3 || c.Currency != strings.ToUpper(c.Currency) {
		return bad("currency %q", c.Currency)
	}
	if c.Delivery.Line1 == "" || c.Delivery.Country == "" {
		return bad("no delivery address")
	}
	// v1 builds the issued card alone: the customer's own method has its
	// own risks, and no design yet.
	if c.Route != "issued_card" || c.Grant.Kind != "single_use_card" || c.Path == "none" {
		return bad("v1 buys only with a single-use issued card, by token or card fill")
	}
	switch {
	case c.Path == "token" && !stripeProfile.MatchString(c.Merchant.StripeProfile):
		return bad("a token case names the seller's Stripe profile")
	case c.Path != "token" && c.Merchant.StripeProfile != "":
		return bad("only a token case names a Stripe profile")
	}
	if c.Merchant.Match.NameKey == "" || c.Merchant.Match.NameKey != Fold(c.Merchant.Name) || c.Merchant.Country == "" {
		return bad("the merchant's match isn't its folded name")
	}
	return nil
}

// Digest is the case's SHA-256, hex, over its canonical JSON: what the
// owner signs and every side carries.
func (c Case) Digest() (string, error) {
	b, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	return permit.ArgsSHA256(b)
}

// Fold is a merchant name as the live authorization compares it: accents
// removed, lower case, letters and digits only.
func Fold(name string) string {
	var b strings.Builder
	for _, r := range norm.NFD.String(name) {
		switch {
		case unicode.Is(unicode.Mn, r):
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(unicode.ToLower(r))
		}
	}
	return b.String()
}

func oneOf(s string, list []string) bool {
	for _, x := range list {
		if s == x {
			return true
		}
	}
	return false
}

// LimitsFrom is a Card's purchase rule as a decision checks it.
func LimitsFrom(perPurchase, perDay int64, perHour, perDayCount int, categories []string) Limits {
	return Limits{PerPurchase: perPurchase, PerDay: perDay, PerHourCount: perHour, PerDayCount: perDayCount, Categories: categories}
}
