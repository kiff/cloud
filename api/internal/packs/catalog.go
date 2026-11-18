// Package packs is the tafs task packs: the five one-time top-ups an
// account buys, through Stripe on kiff.dev or Apple or Google Play in the
// app, and the
// ledger of what each pack has left. A taf's task counts against what the
// plan includes this period first, then against pack tasks, oldest first
// (the per-task overage, retired).
//
// Names, task counts and the store ids live in the pack table (the JSON
// in KIFF_TASK_PACKS, else catalog.json); prices live in Stripe's catalog
// and App Store Connect. Nothing here knows a price.
package packs

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

//go:embed catalog.json
var defaultCatalog []byte

// EnvCatalog names a file holding the pack table, in catalog.json's shape.
// Unset: the table built in.
const EnvCatalog = "KIFF_TASK_PACKS"

// How long tasks last: bought ones a year from purchase, bonus ones KIFF
// grants 90 days.
const (
	BoughtMonths = 12
	BonusDays    = 90
)

// Pack is one pack in the table.
type Pack struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Tasks int    `json:"tasks"`
	// Credits are a pack of credits': a pack has tasks or credits, never
	// both. Packs of credits never expire.
	Credits int64 `json:"credits,omitempty"`
	// Once marks a pack of credits an account is offered until it has
	// bought it once (the First month pack): it leaves the store's list
	// for that account then (CreditLedger's BoughtPack). A receipt for a
	// second one is still recorded, since the customer paid.
	Once bool `json:"once,omitempty"`
	// StripePrice is the lookup key of its one-time Stripe price, when
	// kiff.dev sells it (a pack of credits, which only the app stores sell,
	// never has one);
	// AppleProduct its consumable's product id in App Store Connect (empty,
	// for a pack of credits only: not sold on the App Store yet);
	// GoogleProduct its one-time product's id in the Play Console (empty:
	// not sold on Google Play).
	StripePrice   string `json:"stripe_price"`
	AppleProduct  string `json:"apple_product"`
	GoogleProduct string `json:"google_product,omitempty"`
	// Amount and Currency are read by the Stripe catalog script only, to
	// create the price, VAT included; KIFF never shows or charges them
	// itself.
	Amount   int    `json:"amount,omitempty"`
	Currency string `json:"currency,omitempty"`
}

// Line is how a pack is shown: a pack of tasks' name and tasks, "Rayo · 70
// tasks"; a pack of credits' credits alone, "1,500 credits", as the stores
// title it.
func (p Pack) Line() string {
	if p.Credits > 0 && p.Once {
		// Named, since its credits alone don't tell it from the packs
		// beside it.
		return p.Name + " · " + grouped(p.Credits) + " credits"
	}
	if p.Credits > 0 {
		return grouped(p.Credits) + " credits"
	}
	return fmt.Sprintf("%s · %d tasks", p.Name, p.Tasks)
}

