package handlers

import (
	"context"
	"net/http"

	"github.com/kiff/cloud/api/internal/mandate"
	"github.com/kiff/cloud/api/internal/words"
)

// HeldCallWords is a held call in the person's words, for the email that tells
// them about it: the same lines every app's Needs you shows (withHeldWords), so
// the email says "Refund a payment", "€47.00" and "€47.00 is over the €40.00
// limit for one call", never an identifier or a unit's name.
type HeldCallWords struct {
	Action string // "Refund a payment"
	Amount string // "€47.00", empty when the call has no amount KIFF can name
	Reason string // why it is held, in the person's language
}

// HeldWordsSource reads a hold in the person's words. MandatesHandler is one.
type HeldWordsSource interface {
	HeldCallWords(ctx context.Context, tenantID, exceptionID string, lang words.Lang) (HeldCallWords, bool)
}

// HeldCallWords is hold exceptionID of the account in lang's words: the view
// Needs you serves, drawn the same way, and its action, amount and reason. It
// is false when the hold or its Card cannot be read, and the email then says what
// it can from the proposal.
func (h *MandatesHandler) HeldCallWords(ctx context.Context, tenantID, exceptionID string, lang words.Lang) (HeldCallWords, bool) {
	// The view's helpers read a request for its context and its language.
	r, err := http.NewRequestWithContext(ctx, http.MethodGet, "/", nil)
	if err != nil {
		return HeldCallWords{}, false
	}
	r.Header.Set("Accept-Language", string(lang))
	e, err := h.store.Exception(ctx, tenantID, exceptionID)
	if err != nil {
		return HeldCallWords{}, false
	}
	now := h.now()
	p, err := mandate.ReadException(ctx, h.store, e, now)
	if err != nil {
		return HeldCallWords{}, false
	}
	e = p.Exception
	one := []mandate.Exception{e}
	a, found := h.attribution(r, tenantID, one)[e.ID]
	found = matchingAttribution(e, a, found)
	args, argsOK := h.heldArguments(r, tenantID, one)
	units := h.amountUnits(r, tenantID)
	v := withNextSteps(units.annotate(withArguments(withOutcome(withAttribution(projectedExceptionView(p, now), a, found),
		h.toolOutcomes(r, tenantID, one), e), args, argsOK, e), e), h.nextSteps(r, tenantID, one), e)
	if t := h.toolTitles(r, tenantID)[e.Action]; t != "" {
		v["title"] = t
	}
	if vt := h.vendorTools(r, tenantID)[e.Action]; vt != "" {
		v["vendor_tool"] = vt
	}
	v = withHeldWords(v, e, h.appBaseURL, words.Of(lang))
	out := HeldCallWords{}
	out.Action, _ = v["summary"].(string)
	out.Amount, _ = v["amount_text"].(string)
	out.Reason, _ = v["reason_text"].(string)
	return out, out.Action != ""
}

var _ HeldWordsSource = (*MandatesHandler)(nil)
