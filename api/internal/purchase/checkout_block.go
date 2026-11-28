package purchase

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

// CheckoutLabel is what planque signs a checkout block under, apart from
// its sealed fill's describe, so neither passes for the other.
const CheckoutLabel = "planque/checkout/v1"

// ToolBrowsePay is the relay's call that pays a checkout: deciding it opens
// its purchase case.
const ToolBrowsePay = "browse_pay"

// CheckoutBlock is planque's signed account of the checkout a pay acts on:
// what it read at browse_checkout and kept, for this account, this pay and
// this grant. A case is built from it alone, never from the taf's words.
type CheckoutBlock struct {
	V                 string         `json:"v"`
	Tenant            string         `json:"tenant"`
	Op                string         `json:"op"`
	Grant             string         `json:"grant"`
	Site              string         `json:"site"`
	URL               string         `json:"url"`
	Ref               string         `json:"ref"`
	Lines             []CheckoutLine `json:"lines"`
	Total             string         `json:"total"`
	Currency          string         `json:"currency"`
	Shipping          string         `json:"shipping,omitempty"`
	Tax               string         `json:"tax,omitempty"`
	Merchant          *BlockMerchant `json:"merchant,omitempty"`
	Delivery          *Address       `json:"delivery,omitempty"`
	StripeProfile     string         `json:"stripe_profile,omitempty"`
	StripeProfileFrom string         `json:"stripe_profile_from,omitempty"`
	ReadAt            time.Time      `json:"read_at"`
}

// BlockMerchant is the shop as its page names itself.
type BlockMerchant struct {
	Name    string `json:"name"`
	Country string `json:"country,omitempty"`
}

// ErrBlock: a checkout block that isn't this pay's, or is too old.
var ErrBlock = errors.New("purchase: the checkout block isn't this pay's")

// Check says why b can't open a case for the pay op in tenant at now, or
// nil: its label, account and pay, and a read no older than a case's
// time, and not from the future.
func (b CheckoutBlock) Check(tenant, op string, now time.Time) error {
	bad := func(why string) error { return fmt.Errorf("%w: %s", ErrBlock, why) }
	switch {
	case b.V != CheckoutLabel:
		return bad("it isn't " + CheckoutLabel)
	case b.Tenant != tenant:
		return bad("it's for another account")
	case b.Op == "" || b.Op != op:
		return bad("it's for another pay")
	case b.ReadAt.IsZero() || now.Sub(b.ReadAt) > ValidFor*time.Second || b.ReadAt.Sub(now) > time.Minute:
		return bad("its read is older than a case's 15 minutes, or in the future")
	case strings.TrimSpace(b.Site) == "" || strings.TrimSpace(b.Ref) == "":
		return bad("it names no site or no control")
	}
	return nil
}

// Read is the block as a checkout read. The path is the case's to decide:
// a token where the shop's own site names its Stripe profile, else a card
// fill.
func (b CheckoutBlock) Read() CheckoutRead {
	r := CheckoutRead{Site: b.Site, Lines: b.Lines, Shipping: b.Shipping, Tax: b.Tax, Total: b.Total, Currency: b.Currency,
		StripeProfile: b.StripeProfile, StripeProfileFrom: b.StripeProfileFrom, Path: "card_fill"}
	if b.Merchant != nil {
		r.Name, r.Country = b.Merchant.Name, b.Merchant.Country
	}
	if b.Delivery != nil {
		r.Delivery = *b.Delivery
	}
	if b.StripeProfile != "" {
		r.Path = "token"
	}
	return r
}

// CaseIDFor is the one case a pay opens: the same id each time the pay is
// decided, so a retry finds its case, never a second one.
func CaseIDFor(tenant, op string) string {
	sum := sha256.Sum256([]byte("kiff-purchase|" + tenant + "|" + op))
	return "pc_" + hex.EncodeToString(sum[:12])
}

// OpenOnce opens c's first version, or answers the case's latest version
// when the pay opened it already (a retry): one pay, one case.
func (s *Store) OpenOnce(ctx context.Context, c Case) (Row, error) {
	if r, err := s.Latest(ctx, c.Tenant, c.CaseID); err == nil {
		return r, nil
	} else if !errors.Is(err, ErrNoCase) {
		return Row{}, err
	}
	if _, err := s.Open(ctx, c); err != nil {
		// Two at once: the other opened it.
		if r, lerr := s.Latest(ctx, c.Tenant, c.CaseID); lerr == nil {
			return r, nil
		}
		return Row{}, err
	}
	return s.Latest(ctx, c.Tenant, c.CaseID)
}