// grouped writes n with a comma every three digits: 1500 is "1,500".
func grouped(n int64) string {
	s := strconv.FormatInt(n, 10)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

// soldAsTasks are the store products already sold as packs of tasks. A
// receipt for one is tasks whatever the table says later, so the table
// never makes one a pack of credits.
var soldAsTasks = map[string]bool{
	"bot.tafs.pack.pico": true, "bot.tafs.pack.vonk": true, "bot.tafs.pack.rayo": true,
	"bot.tafs.pack.forte": true, "bot.tafs.pack.maxi": true,
	"kiff_pack_pico": true, "kiff_pack_vonk": true, "kiff_pack_rayo": true,
	"kiff_pack_forte": true, "kiff_pack_maxi": true,
}

// Catalog is the pack table, and the one plan the app stores may bill.
type Catalog struct {
	Solo struct {
		// AppleProduct is Solo's auto-renewable subscription in App Store
		// Connect: the only plan Apple bills. GoogleProduct is Solo's
		// subscription in the Play Console, the only plan Google Play
		// bills; empty: not sold there.
		AppleProduct  string `json:"apple_product"`
		GoogleProduct string `json:"google_product,omitempty"`
	} `json:"solo"`
	Packs []Pack `json:"packs"`
}

// LoadCatalog reads the table from EnvCatalog's file, else the built-in one.
func LoadCatalog() (Catalog, error) {
	raw := defaultCatalog
	if path := strings.TrimSpace(os.Getenv(EnvCatalog)); path != "" {
		b, err := os.ReadFile(path)
		if err != nil {
			return Catalog{}, fmt.Errorf("packs: read %s: %w", EnvCatalog, err)
		}
		raw = b
	}
	return ParseCatalog(raw)
}

// ParseCatalog reads a table and checks it: every pack has an id, a name,
// tasks or credits, and its store ids, and no id is used twice. A pack
// with a Stripe price is sold on kiff.dev; none is now: packs of tasks are
// no longer sold, and are kept so the stores' late receipts and refunds of
// them still map to tasks. A pack of credits is Solo's, sold in the app
// stores only, never with a Stripe price, and it is never a product
// already sold as a pack of tasks.
func ParseCatalog(raw []byte) (Catalog, error) {
	var c Catalog
	if err := json.Unmarshal(raw, &c); err != nil {
		return Catalog{}, fmt.Errorf("packs: catalog: %w", err)
	}
	seen := map[string]bool{}
	for _, p := range c.Packs {
		switch {
		case p.ID == "" || p.Name == "" || (p.Tasks <= 0) == (p.Credits <= 0):
			return Catalog{}, fmt.Errorf("packs: catalog: pack %q is missing its id, name, or tasks or credits (one of them)", p.ID)
		case p.AppleProduct == "" && (p.Credits <= 0 || p.GoogleProduct == ""):
			// A pack of credits may be on one store before the other; a
			// pack of tasks always had its Apple product.
			return Catalog{}, fmt.Errorf("packs: catalog: pack %q is missing its store ids", p.ID)
		case p.Once && p.Credits <= 0:
			return Catalog{}, fmt.Errorf("packs: catalog: pack %q is sold once per account but isn't a pack of credits", p.ID)
		case p.Credits > 0 && p.StripePrice != "":
			return Catalog{}, fmt.Errorf("packs: catalog: pack of credits %q has a Stripe price; it is sold in the app stores only", p.ID)
		case p.Credits > 0 && (soldAsTasks[p.AppleProduct] || soldAsTasks[p.GoogleProduct]):
			return Catalog{}, fmt.Errorf("packs: catalog: pack of credits %q uses a product already sold as a pack of tasks", p.ID)
		case seen[p.ID] || (p.StripePrice != "" && seen["s:"+p.StripePrice]) || (p.AppleProduct != "" && seen["a:"+p.AppleProduct]) || (p.GoogleProduct != "" && seen["g:"+p.GoogleProduct]):
			return Catalog{}, fmt.Errorf("packs: catalog: pack %q repeats an id", p.ID)
		case p.GoogleProduct != "" && p.GoogleProduct == c.Solo.GoogleProduct:
			return Catalog{}, fmt.Errorf("packs: catalog: pack %q is sold on Google Play as Solo", p.ID)
		}
		seen[p.ID] = true
		if p.AppleProduct != "" {
			seen["a:"+p.AppleProduct] = true
		}
		if p.StripePrice != "" {
			seen["s:"+p.StripePrice] = true
		}
		if p.GoogleProduct != "" {
			seen["g:"+p.GoogleProduct] = true
		}
	}
	if len(c.Packs) == 0 {
		return Catalog{}, fmt.Errorf("packs: catalog has no packs")
	}
	return c, nil
}

// CreditPacks are the packs of credits, in the table's order.
func (c Catalog) CreditPacks() []Pack {
	out := []Pack{}
	for _, p := range c.Packs {
		if p.Credits > 0 {
			out = append(out, p)
		}
	}
	return out
}

// Offered are the packs of credits an account is offered, in the table's
// order: all of them, less each once-only pack bought says it has bought.
func (c Catalog) Offered(bought func(pack string) bool) []Pack {
	out := []Pack{}
	for _, p := range c.CreditPacks() {
		if p.Once && bought != nil && bought(p.ID) {
			continue
		}
		out = append(out, p)
	}
	return out
}

// OncePacks are the packs of credits sold once per account.
func (c Catalog) OncePacks() []Pack {
	out := []Pack{}
	for _, p := range c.CreditPacks() {
		if p.Once {
			out = append(out, p)
		}
	}
	return out
}

// Pack is the pack with id.
func (c Catalog) Pack(id string) (Pack, bool) {
	for _, p := range c.Packs {
		if p.ID == id {
			return p, true
		}
	}
	return Pack{}, false
}

// ByAppleProduct is the pack sold as Apple product id.
func (c Catalog) ByAppleProduct(id string) (Pack, bool) {
	for _, p := range c.Packs {
		if id != "" && p.AppleProduct == id {
			return p, true
		}
	}
	return Pack{}, false
}

// ByGoogleProduct is the pack sold on Google Play as product id.
func (c Catalog) ByGoogleProduct(id string) (Pack, bool) {
	for _, p := range c.Packs {
		if id != "" && p.GoogleProduct == id {
			return p, true
		}
	}
	return Pack{}, false
}

// Expiry is when tasks of kind granted at at stop counting.
func Expiry(kind Kind, at time.Time) time.Time {
	if kind == KindBonus {
		return at.AddDate(0, 0, BonusDays)
	}
	return at.AddDate(0, BoughtMonths, 0)
}
