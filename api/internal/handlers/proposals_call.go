package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/kiff/cloud/api/internal/alert"
	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/domain"
	"github.com/kiff/cloud/api/internal/mandate"
	"github.com/kiff/cloud/api/internal/words"
	kiffproposal "github.com/kiff/kiff/pkg/kiff/proposal"
)

// A decision records what its call moves and what it is on (kiff/tafs#167),
// so the owner's record and the receipt can say "€150.00 on payment …":
//
//   - amount: the action's amount parameter, as {value, unit}, value a
//     decimal string in the unit's scale ("150.00"), never a float. The
//     amount is the parameter with a unit, or in the gateway's domain its
//     integer one, as for a draft's Cards (amountOf).
//   - on_arg and on_hash: in the gateway's domain, the action's one string
//     parameter, which is the tool's resource argument
//     (gateway/internal/gateway/contract.go), and the SHA-256 of its
//     value. The value itself is not kept, as for every string parameter.
//
// Each is left out when the action has none, or more than one.

type callFactsKey struct{}

// withCallFacts threads the call's facts to recordOutcome, the way
// withDecisionState threads its state. observed is the call's unit as the
// gateway read it, "" when it didn't: the amount is recorded in it.
func withCallFacts(ctx context.Context, spec domain.Spec, prop kiffproposal.ActionProposal, observed string) context.Context {
	facts := callFacts(spec, prop)
	if a, ok := facts["amount"].(map[string]any); ok && observed != "" && observed != UnitUnverified {
		a["unit"] = strings.ToUpper(observed)
	}
	if len(facts) == 0 {
		return ctx
	}
	return context.WithValue(ctx, callFactsKey{}, facts)
}

func callFactsFrom(ctx context.Context) map[string]any {
	f, _ := ctx.Value(callFactsKey{}).(map[string]any)
	return f
}

func callFacts(spec domain.Spec, prop kiffproposal.ActionProposal) map[string]any {
	var action *domain.ActionSpec
	for i := range spec.Actions {
		if spec.Actions[i].Name == prop.ActionName {
			action = &spec.Actions[i]
			break
		}
	}
	if action == nil {
		return nil
	}
	gateway := spec.Domain == gatewayDomain
	var amounts, ons []domain.ParameterSpec
	for _, p := range action.Parameters {
		switch {
		case p.Type == "int" && (p.Unit != "" || gateway):
			amounts = append(amounts, p)
		case p.Type == "string" && gateway:
			ons = append(ons, p)
		}
	}
	facts := map[string]any{}
	if len(amounts) == 1 {
		nums, _ := numericParameters(prop.Parameters)
		if v, ok := nums[strings.ToLower(amounts[0].Name)]; ok {
			amount := map[string]any{"value": scaledDecimal(v, amounts[0].Scale)}
			if amounts[0].Unit != "" {
				amount["unit"] = amounts[0].Unit
			}
			facts["amount"] = amount
		}
	}
	if len(ons) == 1 {
		if s, ok := prop.Parameters[ons[0].Name].(string); ok {
			facts["on_arg"] = ons[0].Name
			facts["on_hash"] = resourceHash(s)
		}
	}
	return facts
}

// resourceHash is how a decision records what a call is on: "sha256:" and
// the hex SHA-256 of the value's UTF-8 bytes. Anyone holding the value can
// check it; the record alone does not reveal it.
func resourceHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// scaledDecimal writes an amount in minor units as a decimal: 15000 at
// scale 2 is "150.00".
func scaledDecimal(minor int64, scale int) string {
	sign := ""
	if minor < 0 {
		sign, minor = "-", -minor
	}
	if scale <= 0 {
		return fmt.Sprintf("%s%d", sign, minor)
	}
	p := int64(1)
	for i := 0; i < scale; i++ {
		p *= 10
	}
	return fmt.Sprintf("%s%d.%0*d", sign, minor/p, scale, minor%p)
}

// GatewayUnitParam is the parameter the gateway sends a call's unit in, as
// its own read of the call's resource said it (a payment's currency), so
// KIFF records a call in another unit than its Card counts as blocked
// instead of counting it. UnitUnverified says the
// gateway couldn't read it. Only the gateway's domain reads it, and it is
// taken out of the parameters before anything else sees them.
const (
	GatewayUnitParam = "kiff_amount_unit"
	UnitUnverified   = "unverified"
)

// Reasons a decision is blocked for when the call's unit isn't its Card's.
const (
	reasonUnitMismatch   = "amount_unit_mismatch"
	reasonUnitUnverified = "amount_unit_unverified"
)

// GatewayAmountUnverifiedParam is the parameter the gateway sends, true,
// for a call whose amount it reads from the call's resource (amount_lookup)
// and couldn't. KIFF records the call as blocked, for the owner's
// record, and never counts it; the gateway didn't send it. As with the unit,
// only the gateway's domain reads it, and it is taken out first.
const GatewayAmountUnverifiedParam = "kiff_amount_unverified"

