package handlers

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/mandate"
	"github.com/kiff/cloud/api/internal/permit"
	"github.com/kiff/cloud/api/internal/purchase"
	"github.com/kiff/cloud/api/internal/words"
	kiffproposal "github.com/kiff/kiff/pkg/kiff/proposal"
)

// A purchase's case, from its pay (Thune). Deciding a relay's browse_pay
// opens the purchase's case from planque's signed checkout block, which the
// gateway asks planque for (a describe) and carries on the decision: never
// from the taf's words. The Card's purchase rule then decides it: within
// its limits, KIFF decides it now, reserving its room in the same
// transaction; otherwise the call is held, and the owner's answer, signed on
// the phone over the case's purchase line, decides it. The answer carries
// the case (purchase: {case_id, version, digest}), so the card and the token
// planque then asks for bind that exact case.

// planqueCheckoutLabel is what planque signs a checkout block under.
const planqueCheckoutLabel = purchase.CheckoutLabel

// PurchaseOpener is the purchase store as a pay's decision uses it.
type PurchaseOpener interface {
	OpenOnce(ctx context.Context, c purchase.Case) (purchase.Row, error)
	Decide(ctx context.Context, tenant, caseID string, version int, digest string, d purchase.Decision) error
	Hold(ctx context.Context, tenant, caseID string, version int, exceptionID string) error
	Latest(ctx context.Context, tenant, caseID string) (purchase.Row, error)
}

// PurchaseRules finds the Card a pay buys under: the Card in force that
// grants agent the action with a purchase rule in currency, and its
// revision.
type PurchaseRules interface {
	PurchaseRule(ctx context.Context, tenant, agent, action, currency string) (card mandate.Mandate, revision int, rule mandate.PurchaseRule, ok bool, err error)
}

// purchaseOpening is the purchase side of a pay's decision.
type purchaseOpening struct {
	cases PurchaseOpener
	rules PurchaseRules
	// cardFill lets a case pay by card fill: only while the issuer is in
	// test mode, since a card number then reaches a worker's browser.
	cardFill bool
	verify   func(json.RawMessage, string) bool
}

// WithProposalsPurchaseOpening turns on opening a purchase case from each
// browse_pay a relay sends (KIFF_PURCHASES). Off, a pay is decided as any
// call is.
func WithProposalsPurchaseOpening(cases PurchaseOpener, rules PurchaseRules, cardFill bool) ProposalsOption {
	return func(h *ProposalsHandler) {
		if cases != nil && rules != nil {
			h.purchaseOpening = &purchaseOpening{cases: cases, rules: rules, cardFill: cardFill, verify: verifyPlanqueCheckout}
		}
	}
}

// openedCase is a pay's case as its decision goes on with it.
type openedCase struct {
	row     purchase.Row
	decided bool
}

// purchaseRef is the case an allowed or held pay names.
type purchaseRef struct {
	CaseID  string `json:"case_id"`
	Version int    `json:"version"`
	Digest  string `json:"digest"`
}

const (
	reasonPurchaseCheckout = "purchase_checkout"
	reasonPurchaseNoRule   = "purchase_no_rule"
	reasonPurchaseCase     = "purchase_case"
)

// isPay says the permit request is a relay's pay (browse_pay), not its
// describe. The gateway sends a describe as the proposal "<call>:describe"
// with that as its op (planque answers it from the checkout it kept, under
// its KIFF-Checkout-Describe header, and acts on nothing). A call's own id
// is the gateway's "tc-<hex>", so a taf can't name a pay as a describe.
func isPay(req *permit.Request, proposalID string) bool {
	if req == nil || req.Tool != purchase.ToolBrowsePay {
		return false
	}
	var a struct {
		Op string `json:"op"`
	}
	_ = json.Unmarshal(req.Arguments, &a)
	return !(strings.HasSuffix(proposalID, ":describe") && a.Op == proposalID)
}

