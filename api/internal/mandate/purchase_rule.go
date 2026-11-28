package mandate

import (
	"fmt"
	"slices"
	"strings"
)

// PurchaseRule is how a purchase under a grant is decided (Thune): every
// purchase asked of the owner, or, in "Buy within these limits", decided by
// KIFF when it's inside each limit, and held for the owner when it isn't.
// Buying moves money, so the grant must open money (K4). Amounts are in
// Currency's minor units.
type PurchaseRule struct {
	Mode     string
	Currency string
	// PerPurchase and PerDay bound one purchase and a day's purchases;
	// PerHour and PerDayCount count them, so a loop of small buys can't
	// drain the day. Within limits needs both amounts and a count.
	PerPurchase int64 `json:",omitempty"`
	PerDay      int64 `json:",omitempty"`
	PerHour     int   `json:",omitempty"`
	PerDayCount int   `json:",omitempty"`
	// Categories are the issuer's merchant categories purchases may be in.
	Categories []string `json:",omitempty"`
	// PriceRule is how much more than the shown total a purchase may be
	// without a new decision, and for what; nil is none (the default).
	PriceRule *PurchasePriceRule `json:",omitempty"`
}

// PurchasePriceRule is the Card's price rule, copied into each case.
type PurchasePriceRule struct {
	UpTo int64
	For  []string
}

// Purchase modes. Asking each time is every new Card's default.
const (
	PurchaseAskEachTime  = "ask_each_time"
	PurchaseWithinLimits = "within_limits"
)

// Bounds on a purchase rule.
const (
	MaxPurchaseCategories = 20
	MaxPriceRuleFor       = 3
)

func validatePurchase(action string, g Grant) error {
	p := g.Purchase
	if p == nil {
		return nil
	}
	bad := func(why string) error { return fmt.Errorf("%w: %s's purchase rule: %s", ErrInvalidMandate, action, why) }
	if !slices.ContainsFunc(g.Opens, func(o Opening) bool { return o.Class == ClassMoney }) {
		return bad("buying moves money, so the grant must open money")
	}
	if len(p.Currency) != 3 || p.Currency != strings.ToUpper(p.Currency) {
		return bad("its currency isn't a currency code")
	}
	if p.PerPurchase < 0 || p.PerDay < 0 || p.PerHour < 0 || p.PerDayCount < 0 {
		return bad("a limit below zero")
	}
	if len(p.Categories) == 0 || len(p.Categories) > MaxPurchaseCategories {
		return bad(fmt.Sprintf("it names 1 to %d categories", MaxPurchaseCategories))
	}
	switch p.Mode {
	case PurchaseAskEachTime:
	case PurchaseWithinLimits:
		if p.PerPurchase == 0 || p.PerDay == 0 || (p.PerHour == 0 && p.PerDayCount == 0) || p.PerPurchase > p.PerDay {
			return bad("buying within limits needs a per-purchase limit, a day's limit at least that, and a count")
		}
	default:
		return bad(fmt.Sprintf("mode %q", p.Mode))
	}
	if r := p.PriceRule; r != nil && (r.UpTo <= 0 || len(r.For) == 0 || len(r.For) > MaxPriceRuleFor) {
		return bad("a price rule names an amount and what it's for")
	}
	return nil
}

// purchaseWidenings says how next's purchase rules widen cur's, per
// action: a rule where there was none, asking turned into buying within
// limits, a limit raised or removed, a category or a price rule added or
// raised, or another currency.
func purchaseWidenings(cur, next []Grant) []string {
	was := map[string]*PurchaseRule{}
	for _, g := range cur {
		if g.Purchase != nil {
			was[g.Action] = g.Purchase
		}
	}
	var out []string
	for _, g := range next {
		n := g.Purchase
		if n == nil {
			continue
		}
		w := was[g.Action]
		if w == nil {
			out = append(out, fmt.Sprintf("Lets %s buy", g.Action))
			continue
		}
		if w.Mode == PurchaseAskEachTime && n.Mode == PurchaseWithinLimits {
			out = append(out, fmt.Sprintf("Lets %s buy within its limits without asking you", g.Action))
		}
		if n.Currency != w.Currency {
			out = append(out, fmt.Sprintf("Lets %s buy in %s", g.Action, n.Currency))
		}
		for _, l := range []struct {
			name     string
			was, now int64
		}{
			{"per purchase", w.PerPurchase, n.PerPurchase},
			{"a day", w.PerDay, n.PerDay},
			{"purchases an hour", int64(w.PerHour), int64(n.PerHour)},
			{"purchases a day", int64(w.PerDayCount), int64(n.PerDayCount)},
		} {
			if l.was > 0 && (l.now == 0 || l.now > l.was) {
				out = append(out, fmt.Sprintf("Raises %s's limit %s", g.Action, l.name))
			}
		}
		for _, c := range n.Categories {
			if !slices.Contains(w.Categories, c) {
				out = append(out, fmt.Sprintf("Lets %s buy in %s", g.Action, c))
			}
		}
		switch {
		case n.PriceRule == nil:
		case w.PriceRule == nil || n.PriceRule.UpTo > w.PriceRule.UpTo:
			out = append(out, fmt.Sprintf("Lets %s's purchases go up to %d more than shown", g.Action, n.PriceRule.UpTo))
		default:
			for _, f := range n.PriceRule.For {
				if !slices.Contains(w.PriceRule.For, f) {
					out = append(out, fmt.Sprintf("Lets %s's purchases go up for %s", g.Action, f))
				}
			}
		}
	}
	return out
}
