package handlers

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/mandate"
	"github.com/kiff/cloud/api/internal/permit"
	"github.com/kiff/cloud/api/internal/purchase"
	kiffproposal "github.com/kiff/kiff/pkg/kiff/proposal"
)

func checkoutBody(tenant, op string, edit func(map[string]any)) map[string]any {
	b := map[string]any{"v": purchase.CheckoutLabel, "tenant": tenant, "op": op, "grant": "g_web", "site": "papeterie-lumiere.example",
		"url": "https://papeterie-lumiere.example/checkout", "ref": "c7",
		"lines": []map[string]any{{"item": "A4 paper, 80 g, 5 reams", "quantity": 2, "price": "59.80"}, {"item": "Ballpoint pens, blue, box of 50", "quantity": 1, "price": "14.90"}},
		"total": "84.20", "currency": "EUR", "shipping": "9.50",
		"merchant":       map[string]any{"name": "Papeterie Lumière", "country": "FR"},
		"delivery":       map[string]any{"name": "Acme SAS", "line1": "12 rue des Lilas", "postal_code": "69003", "city": "Lyon", "country": "FR"},
		"stripe_profile": "profile_61RkZt0hLumiere", "stripe_profile_from": "papeterie-lumiere.example",
		"read_at": time.Now().UTC().Add(-time.Minute).Format(time.RFC3339)}
	if edit != nil {
		edit(b)
	}
	return b
}