// relayGrant is the planque account and grant a relay's audience names:
// "<planque base>/mcp/<tenant>/<grant>". A checkout block is for the grant
// its describe reached, so a block from one connection's describe can't be
// carried with another connection's pay.
func relayGrant(audience string) (tenant, grant string) {
	u, err := url.Parse(audience)
	if err != nil {
		return "", ""
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 3 || parts[len(parts)-3] != "mcp" || parts[len(parts)-2] == "" || parts[len(parts)-1] == "" {
		return "", ""
	}
	return parts[len(parts)-2], parts[len(parts)-1]
}

// samePage says the pay's own url and ref are the block's: the case binds
// the exact pay, not only its op.
func samePage(args json.RawMessage, b purchase.CheckoutBlock) bool {
	var a struct {
		URL string `json:"url"`
		Ref string `json:"ref"`
	}
	return json.Unmarshal(args, &a) == nil && a.URL != "" && a.URL == b.URL && a.Ref == b.Ref
}

// openPurchase opens the pay's case, or says why it's refused: the response
// to send, and true. Within its Card's limits it's decided here; otherwise
// it waits for the owner (the call is held with the purchase ask).
func (h *ProposalsHandler) openPurchase(ctx context.Context, id auth.Identity, prop kiffproposal.ActionProposal, pay *permit.Request, block json.RawMessage) (*openedCase, proposalResponse, bool) {
	po := h.purchaseOpening
	refuse := func(reason, msg string) (*openedCase, proposalResponse, bool) {
		h.recordRejection(ctx, id, prop, outcomeBlocked, []string{reason})
		return nil, proposalResponse{ProposalID: prop.ID, Outcome: string(outcomeBlocked), Reasons: []string{reason}, Message: msg}, true
	}
	b, why := checkoutBlockOf(block, po.verify)
	if why == "" {
		if err := b.Check(id.Tenant, prop.ID, time.Now()); err != nil {
			why = err.Error()
		} else if tenant, grant := relayGrant(pay.Audience); grant == "" || b.Grant != grant || b.Tenant != tenant {
			why = "it's another connection's checkout"
		} else if !samePage(pay.Arguments, b) {
			why = "it's another page's, or another control's"
		}
	}
	if why != "" {
		return refuse(reasonPurchaseCheckout, "KIFF opens a purchase only from planque's signed read of its checkout, and this one doesn't hold ("+why+"); nothing was bought")
	}
	read := b.Read()
	if read.Path == "card_fill" && !po.cardFill {
		return refuse(reasonPurchaseCheckout, "this shop takes no token, and paying by card fill is for test mode only; nothing was bought")
	}
	card, revision, rule, ok, err := po.rules.PurchaseRule(ctx, id.Tenant, prop.ActorID, prop.ActionName, read.Currency)
	if err != nil {
		return refuse(reasonPurchaseCase, "KIFF couldn't read the Card this purchase is under; nothing was bought")
	}
	if !ok {
		return refuse(reasonPurchaseNoRule, "no Card in force lets this agent buy in "+read.Currency+"; nothing was bought")
	}
	cr := purchase.CardRule{Card: purchase.CardRef{ID: card.ID, Version: revision}, Mode: rule.Mode, Categories: rule.Categories}
	if rule.PriceRule != nil {
		cr.PriceRule = &purchase.PriceRule{UpTo: rule.PriceRule.UpTo, For: rule.PriceRule.For}
	}
	c, err := purchase.FromCheckout(purchase.Opening{Tenant: id.Tenant, Agent: prop.ActorID, Checkout: prop.ID, Request: b.Ref, Payer: purchase.Payer{Kind: "account", ID: id.Tenant}}, read, cr)
	if err != nil {
		return refuse(reasonPurchaseCheckout, "the checkout can't be a purchase ("+err.Error()+"); nothing was bought")
	}
	c.CaseID = purchase.CaseIDFor(id.Tenant, prop.ID)
	row, err := po.cases.OpenOnce(ctx, c)
	if err != nil {
		if errors.Is(err, purchase.ErrCase) {
			return refuse(reasonPurchaseCheckout, "the checkout can't be a purchase ("+err.Error()+"); nothing was bought")
		}
		h.logger.ErrorContext(ctx, "proposals: purchase case not opened", slog.String("tenant", id.Tenant), slog.String("err", err.Error()))
		return refuse(reasonPurchaseCase, "KIFF couldn't open this purchase's case; nothing was bought")
	}
	o := &openedCase{row: row}
	switch row.State {
	case purchase.StateApproved, purchase.StateExecuting:
		// Decided already: a retry of the same pay.
		o.decided = true
		return o, proposalResponse{}, false
	case purchase.StateHeld:
	default:
		return refuse(reasonPurchaseCase, "this purchase's case has ended ("+row.State+"); a new checkout is a new purchase")
	}
	if rule.Mode == mandate.PurchaseWithinLimits {
		limits := purchase.LimitsFrom(rule.PerPurchase, rule.PerDay, rule.PerHour, rule.PerDayCount, rule.Categories).In(card.Location)
		err := po.cases.Decide(ctx, id.Tenant, row.Case.CaseID, row.Case.Version, row.Digest,
			purchase.Decision{By: purchase.ByCardLimits, Card: row.Case.Card, Limits: limits})
		switch {
		case err == nil:
			o.decided = true
			if r, lerr := po.cases.Latest(ctx, id.Tenant, row.Case.CaseID); lerr == nil {
				o.row = r
			}
		case errors.Is(err, purchase.ErrOutsideLimits), errors.Is(err, purchase.ErrRoomTaken):
			// Outside the limits, or their room just taken: the owner's.
		default:
			h.logger.ErrorContext(ctx, "proposals: purchase not decided within limits", slog.String("tenant", id.Tenant), slog.String("err", err.Error()))
			return refuse(reasonPurchaseCase, "KIFF couldn't decide this purchase; nothing was bought")
		}
	}
	return o, proposalResponse{}, false
}

// withPurchaseOpened is ctx for a pay's decision: one decided with its case
// moves money as that case allows; one that isn't waits for the owner.
func withPurchaseOpened(ctx context.Context, o *openedCase) context.Context {
	if o.decided {
		return withPurchaseDecided(ctx)
	}
	return withOwnerAsk(ctx, words.OwnerAskPurchase)
}

// afterPurchase links a held pay's hold to its case, so the owner's answer
// decides that case, and names the case on the answer.
func (h *ProposalsHandler) afterPurchase(ctx context.Context, tenant string, o *openedCase, resp *proposalResponse) {
	c := o.row.Case
	if resp.Outcome == string(outcomeOwnerRequired) && resp.Exception != nil && !o.decided {
		if err := h.purchaseOpening.cases.Hold(ctx, tenant, c.CaseID, c.Version, resp.Exception.ID); err != nil && !errors.Is(err, purchase.ErrNotHeld) {
			h.logger.ErrorContext(ctx, "proposals: purchase hold not linked", slog.String("tenant", tenant), slog.String("err", err.Error()))
		}
	}
	if resp.Outcome == string(outcomeAllowed) || resp.Outcome == string(outcomeOwnerRequired) {
		resp.Purchase = &purchaseRef{CaseID: c.CaseID, Version: c.Version, Digest: o.row.Digest}
	}
}

// checkoutBlockOf reads a signed checkout block: {block, sig}, planque's
// signature over it under its checkout label, and its fields, each once.
func checkoutBlockOf(raw json.RawMessage, verify func(json.RawMessage, string) bool) (purchase.CheckoutBlock, string) {
	if len(raw) == 0 || len(raw) > maxBrowserBlock {
		return purchase.CheckoutBlock{}, "no block, or one over 8 KiB"
	}
	if !plainKeys(raw) {
		return purchase.CheckoutBlock{}, "a block's keys are each written once, in one case"
	}
	var s signedBrowserBlock
	if strictDecode(raw, &s) != nil || len(s.Block) == 0 || s.Sig == "" {
		return purchase.CheckoutBlock{}, "a block travels as {block, sig}"
	}
	// The signature first: no field is read from a block planque didn't sign.
	if !verify(s.Block, s.Sig) {
		return purchase.CheckoutBlock{}, "the block isn't signed by planque as a checkout"
	}
	if !plainKeys(s.Block) {
		return purchase.CheckoutBlock{}, "a block's keys are each written once, in one case"
	}
	var b purchase.CheckoutBlock
	dec := json.NewDecoder(bytes.NewReader(s.Block))
	dec.DisallowUnknownFields()
	if dec.Decode(&b) != nil {
		return purchase.CheckoutBlock{}, "the block isn't " + planqueCheckoutLabel
	}
	return b, ""
}

// verifyPlanqueCheckout reports whether sig is planque's step key's
// signature over JCS(block) under its checkout label.
func verifyPlanqueCheckout(block json.RawMessage, sig string) bool {
	der, err := base64.RawURLEncoding.DecodeString(planqueStepKey)
	if err != nil {
		return false
	}
	pub, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		return false
	}
	k, ok := pub.(*ecdsa.PublicKey)
	if !ok || k.Curve.Params().Name != "P-256" {
		return false
	}
	return verifyLabel(k, planqueCheckoutLabel, block, sig)
}