const reasonAmountUnverified = "amount_unverified"

// GatewayOwnerAskParam is the parameter the gateway sends when every call
// of a tool is the owner's to approve, however much room the Card has. Its
// value is a reason code (words.OwnerAsks), never words: the gateway
// decides with the agent's own key, so an agent can send it too, and
// nothing it sends may be read by the owner as KIFF's. A code KIFF doesn't
// know, or a value that isn't one, still asks, as words.OwnerAskOther. The
// Card's limits still apply, and a Card set to refuse what's outside it
// refuses the call (mandate.Request.OwnerAsk). As with the unit, only the
// gateway's domain reads it, and it is taken out first. Sending it only
// ever adds an ask.
const GatewayOwnerAskParam = "kiff_owner_ask"

// OwnerAskSealedMoney is the code for a tool that moves money on a
// connection where KIFF holds the key.
const OwnerAskSealedMoney = words.OwnerAskSealedMoney

// takeOwnerAsk takes GatewayOwnerAskParam out of a gateway call's
// parameters, leaving the caller's map as it was, and says the reason code
// every call of it asks its owner with ("" when it doesn't).
func takeOwnerAsk(spec domain.Spec, prop kiffproposal.ActionProposal) (string, kiffproposal.ActionProposal) {
	v, ok := prop.Parameters[GatewayOwnerAskParam]
	if !ok {
		return "", prop
	}
	params := make(map[string]any, len(prop.Parameters)-1)
	for k, p := range prop.Parameters {
		if k != GatewayOwnerAskParam {
			params[k] = p
		}
	}
	prop.Parameters = params
	if spec.Domain != gatewayDomain {
		return "", prop
	}
	code, _ := v.(string)
	return words.OwnerAskCode(code), prop
}

// ownerAskLine is KIFF's English line for an owner ask's code.
func ownerAskLine(code string) string { return words.Of(words.EN).OwnerAsks[words.OwnerAskCode(code)] }

// ownerAskWhyFrom is KIFF's English line for the code ctx asks with, or "".
func ownerAskWhyFrom(ctx context.Context) string {
	if code := ownerAskFrom(ctx); code != "" {
		return ownerAskLine(code)
	}
	return ""
}

type ownerAskKey struct{}

// withOwnerAsk marks ctx as deciding a call every one of which asks its
// owner, with code.
func withOwnerAsk(ctx context.Context, code string) context.Context {
	if code == "" {
		return ctx
	}
	return context.WithValue(ctx, ownerAskKey{}, code)
}

// ownerAskFrom is the code every call ctx decides asks its owner with (""
// when it doesn't).
func ownerAskFrom(ctx context.Context) string {
	code, _ := ctx.Value(ownerAskKey{}).(string)
	return code
}

// takeAmountUnverified takes GatewayAmountUnverifiedParam out of a gateway
// call's parameters, leaving the caller's map as it was, and says whether
// the gateway couldn't read the call's amount.
func takeAmountUnverified(spec domain.Spec, prop kiffproposal.ActionProposal) (bool, kiffproposal.ActionProposal) {
	v, ok := prop.Parameters[GatewayAmountUnverifiedParam]
	if !ok {
		return false, prop
	}
	params := make(map[string]any, len(prop.Parameters)-1)
	for k, p := range prop.Parameters {
		if k != GatewayAmountUnverifiedParam {
			params[k] = p
		}
	}
	prop.Parameters = params
	b, _ := v.(bool)
	return b && spec.Domain == gatewayDomain, prop
}

// amountRefusal is the blocked answer for a gateway call whose amount the
// gateway couldn't read.
func (h *ProposalsHandler) amountRefusal(ctx context.Context, id auth.Identity, prop kiffproposal.ActionProposal, unverified bool) (proposalResponse, bool) {
	if !unverified {
		return proposalResponse{}, false
	}
	reasons := []string{reasonAmountUnverified}
	h.recordOutcome(ctx, id, prop, outcomeBlocked, reasons)
	return proposalResponse{ProposalID: prop.ID, Outcome: string(outcomeBlocked), Reasons: reasons,
		Message: "KIFF couldn't read this call's amount, so it didn't count or send it."}, true
}

// takeObservedUnit takes GatewayUnitParam out of a gateway call's
// parameters, leaving the caller's map as it was.
func takeObservedUnit(spec domain.Spec, prop kiffproposal.ActionProposal) (string, kiffproposal.ActionProposal) {
	v, ok := prop.Parameters[GatewayUnitParam]
	if !ok {
		return "", prop
	}
	params := make(map[string]any, len(prop.Parameters)-1)
	for k, p := range prop.Parameters {
		if k != GatewayUnitParam {
			params[k] = p
		}
	}
	prop.Parameters = params
	s, _ := v.(string)
	if spec.Domain != gatewayDomain {
		return "", prop
	}
	return strings.TrimSpace(s), prop
}