// signUnder signs body as planque does, under label.
func signUnder(t *testing.T, key *ecdsa.PrivateKey, label string, body map[string]any) json.RawMessage {
	t.Helper()
	raw, _ := json.Marshal(body)
	canon, err := permit.Canonicalize(raw)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(append(append([]byte(label), 0), canon...))
	r, s, err := ecdsa.Sign(rand.Reader, key, sum[:])
	if err != nil {
		t.Fatal(err)
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	out, _ := json.Marshal(map[string]any{"block": json.RawMessage(raw), "sig": base64.RawURLEncoding.EncodeToString(sig)})
	return out
}

func checkoutVerifier(k *ecdsa.PublicKey) func(json.RawMessage, string) bool {
	return func(b json.RawMessage, s string) bool { return verifyLabel(k, planqueCheckoutLabel, b, s) }
}

// A checkout block counts only under planque's checkout label, as signed:
// a sealed fill's describe never passes for one, nor one for a describe,
// and a field changed after signing refuses it.
// Category: authority
// Attack: a taf passes a sealed fill's signed describe, or a checkout it edited, as the checkout a purchase is built from
func TestACheckoutBlockIsReadOnlyUnderItsOwnLabel(t *testing.T) {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	body := checkoutBody("t1", "op-pay", nil)
	if _, why := checkoutBlockOf(signUnder(t, key, planqueCheckoutLabel, body), checkoutVerifier(&key.PublicKey)); why != "" {
		t.Fatalf("a good block: %s", why)
	}
	if _, why := checkoutBlockOf(signUnder(t, key, planqueDescribeLabel, body), checkoutVerifier(&key.PublicKey)); why == "" {
		t.Fatal("a block signed as a describe read as a checkout")
	}
	describe := testBlock(t, key, "t1", "p-1", nil)
	if why := browserBlockRefusal(signUnder(t, key, planqueCheckoutLabel, func() map[string]any {
		var s struct{ Block map[string]any }
		_ = json.Unmarshal(describe, &s)
		return s.Block
	}()), "t1", "p-1", func(b json.RawMessage, s string) bool { return verifyWith(&key.PublicKey, b, s) }); why == "" {
		t.Fatal("a describe signed as a checkout read as a sealed fill's block")
	}
	signed := signUnder(t, key, planqueCheckoutLabel, body)
	tampered := strings.Replace(string(signed), `"84.20"`, `"8.42"`, 1)
	if _, why := checkoutBlockOf(json.RawMessage(tampered), checkoutVerifier(&key.PublicKey)); why == "" {
		t.Fatal("a tampered block read")
	}
	extra := checkoutBody("t1", "op-pay", func(b map[string]any) { b["cap"] = 999999 })
	if _, why := checkoutBlockOf(signUnder(t, key, planqueCheckoutLabel, extra), checkoutVerifier(&key.PublicKey)); why == "" {
		t.Fatal("a block with a field KIFF doesn't know read")
	}
}

type fakeOpener struct {
	rows    map[string]purchase.Row
	decided *purchase.Decision
	held    string
	decide  error
}

func (f *fakeOpener) OpenOnce(_ context.Context, c purchase.Case) (purchase.Row, error) {
	if r, ok := f.rows[c.CaseID]; ok {
		return r, nil
	}
	if err := c.Check(); err != nil {
		return purchase.Row{}, err
	}
	d, _ := c.Digest()
	r := purchase.Row{Case: c, Digest: d, State: purchase.StateHeld}
	f.rows[c.CaseID] = r
	return r, nil
}
func (f *fakeOpener) Decide(_ context.Context, _, caseID string, _ int, _ string, d purchase.Decision) error {
	if f.decide != nil {
		return f.decide
	}
	f.decided = &d
	r := f.rows[caseID]
	r.State = purchase.StateApproved
	f.rows[caseID] = r
	return nil
}
func (f *fakeOpener) Hold(_ context.Context, _, _ string, _ int, exceptionID string) error {
	f.held = exceptionID
	return nil
}
func (f *fakeOpener) Latest(_ context.Context, _, caseID string) (purchase.Row, error) {
	return f.rows[caseID], nil
}

type fakeRules struct{ rule *mandate.PurchaseRule }

func (f fakeRules) PurchaseRule(context.Context, string, string, string, string) (mandate.Mandate, int, mandate.PurchaseRule, bool, error) {
	if f.rule == nil {
		return mandate.Mandate{}, 0, mandate.PurchaseRule{}, false, nil
	}
	return mandate.Mandate{ID: "card-buy", Location: "Europe/Paris"}, 3, *f.rule, true, nil
}

// A pay opens one case from planque's checkout: within its Card's limits
// it's decided by KIFF, asked each time it waits for the owner; a retry of
// the same pay finds its case; a card fill outside test mode, a Card with
// no purchase rule, another pay's block or a stale read opens nothing.
func TestAPayOpensItsCaseFromTheCheckout(t *testing.T) {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	id := auth.Identity{Tenant: "t1", Subject: "agt_buy"}
	prop := kiffproposal.ActionProposal{ID: "op-pay", ActorID: "agt_buy", ActionName: "browse_pay_web"}
	within := &mandate.PurchaseRule{Mode: mandate.PurchaseWithinLimits, Currency: "EUR", PerPurchase: 10000, PerDay: 50000, PerHour: 3, PerDayCount: 5,
		Categories: []string{"stationery_office_supplies_printing_and_writing_paper"}, PriceRule: &mandate.PurchasePriceRule{UpTo: 200, For: []string{"shipping"}}}
	ask := *within
	ask.Mode = mandate.PurchaseAskEachTime
	rig := func(rule *mandate.PurchaseRule, cardFill bool) (*ProposalsHandler, *fakeOpener) {
		f := &fakeOpener{rows: map[string]purchase.Row{}}
		return &ProposalsHandler{logger: slog.New(slog.NewTextHandler(io.Discard, nil)), tenants: noRuntime{},
			purchaseOpening: &purchaseOpening{cases: f, rules: fakeRules{rule}, cardFill: cardFill, verify: checkoutVerifier(&key.PublicKey)}}, f
	}
	block := signUnder(t, key, planqueCheckoutLabel, checkoutBody("t1", "op-pay", nil))
	// The relay's connection: planque's account t1, grant g_web.
	aud := "https://planque.example/mcp/t1/g_web"
	pay := func(aud string) *permit.Request {
		return &permit.Request{Tool: purchase.ToolBrowsePay, Audience: aud,
			Arguments: json.RawMessage(`{"url":"https://papeterie-lumiere.example/checkout","ref":"c7","planque_op":"op-pay"}`)}
	}

	h, f := rig(within, false)
	o, resp, stop := h.openPurchase(context.Background(), id, prop, pay(aud), block)
	if stop || o == nil || !o.decided || f.decided == nil || f.decided.By != purchase.ByCardLimits || f.decided.Limits.Location != "Europe/Paris" {
		t.Fatalf("within limits: %+v %+v %+v", o, resp, f.decided)
	}
	c := o.row.Case
	if c.CaseID != purchase.CaseIDFor("t1", "op-pay") || c.Path != "token" || c.Merchant.StripeProfile != "profile_61RkZt0hLumiere" || c.Price.Total != 8420 ||
		c.Card != (purchase.CardRef{ID: "card-buy", Version: 3}) || c.Reference.Request != "c7" || c.Reference.Checkout != "https://papeterie-lumiere.example/checkout" {
		t.Fatalf("case %+v", c)
	}
	// The same pay again: the same case, decided, never a second one.
	o2, _, _ := h.openPurchase(context.Background(), id, prop, pay(aud), block)
	if o2.row.Case.CaseID != c.CaseID || !o2.decided || len(f.rows) != 1 {
		t.Fatalf("a retry: %+v", o2)
	}

	h, f = rig(&ask, false)
	o, _, stop = h.openPurchase(context.Background(), id, prop, pay(aud), block)
	if stop || o.decided || f.decided != nil || ownerAskFrom(withPurchaseOpened(context.Background(), o)) == "" {
		t.Fatalf("asked each time: %+v", o)
	}
	resp = proposalResponse{Outcome: string(outcomeOwnerRequired), Exception: &exceptionHandle{ID: "exc_9"}}
	h.afterPurchase(context.Background(), "t1", o, &resp)
	if f.held != "exc_9" || resp.Purchase == nil || resp.Purchase.CaseID != o.row.Case.CaseID {
		t.Fatalf("the hold's link: %q %+v", f.held, resp.Purchase)
	}

	h, f = rig(within, false)
	f.decide = purchase.ErrOutsideLimits
	if o, _, _ := h.openPurchase(context.Background(), id, prop, pay(aud), block); o == nil || o.decided {
		t.Fatalf("outside the limits is the owner's: %+v", o)
	}

	for name, c := range map[string]struct {
		rule     *mandate.PurchaseRule
		cardFill bool
		block    json.RawMessage
		reason   string
	}{
		"a card fill outside test mode": {within, false, signUnder(t, key, planqueCheckoutLabel, checkoutBody("t1", "op-pay", func(b map[string]any) {
			delete(b, "stripe_profile")
			delete(b, "stripe_profile_from")
		})), reasonPurchaseCheckout},
		"no purchase rule":         {nil, false, block, reasonPurchaseNoRule},
		"another pay's block":      {within, false, signUnder(t, key, planqueCheckoutLabel, checkoutBody("t1", "op-other", nil)), reasonPurchaseCheckout},
		"another account's":        {within, false, signUnder(t, key, planqueCheckoutLabel, checkoutBody("t2", "op-pay", nil)), reasonPurchaseCheckout},
		"a read past 15 minutes":   {within, false, signUnder(t, key, planqueCheckoutLabel, checkoutBody("t1", "op-pay", func(b map[string]any) { b["read_at"] = time.Now().Add(-20 * time.Minute).UTC().Format(time.RFC3339) })), reasonPurchaseCheckout},
		"no block":                 {within, false, nil, reasonPurchaseCheckout},
		"totals that don't add up": {within, false, signUnder(t, key, planqueCheckoutLabel, checkoutBody("t1", "op-pay", func(b map[string]any) { b["total"] = "84.21" })), reasonPurchaseCheckout},
	} {
		h, f := rig(c.rule, c.cardFill)
		o, resp, stop := h.openPurchase(context.Background(), id, prop, pay(aud), c.block)
		if !stop || o != nil || len(resp.Reasons) != 1 || resp.Reasons[0] != c.reason || len(f.rows) != 0 {
			t.Errorf("%s: %v %+v %d", name, stop, resp, len(f.rows))
		}
	}
	// A block from another connection's describe, or a pay whose audience
	// names no relay grant, opens nothing.
	for name, c := range map[string]struct {
		aud   string
		block json.RawMessage
	}{
		"another connection's checkout": {aud, signUnder(t, key, planqueCheckoutLabel, checkoutBody("t1", "op-pay", func(b map[string]any) { b["grant"] = "g_other" }))},
		"another grant's audience":      {"https://planque.example/mcp/t1/g_other", block},
		"another account's audience":    {"https://planque.example/mcp/t2/g_web", block},
		"an audience naming no grant":   {"planque", block},
		"another page's checkout":       {aud, signUnder(t, key, planqueCheckoutLabel, checkoutBody("t1", "op-pay", func(b map[string]any) { b["url"] = "https://papeterie-lumiere.example/other" }))},
		"another control's checkout":    {aud, signUnder(t, key, planqueCheckoutLabel, checkoutBody("t1", "op-pay", func(b map[string]any) { b["ref"] = "c8" }))},
	} {
		h, f := rig(within, false)
		o, resp, stop := h.openPurchase(context.Background(), id, prop, pay(c.aud), c.block)
		if !stop || o != nil || len(resp.Reasons) != 1 || resp.Reasons[0] != reasonPurchaseCheckout || len(f.rows) != 0 {
			t.Errorf("%s: %v %+v %d", name, stop, resp, len(f.rows))
		}
	}
	// In test mode a card fill opens its case.
	h, _ = rig(within, true)
	noToken := signUnder(t, key, planqueCheckoutLabel, checkoutBody("t1", "op-pay", func(b map[string]any) {
		delete(b, "stripe_profile")
		delete(b, "stripe_profile_from")
	}))
	if o, resp, stop := h.openPurchase(context.Background(), id, prop, pay(aud), noToken); stop || o.row.Case.Path != "card_fill" {
		t.Fatalf("a card fill in test mode: %+v", resp)
	}
}

// A describe is no pay: it opens no case. It's told by the gateway's id,
// never by an argument, whatever the relay calls its operation.
func TestADescribeIsNoPay(t *testing.T) {
	pay := &permit.Request{Tool: purchase.ToolBrowsePay, Arguments: json.RawMessage(`{"url":"https://x.example/checkout","ref":"c7","planque_op":"tc-1"}`)}
	describe := &permit.Request{Tool: purchase.ToolBrowsePay, Arguments: json.RawMessage(`{"url":"https://x.example/checkout","ref":"c7","planque_op":"tc-1:describe"}`)}
	other := &permit.Request{Tool: "browse_open", Arguments: json.RawMessage(`{}`)}
	if !isPay(pay, "tc-1") || isPay(describe, "tc-1:describe") || isPay(other, "tc-1") || isPay(nil, "tc-1") {
		t.Fatal("isPay")
	}
	// A pay whose arguments name a describe is still a pay.
	if !isPay(describe, "tc-1") {
		t.Fatal("a pay passed as a describe")
	}
}