// unitRefusal is the blocked answer for a gateway call in another unit
// than its action's amount, or one whose unit the gateway couldn't read.
func (h *ProposalsHandler) unitRefusal(ctx context.Context, id auth.Identity, prop kiffproposal.ActionProposal, spec domain.Spec, observed string) (proposalResponse, bool) {
	if observed == "" {
		return proposalResponse{}, false
	}
	unit := ""
	for _, a := range spec.Actions {
		if a.Name == prop.ActionName {
			for _, p := range a.Parameters {
				if p.Type == "int" && p.Unit != "" {
					unit = p.Unit
				}
			}
		}
	}
	var reasons []string
	var message string
	switch {
	case unit == "":
		return proposalResponse{}, false
	case observed == UnitUnverified:
		reasons, message = []string{reasonUnitUnverified}, "KIFF couldn't check this call's currency, so it didn't count or send it."
	case !strings.EqualFold(observed, unit):
		reasons = []string{reasonUnitMismatch}
		message = fmt.Sprintf("This call is in %s and the Card's limits are in %s, so KIFF didn't count or send it.", strings.ToUpper(observed), strings.ToUpper(unit))
	default:
		// In the Card's unit: an ask for it, if one is open, is answered.
		h.closeCardUnit(ctx, id, prop, observed)
		return proposalResponse{}, false
	}
	h.recordOutcome(ctx, id, prop, outcomeBlocked, reasons)
	if reasons[0] == reasonUnitMismatch {
		h.askCardUnit(ctx, id, prop, observed, unit)
	}
	return proposalResponse{ProposalID: prop.ID, Outcome: string(outcomeBlocked), Reasons: reasons, Message: message}, true
}

// CardUnitAsks keeps the owner's one ask per agent, action and unit for
// calls refused because their Card has no limit in their unit
// (kiff/tafs#464 item 11): a KindCardUnit exception, in Needs you.
type CardUnitAsks interface {
	AskCardUnit(ctx context.Context, a mandate.CardUnitAsk, now time.Time) (mandate.Exception, bool, error)
	CloseCardUnit(ctx context.Context, a mandate.CardUnitAsk, now time.Time) error
}

// WithProposalsCardUnitAsks installs where the ask is kept. Without it,
// such a call is refused as before, with no ask.
func WithProposalsCardUnitAsks(a CardUnitAsks) ProposalsOption {
	return func(h *ProposalsHandler) { h.cardUnits = a }
}

func cardUnitAsk(id auth.Identity, prop kiffproposal.ActionProposal, observed, unit string) mandate.CardUnitAsk {
	return mandate.CardUnitAsk{TenantID: id.Tenant, ProposalID: prop.ID, Subject: prop.ActorID, Action: prop.ActionName,
		EntityID: prop.EntityID, Unit: observed, CardUnit: unit}
}

// askCardUnit opens the ask for prop's refusal, or counts it on the open
// one. The refusal is recorded either way; only the first one alerts, as a
// hold does. Best effort: a store that fails leaves the refusal as it was.
func (h *ProposalsHandler) askCardUnit(ctx context.Context, id auth.Identity, prop kiffproposal.ActionProposal, observed, unit string) {
	if h.cardUnits == nil {
		return
	}
	e, opened, err := h.cardUnits.AskCardUnit(ctx, cardUnitAsk(id, prop, observed, unit), h.now())
	if err != nil {
		h.logger.WarnContext(ctx, "proposals: card unit ask not kept", slog.String("tenant", id.Tenant), slog.String("err", err.Error()))
		return
	}
	if opened && h.alerter != nil {
		h.alerter.Notify(alert.Event{TenantID: id.Tenant, ProposalID: prop.ID, Action: prop.ActionName, EntityID: prop.EntityID,
			EntityType: prop.EntityType, Outcome: string(outcomeOwnerRequired), Reasons: []string{reasonUnitMismatch}, DecidedAt: h.now(),
			Hold: &alert.Hold{ID: e.ID, Status: string(e.Status)}})
	}
}

// closeCardUnit answers the open ask for prop's action and unit, if any: a
// call in that unit reached its Card.
func (h *ProposalsHandler) closeCardUnit(ctx context.Context, id auth.Identity, prop kiffproposal.ActionProposal, observed string) {
	if h.cardUnits == nil {
		return
	}
	if err := h.cardUnits.CloseCardUnit(ctx, cardUnitAsk(id, prop, observed, ""), h.now()); err != nil {
		h.logger.WarnContext(ctx, "proposals: card unit ask not closed", slog.String("tenant", id.Tenant), slog.String("err", err.Error()))
	}
}
